// Package pgcsv turns a backup's data into spreadsheets: one CSV file per table, in a zip (Repo C design note
// BF06_YOUR_APPS_DATA §4.2, BF-06b). It reads what pg_restore --data-only writes for a backup, as text, so no database is
// restored and nothing from the app or a person becomes part of a command: the tables and their columns come from the
// backup itself, and every byte is read with bounds.
package pgcsv

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// A backup's spreadsheets: at most MaxTables files, a zip of at most MaxBytes, and rows of at most MaxLine bytes as
// pg_restore writes them.
const (
	MaxTables = 2000
	MaxBytes  = 1 << 30
	MaxLine   = 64 << 20
)

// Platform is the prefix of the tables the app's platform keeps for itself, such as its migrations and its timetable;
// they are left out, as they are of the counts on Repo C's card.
const Platform = "autobuild_"

var (
	// ErrData is a backup whose data does not read as pg_restore writes it.
	ErrData = errors.New("the backup's data could not be read")
	// ErrTooBig is a backup whose spreadsheets would pass MaxTables or MaxBytes.
	ErrTooBig = errors.New("the backup's data is too big for spreadsheets")
)

// Summary is what was written: the files and their rows.
type Summary struct {
	Tables int
	Rows   int64
}

// Convert reads pg_restore --data-only text from r and writes a zip to w: one CSV file per table that is not the
// platform's, named by the table, with a header row of its columns, and modified at the backup's time. An empty table
// is a file with its header row only. Anything it does not read as a table's data, such as settings and sequence
// values, is passed over. On an error the zip is unfinished and must not be offered.
func Convert(r io.Reader, w io.Writer, modified time.Time) (Summary, error) {
	in := bufio.NewReaderSize(r, 64<<10)
	out := &counter{w: w}
	zw := zip.NewWriter(out)
	names := map[string]bool{}
	var sum Summary
	for {
		line, err := readLine(in)
		if err == io.EOF {
			break
		}
		if err != nil {
			return sum, err
		}
		if !bytes.HasPrefix(line, []byte("COPY ")) {
			continue
		}
		schema, table, columns, ok := parseCopy(string(line))
		if !ok {
			return sum, ErrData
		}
		if strings.HasPrefix(table, Platform) {
			if _, err := copyRows(in, len(columns), nil, out); err != nil {
				return sum, err
			}
			continue
		}
		if sum.Tables++; sum.Tables > MaxTables {
			return sum, ErrTooBig
		}
		f, err := zw.CreateHeader(&zip.FileHeader{Name: fileName(schema, table, names), Method: zip.Deflate, Modified: modified})
		if err != nil {
			return sum, err
		}
		if _, err := f.Write([]byte("\ufeff")); err != nil {
			return sum, err
		}
		cw := csv.NewWriter(f)
		cw.UseCRLF = true
		if err := cw.Write(columns); err != nil {
			return sum, err
		}
		rows, err := copyRows(in, len(columns), cw, out)
		sum.Rows += rows
		if err != nil {
			return sum, err
		}
	}
	if err := zw.Close(); err != nil {
		return sum, err
	}
	return sum, out.err
}

// copyRows reads one table's rows up to its end marker and writes each to cw, a nil cw passing them over.
func copyRows(in *bufio.Reader, columns int, cw *csv.Writer, out *counter) (int64, error) {
	var rows int64
	for {
		line, err := readLine(in)
		if err == io.EOF {
			return rows, ErrData
		}
		if err != nil {
			return rows, err
		}
		if string(line) == `\.` {
			if cw != nil {
				cw.Flush()
				if err := cw.Error(); err != nil {
					return rows, err
				}
			}
			return rows, out.err
		}
		fields, ok := splitRow(line)
		if !ok || len(fields) != columns {
			return rows, ErrData
		}
		rows++
		if cw == nil {
			continue
		}
		for i, f := range fields {
			fields[i] = guard(f)
		}
		if err := cw.Write(fields); err != nil {
			return rows, err
		}
		if out.err != nil {
			return rows, out.err
		}
	}
}

// readLine is the next line without its newline; a line longer than MaxLine is an error, and a last line without a
// newline is read as it is.
func readLine(in *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		part, err := in.ReadSlice('\n')
		if len(line)+len(part) > MaxLine {
			return nil, ErrTooBig
		}
		line = append(line, part...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF && len(line) > 0 {
			return line, nil
		}
		if err != nil {
			return nil, err
		}
		return line[:len(line)-1], nil
	}
}

// parseCopy reads a table's data header, COPY schema.table (column, ...) FROM stdin;, with each name as quote_ident
// writes it.
func parseCopy(line string) (schema, table string, columns []string, ok bool) {
	rest, found := strings.CutPrefix(line, "COPY ")
	if !found {
		return "", "", nil, false
	}
	rest, found = strings.CutSuffix(rest, " FROM stdin;")
	if !found {
		return "", "", nil, false
	}
	schema, rest, ok = ident(rest)
	if !ok || !strings.HasPrefix(rest, ".") {
		return "", "", nil, false
	}
	table, rest, ok = ident(rest[1:])
	if !ok {
		return "", "", nil, false
	}
	columns = []string{}
	if rest == "" {
		return schema, table, columns, true
	}
	rest, found = strings.CutPrefix(rest, " (")
	if !found || !strings.HasSuffix(rest, ")") {
		return "", "", nil, false
	}
	rest = strings.TrimSuffix(rest, ")")
	for {
		var c string
		c, rest, ok = ident(rest)
		if !ok {
			return "", "", nil, false
		}
		columns = append(columns, c)
		if rest == "" {
			return schema, table, columns, true
		}
		if rest, found = strings.CutPrefix(rest, ", "); !found {
			return "", "", nil, false
		}
	}
}

// ident reads one name at the start of s, quoted ("Order Items", with "" for a quote) or not, and what follows it.
func ident(s string) (string, string, bool) {
	if strings.HasPrefix(s, `"`) {
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			if s[i] != '"' {
				b.WriteByte(s[i])
				continue
			}
			if i+1 < len(s) && s[i+1] == '"' {
				b.WriteByte('"')
				i++
				continue
			}
			return b.String(), s[i+1:], b.Len() > 0
		}
		return "", "", false
	}
	end := strings.IndexAny(s, ".,() ")
	if end < 0 {
		end = len(s)
	}
	return s[:end], s[end:], end > 0
}

// splitRow reads one row of COPY's text format: fields split by tabs, \N for null (an empty field here), and
// backslash escapes.
func splitRow(line []byte) ([]string, bool) {
	var fields []string
	for _, raw := range bytes.Split(line, []byte{'\t'}) {
		if string(raw) == `\N` {
			fields = append(fields, "")
			continue
		}
		f, ok := unescape(raw)
		if !ok {
			return nil, false
		}
		fields = append(fields, f)
	}
	return fields, true
}

// unescape decodes COPY's backslash escapes: \b \f \n \r \t \v, \\, up to three octal digits, \x and up to two hex
// digits, and any other character as itself.
func unescape(raw []byte) (string, bool) {
	if bytes.IndexByte(raw, '\\') < 0 {
		return string(raw), true
	}
	var b strings.Builder
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(raw) {
			return "", false
		}
		switch c = raw[i]; {
		case c == 'b':
			b.WriteByte('\b')
		case c == 'f':
			b.WriteByte('\f')
		case c == 'n':
			b.WriteByte('\n')
		case c == 'r':
			b.WriteByte('\r')
		case c == 't':
			b.WriteByte('\t')
		case c == 'v':
			b.WriteByte('\v')
		case c >= '0' && c <= '7':
			v, n := 0, 0
			for ; n < 3 && i+n < len(raw) && raw[i+n] >= '0' && raw[i+n] <= '7'; n++ {
				v = v*8 + int(raw[i+n]-'0')
			}
			b.WriteByte(byte(v))
			i += n - 1
		case c == 'x' && i+1 < len(raw) && isHex(raw[i+1]):
			v, n := 0, 0
			for ; n < 2 && i+1+n < len(raw) && isHex(raw[i+1+n]); n++ {
				d, _ := strconv.ParseUint(string(raw[i+1+n]), 16, 8)
				v = v*16 + int(d)
			}
			b.WriteByte(byte(v))
			i += n
		default:
			b.WriteByte(c)
		}
	}
	return b.String(), true
}

func isHex(c byte) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F' }

// guard keeps a spreadsheet from running a value as a formula: one that starts with =, +, -, @, a tab or a carriage
// return, and is not a number, gets a leading apostrophe.
func guard(v string) string {
	if v == "" || !strings.ContainsAny(v[:1], "=+-@\t\r") {
		return v
	}
	if _, err := strconv.ParseFloat(v, 64); err == nil {
		return v
	}
	return "'" + v
}

// fileName names a table's file: its name, with its schema unless that is public, as letters, digits, _, - and ., at
// most 100 bytes, and made unique among the zip's files.
func fileName(schema, table string, taken map[string]bool) string {
	name := table
	if schema != "public" {
		name = schema + "." + table
	}
	var b strings.Builder
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' && r != '.' {
			r = '_'
		}
		if b.Len()+utf8.RuneLen(r) > 100 {
			break
		}
		b.WriteRune(r)
	}
	base := b.String()
	if base == "" || strings.HasPrefix(base, ".") {
		base = "_" + base
	}
	candidate := base + ".csv"
	for n := 2; taken[strings.ToLower(candidate)]; n++ {
		candidate = base + "-" + strconv.Itoa(n) + ".csv"
	}
	taken[strings.ToLower(candidate)] = true
	return candidate
}

// counter passes the zip's writes on and refuses any past MaxBytes.
type counter struct {
	w   io.Writer
	n   int64
	err error
}

func (c *counter) Write(p []byte) (int, error) {
	if c.err != nil {
		return 0, c.err
	}
	if c.n+int64(len(p)) > MaxBytes {
		c.err = ErrTooBig
		return 0, c.err
	}
	n, err := c.w.Write(p)
	c.n += int64(n)
	if err != nil {
		c.err = err
	}
	return n, err
}

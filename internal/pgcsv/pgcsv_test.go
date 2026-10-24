package pgcsv

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
)

// dump is what pg_restore --data-only writes for a small app: settings, a table per COPY block, sequence values.
const dump = `--
-- PostgreSQL database dump
--
\restrict k3y
SET statement_timeout = 0;
SET client_encoding = 'UTF8';

--
-- Data for Name: autobuild_migrations; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.autobuild_migrations (id, name) FROM stdin;
1	0001_init.sql
\.


--
-- Data for Name: Order Items; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public."Order Items" (id, "unit ""price""", note) FROM stdin;
1	12.50	first, with a comma
2	-3	=HYPERLINK("http://x")
3	\N	line one\nline two\ttabbed \\ back \101\x42
\.


COPY audit.events (id) FROM stdin;
\.


COPY public.users (id, email, phone) FROM stdin;
1	asha@example.com	+91 98765 43210
\.


SELECT pg_catalog.setval('public.users_id_seq', 1, true);
\unrestrict k3y
`

func open(t *testing.T, b []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(r)
		files[f.Name] = string(data)
		if !f.Modified.Equal(time.Date(2026, 10, 24, 17, 13, 33, 0, time.UTC)) {
			t.Fatalf("%s modified %v", f.Name, f.Modified)
		}
	}
	return files
}

func TestABackupsTablesBecomeOneSpreadsheetEach(t *testing.T) {
	var out bytes.Buffer
	sum, err := Convert(strings.NewReader(dump), &out, time.Date(2026, 10, 24, 17, 13, 33, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if sum != (Summary{Tables: 3, Rows: 4}) {
		t.Fatalf("summary %+v", sum)
	}
	files := open(t, out.Bytes())
	want := map[string]string{
		"Order_Items.csv": "\ufeffid,\"unit \"\"price\"\"\",note\r\n" +
			"1,12.50,\"first, with a comma\"\r\n" +
			"2,-3,\"'=HYPERLINK(\"\"http://x\"\")\"\r\n" +
			"3,,\"line one\r\nline two\ttabbed \\ back AB\"\r\n", // a newline inside a value is CRLF, as the lines are
		"audit.events.csv": "\ufeffid\r\n",
		"users.csv":        "\ufeffid,email,phone\r\n1,asha@example.com,'+91 98765 43210\r\n",
	}
	if !reflect.DeepEqual(files, want) {
		t.Fatalf("files\n%q\nwant\n%q", files, want)
	}
}

func TestDataThatDoesNotReadAsPgRestoreWritesItIsRefused(t *testing.T) {
	for _, bad := range []string{
		"COPY public.t (a, b) FROM stdin;\n1\t2\n",         // no end marker
		"COPY public.t (a, b) FROM stdin;\n1\t2\t3\n\\.\n", // a field too many
		"COPY public.t (a, b) FROM stdin;\n1\t2\\\n\\.\n",  // a lone backslash
		"COPY t (a) FROM stdin;\n\\.\n",                    // no schema
		"COPY public.\"t (a) FROM stdin;\n\\.\n",           // an open quote
		"COPY public.t (a,b) FROM stdin;\n\\.\n",           // not as quote_ident lists them
		"COPY public.t (a) FROM '/etc/passwd';\n\\.\n",     // not from stdin
	} {
		if _, err := Convert(strings.NewReader(bad), io.Discard, time.Now()); !errors.Is(err, ErrData) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	var out bytes.Buffer
	if sum, err := Convert(strings.NewReader("SET x = 1;\n"), &out, time.Now()); err != nil || sum.Tables != 0 || len(open2(t, out.Bytes())) != 0 {
		t.Fatalf("a backup with no tables is an empty zip: %+v %v", sum, err)
	}
}

func open2(t *testing.T, b []byte) []*zip.File {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	return zr.File
}

func TestSpreadsheetsAreBounded(t *testing.T) {
	var many strings.Builder
	for i := 0; i <= MaxTables; i++ {
		many.WriteString("COPY public.t (a) FROM stdin;\n\\.\n")
	}
	if _, err := Convert(strings.NewReader(many.String()), io.Discard, time.Now()); !errors.Is(err, ErrTooBig) {
		t.Fatalf("too many tables: %v", err)
	}
	long := "COPY public.t (a) FROM stdin;\n" + strings.Repeat("x", MaxLine+1) + "\n\\.\n"
	if _, err := Convert(strings.NewReader(long), io.Discard, time.Now()); !errors.Is(err, ErrTooBig) {
		t.Fatalf("a row too long: %v", err)
	}
	c := &counter{w: io.Discard, n: MaxBytes - 1}
	if _, err := c.Write([]byte("ab")); !errors.Is(err, ErrTooBig) {
		t.Fatalf("a zip too big: %v", err)
	}
}

func TestNamesAreReadAndWrittenSafely(t *testing.T) {
	for _, c := range []struct {
		line, schema, table string
		columns             []string
	}{
		{`COPY public.orders (id, "Total", "a ""b""") FROM stdin;`, "public", "orders", []string{"id", "Total", `a "b"`}},
		{`COPY "My Schema"."x.y" FROM stdin;`, "My Schema", "x.y", []string{}},
	} {
		s, tb, cols, ok := parseCopy(c.line)
		if !ok || s != c.schema || tb != c.table || !reflect.DeepEqual(cols, c.columns) {
			t.Fatalf("%s: %q %q %q %v", c.line, s, tb, cols, ok)
		}
	}
	taken := map[string]bool{}
	got := []string{fileName("public", "../../etc/passwd", taken), fileName("public", "Orders", taken), fileName("public", "orders", taken),
		fileName("public", strings.Repeat("é", 80), taken), fileName("public", ".hidden", taken)}
	want := []string{"_.._.._etc_passwd.csv", "Orders.csv", "orders-2.csv", strings.Repeat("é", 50) + ".csv", "_.hidden.csv"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
	for v, w := range map[string]string{"-12.5": "-12.5", "+3": "+3", "@home": "'@home", "\tx": "'\tx", "plain": "plain", "": ""} {
		if guard(v) != w {
			t.Fatalf("guard(%q) = %q", v, guard(v))
		}
	}
}

// Package applog reads an app's own output for Autobuild (Repo C design note BF07_WHEN_A_RUNNING_APP_GOES_WRONG §4.3): which
// lines read as errors, and what in a line looks private and is replaced before it leaves ABCP.
package applog

import (
	"regexp"
	"strings"
)

// errorLine is what makes a line of an app's output read as an error: the words Node and its libraries print for one,
// the system's error codes, and a stack frame. Servers apply the same pattern with grep (remotetarget).
var errorLine = regexp.MustCompile(`(?i)error|exception|fatal|unhandled|uncaught|refused|denied|failed|crash|panic|ECONN|EADDR|ENOENT|EACCES|timed? ?out|traceback|^\s+at `)

// ErrorLine reports whether a line of an app's output reads as an error.
func ErrorLine(line string) bool { return errorLine.MatchString(line) }

// Printable drops control characters, keeping tabs as spaces.
func Printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

var redactions = []struct {
	pattern *regexp.Regexp
	with    string
}{
	// A connection string's password: scheme://user:password@host.
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^\s:/@]+:[^\s@/]+@`), "${1}[hidden]@"},
	// Bearer and Basic credentials.
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`), "$1 [token]"},
	// key=value and "key": "value" pairs whose name says secret.
	{regexp.MustCompile(`(?i)\b([a-z0-9_.-]*(password|passwd|pwd|secret|token|api[_-]?key|apikey|auth|cookie|session|signature|private[_-]?key)[a-z0-9_.-]*)("?\s*[:=]\s*"?)[^\s",;&}]+`), "${1}${3}[hidden]"},
	// A JSON Web Token.
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`), "[token]"},
	// Keys with a known prefix.
	{regexp.MustCompile(`\b(sk|pk|rk|rzp|whsec|ghp|gho|github_pat|xox[abpr])[_-][A-Za-z0-9_-]{8,}\b`), "[key]"},
	// An email address.
	{regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`), "[email]"},
}

var (
	// Long runs of hex, and of base64 that mixes digits with both cases, read
	// as secrets; a file path in a stack trace does not.
	hexPattern    = regexp.MustCompile(`\b[A-Fa-f0-9]{32,}\b`)
	base64Pattern = regexp.MustCompile(`[A-Za-z0-9+/_-]{40,}={0,2}`)
	// A phone number: a run of ten or more digits, with spaces, dots, dashes,
	// brackets or a leading +. Dates are set aside first, so a time stamp
	// stays.
	numberPattern = regexp.MustCompile(`\+?\d[\d .()-]{8,}\d`)
	datePattern   = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}\b`)
)

// Redact replaces what looks like a secret, a sign-in token, an email address
// or a phone number in one line by fixed words.
func Redact(line string) string {
	line = strings.ReplaceAll(line, "\x00", "")
	for _, r := range redactions {
		line = r.pattern.ReplaceAllString(line, r.with)
	}
	line = hexPattern.ReplaceAllString(line, "[secret]")
	line = base64Pattern.ReplaceAllStringFunc(line, func(run string) string {
		var digit, upper, lower bool
		for _, r := range run {
			switch {
			case r >= '0' && r <= '9':
				digit = true
			case r >= 'A' && r <= 'Z':
				upper = true
			case r >= 'a' && r <= 'z':
				lower = true
			}
		}
		if digit && upper && lower {
			return "[secret]"
		}
		return run
	})
	var dates []string
	line = datePattern.ReplaceAllStringFunc(line, func(date string) string {
		dates = append(dates, date)
		return "\x00"
	})
	line = numberPattern.ReplaceAllStringFunc(line, func(number string) string {
		digits := 0
		for _, r := range number {
			if r >= '0' && r <= '9' {
				digits++
			}
		}
		if digits < 10 {
			return number
		}
		return "[number]"
	})
	if len(dates) > 0 {
		parts := strings.Split(line, "\x00")
		var b strings.Builder
		for i, part := range parts {
			b.WriteString(part)
			if i < len(dates) && i < len(parts)-1 {
				b.WriteString(dates[i])
			}
		}
		line = b.String()
	}
	return line
}

package db

import (
	"strings"

	"gotable/internal/model"
)

// Span is a statement and its byte range in the source script.
type Span struct {
	SQL        string
	Start, End int
}

// SplitStatements splits a script into statements, honoring quotes,
// comments, PostgreSQL dollar quoting, SQLite trigger bodies and the MySQL
// client DELIMITER command. Empty statements are dropped.
func SplitStatements(kind model.Driver, script string) []Span {
	mysql := kind == model.DriverMySQL || kind == model.DriverMariaDB
	var out []Span
	delim := ";"
	start := 0
	depth := 0 // BEGIN ... END nesting inside CREATE TRIGGER
	emit := func(end int) {
		s := strings.TrimSpace(script[start:end])
		if s != "" && (!onlyComments(s) || mysql && strings.Contains(s, "/*!")) {
			lead := len(script[start:end]) - len(strings.TrimLeft(script[start:end], " \t\r\n"))
			out = append(out, Span{SQL: s, Start: start + lead, End: end})
		}
	}
	n := len(script)
	for i := 0; i < n; {
		ch := script[i]
		// MySQL client DELIMITER command at line start.
		if mysql && atLineStart(script, i) && hasPrefixFold(script[i:], "DELIMITER ") {
			eol := strings.IndexByte(script[i:], '\n')
			if eol < 0 {
				eol = n - i
			}
			emit(i)
			if d := strings.TrimSpace(script[i+len("DELIMITER ") : i+eol]); d != "" {
				delim = d
			}
			i += eol
			start = i
			continue
		}
		switch {
		case ch == '\'' || ch == '"' || ch == '`':
			i = skipQuoted(script, i, ch, mysql)
			continue
		case ch == '-' && i+1 < n && script[i+1] == '-', mysql && ch == '#':
			if eol := strings.IndexByte(script[i:], '\n'); eol >= 0 {
				i += eol
			} else {
				i = n
			}
			continue
		case ch == '/' && i+1 < n && script[i+1] == '*':
			if end := strings.Index(script[i+2:], "*/"); end >= 0 {
				i += end + 4
			} else {
				i = n
			}
			continue
		case ch == '$' && kind == model.DriverPostgres:
			if tag, ok := dollarTag(script[i:]); ok {
				if end := strings.Index(script[i+len(tag):], tag); end >= 0 {
					i += len(tag) + end + len(tag)
				} else {
					i = n
				}
				continue
			}
		case kind == model.DriverSQLite && isWordAt(script, i, "BEGIN") && inTrigger(script[start:i]):
			depth++
		case kind == model.DriverSQLite && depth > 0 && isWordAt(script, i, "END"):
			depth--
		}
		if depth == 0 && strings.HasPrefix(script[i:], delim) {
			emit(i)
			i += len(delim)
			start = i
			continue
		}
		i++
	}
	emit(n)
	return out
}

// StatementAt returns the statement containing byte offset pos (the cursor
// position in the editor). A cursor just after a terminator selects the
// preceding statement.
func StatementAt(kind model.Driver, script string, pos int) (Span, bool) {
	spans := SplitStatements(kind, script)
	var best Span
	found := false
	for _, s := range spans {
		if pos >= s.Start && pos <= s.End+1 {
			return s, true
		}
		if s.End < pos {
			best, found = s, true
		}
	}
	return best, found
}

func skipQuoted(s string, i int, q byte, backslash bool) int {
	n := len(s)
	for j := i + 1; j < n; j++ {
		switch s[j] {
		case '\\':
			if backslash && q != '`' {
				j++
			}
		case q:
			if j+1 < n && s[j+1] == q {
				j++
				continue
			}
			return j + 1
		}
	}
	return n
}

func dollarTag(s string) (string, bool) {
	for j := 1; j < len(s); j++ {
		c := s[j]
		if c == '$' {
			return s[:j+1], true
		}
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || j > 1 && c >= '0' && c <= '9') {
			return "", false
		}
	}
	return "", false
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func isWordAt(s string, i int, w string) bool {
	if !hasPrefixFold(s[i:], w) {
		return false
	}
	if i > 0 && isIdentByte(s[i-1]) {
		return false
	}
	end := i + len(w)
	return end >= len(s) || !isIdentByte(s[end])
}

func inTrigger(stmt string) bool {
	return strings.Contains(strings.ToUpper(stmt), "TRIGGER")
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && strings.EqualFold(s[:len(prefix)], prefix)
}

func atLineStart(s string, i int) bool {
	for j := i - 1; j >= 0; j-- {
		switch s[j] {
		case '\n':
			return true
		case ' ', '\t', '\r':
		default:
			return false
		}
	}
	return true
}

func onlyComments(s string) bool {
	for len(s) > 0 {
		s = strings.TrimSpace(s)
		switch {
		case strings.HasPrefix(s, "--") || strings.HasPrefix(s, "#"):
			eol := strings.IndexByte(s, '\n')
			if eol < 0 {
				return true
			}
			s = s[eol:]
		case strings.HasPrefix(s, "/*"):
			end := strings.Index(s, "*/")
			if end < 0 {
				return true
			}
			s = s[end+2:]
		default:
			return s == ""
		}
	}
	return true
}

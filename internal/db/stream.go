package db

import (
	"bufio"
	"bytes"
	"io"
	"regexp"
	"strings"

	"gotable/internal/model"
)

// Stmt is one statement read from a script.
type Stmt struct {
	SQL  string
	Line int // 1-based line where the statement starts
	// Copy is the data of a PostgreSQL "COPY … FROM stdin" statement, up to
	// the terminating "\." line. It must be read (or is skipped) before the
	// next call to Next.
	Copy io.Reader
}

// Scanner reads statements one at a time from a script of any size,
// without holding more than the current statement in memory.
type Scanner struct {
	r     *bufio.Reader
	kind  model.Driver
	mysql bool
	delim string
	line  int
	copy  *copyReader
	buf   []byte
}

// NewScanner returns a Scanner for the dialect of kind.
func NewScanner(r io.Reader, kind model.Driver) *Scanner {
	return &Scanner{r: bufio.NewReaderSize(r, 1<<20), kind: kind, mysql: kind == model.DriverMySQL || kind == model.DriverMariaDB, delim: ";", line: 1}
}

var copyFromStdin = regexp.MustCompile(`(?is)^COPY\s.+\sFROM\s+stdin\b`)

func isIdent(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c >= 0x80
}

// Next returns the next statement, or io.EOF after the last one.
func (s *Scanner) Next() (Stmt, error) {
	if s.copy != nil {
		if _, err := io.Copy(io.Discard, s.copy); err != nil {
			return Stmt{}, err
		}
		s.copy = nil
	}
	s.buf = s.buf[:0]
	var (
		quote        byte
		lineComment  bool
		blockComment bool
		dollarTag    []byte
		meaningful   bool // non-comment, non-space content seen
		lineStart    = true
		startLine    = s.line
		word         []byte // current identifier, for BEGIN/END in triggers
		sawTrigger   bool
		depth        int
	)
	endWord := func() {
		if len(word) == 0 {
			return
		}
		if s.kind == model.DriverSQLite {
			switch strings.ToUpper(string(word)) {
			case "TRIGGER":
				sawTrigger = true
			case "BEGIN":
				if sawTrigger {
					depth++
				}
			case "END":
				if depth > 0 {
					depth--
				}
			}
		}
		word = word[:0]
	}
	emit := func() (Stmt, bool) {
		sql := strings.TrimSpace(string(s.buf))
		if !meaningful || sql == "" {
			return Stmt{}, false
		}
		st := Stmt{SQL: sql, Line: startLine}
		// pg_dump writes comments right before COPY; look past them.
		if s.kind == model.DriverPostgres && copyFromStdin.MatchString(stripLeadingComments(sql)) {
			// The data starts on the line after the statement.
			if _, err := s.r.ReadSlice('\n'); err == nil {
				s.line++
			}
			s.copy = &copyReader{s: s}
			st.Copy = s.copy
		}
		return st, true
	}
	for {
		c, err := s.r.ReadByte()
		if err == io.EOF {
			endWord()
			if st, ok := emit(); ok {
				return st, nil
			}
			return Stmt{}, io.EOF
		}
		if err != nil {
			return Stmt{}, err
		}
		if c == '\n' {
			s.line++
		}
		switch {
		case lineComment:
			s.buf = append(s.buf, c)
			if c == '\n' {
				lineComment = false
				lineStart = true
			}
			continue
		case blockComment:
			s.buf = append(s.buf, c)
			if c == '*' {
				if p, _ := s.r.Peek(1); len(p) == 1 && p[0] == '/' {
					s.r.ReadByte()
					s.buf = append(s.buf, '/')
					blockComment = false
				}
			}
			continue
		case quote != 0:
			s.buf = append(s.buf, c)
			if c == '\\' && s.mysql && quote != '`' {
				if n, err := s.r.ReadByte(); err == nil {
					if n == '\n' {
						s.line++
					}
					s.buf = append(s.buf, n)
				}
			} else if c == quote {
				if p, _ := s.r.Peek(1); len(p) == 1 && p[0] == quote {
					s.r.ReadByte()
					s.buf = append(s.buf, quote)
				} else {
					quote = 0
				}
			}
			continue
		case dollarTag != nil:
			s.buf = append(s.buf, c)
			if c == '$' && bytes.HasSuffix(s.buf, dollarTag) {
				dollarTag = nil
			}
			continue
		}

		// Commands that occupy a whole line between statements.
		if lineStart && !meaningful {
			if s.mysql && (c == 'D' || c == 'd') {
				if p, _ := s.r.Peek(9); len(p) == 9 && strings.EqualFold("ELIMITER ", string(p)) {
					line, _ := s.r.ReadString('\n')
					s.line++
					if d := strings.TrimSpace(line[9:]); d != "" {
						s.delim = d
					}
					s.buf = s.buf[:0]
					continue
				}
			}
			if s.kind == model.DriverPostgres && c == '\\' {
				// psql meta-command (\connect, \restrict, …): skip the line.
				if _, err := s.r.ReadString('\n'); err == nil {
					s.line++
				}
				s.buf = s.buf[:0]
				continue
			}
		}

		if isIdent(c) {
			word = append(word, c)
		} else {
			endWord()
		}

		switch {
		case c == '-' && peekIs(s.r, '-'), c == '#' && s.mysql:
			lineComment = true
			s.buf = append(s.buf, c)
			continue
		case c == '/' && peekIs(s.r, '*'):
			s.r.ReadByte()
			s.buf = append(s.buf, '/', '*')
			// MySQL executes /*!NNNNN … */ version comments.
			if s.mysql && peekIs(s.r, '!') {
				meaningful = true
			}
			blockComment = true
			continue
		case c == '\'' || c == '"' || c == '`' && s.mysql:
			quote = c
			meaningful = true
			s.buf = append(s.buf, c)
			continue
		case c == '$' && s.kind == model.DriverPostgres && (len(s.buf) == 0 || !isIdent(s.buf[len(s.buf)-1])):
			if tag := peekDollarTag(s.r); tag != nil {
				s.r.Discard(len(tag) - 1)
				s.buf = append(s.buf, tag...)
				dollarTag = append([]byte(nil), tag...)
				meaningful = true
				continue
			}
		}

		if depth == 0 && c == s.delim[0] && (len(s.delim) == 1 || peekString(s.r, s.delim[1:])) {
			s.r.Discard(len(s.delim) - 1)
			endWord()
			if st, ok := emit(); ok {
				return st, nil
			}
			s.buf = s.buf[:0]
			meaningful, sawTrigger, depth = false, false, 0
			startLine = s.line
			continue
		}

		s.buf = append(s.buf, c)
		if c == '\n' {
			lineStart = true
			if !meaningful {
				startLine = s.line
			}
		} else if c != ' ' && c != '\t' && c != '\r' {
			lineStart = false
			meaningful = true
		}
	}
}

func peekIs(r *bufio.Reader, b byte) bool {
	p, _ := r.Peek(1)
	return len(p) == 1 && p[0] == b
}

func peekString(r *bufio.Reader, s string) bool {
	p, _ := r.Peek(len(s))
	return string(p) == s
}

// peekDollarTag returns "$tag$" if the reader (positioned after a '$')
// continues with "tag$" or "$".
func peekDollarTag(r *bufio.Reader) []byte {
	p, _ := r.Peek(64)
	for i, c := range p {
		if c == '$' {
			return append([]byte{'$'}, p[:i+1]...)
		}
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9') {
			return nil
		}
	}
	return nil
}

// copyReader returns COPY data lines until the "\." terminator.
type copyReader struct {
	s    *Scanner
	line []byte
	done bool
}

func (c *copyReader) Read(p []byte) (int, error) {
	for len(c.line) == 0 {
		if c.done {
			return 0, io.EOF
		}
		line, err := c.s.r.ReadBytes('\n')
		if len(line) > 0 && line[len(line)-1] == '\n' {
			c.s.line++
		}
		if t := bytes.TrimRight(line, "\r\n"); string(t) == `\.` {
			c.done = true
			return 0, io.EOF
		}
		if err == io.EOF {
			c.done = true
			if len(line) == 0 {
				return 0, io.ErrUnexpectedEOF
			}
		} else if err != nil {
			return 0, err
		}
		c.line = line
	}
	n := copy(p, c.line)
	c.line = c.line[n:]
	return n, nil
}

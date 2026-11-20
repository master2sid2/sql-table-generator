package sqlgen

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

type tokKind int

const (
	tkEOF    tokKind = iota
	tkWord           // bare identifier or keyword
	tkIdent          // quoted identifier: "x", `x`, [x]
	tkString         // 'x', N'x', E'x'
	tkNumber         // 12, 1.5e3, 0xFF, X'FF', b'01'
	tkPunct
)

type token struct {
	kind       tokKind
	val        string // unquoted value
	start, end int    // byte offsets in the source
	lineStart  bool   // first token on its line
}

// is reports whether the token is one of the (case-insensitive) words.
func (t token) is(words ...string) bool {
	if t.kind != tkWord {
		return false
	}
	for _, w := range words {
		if strings.EqualFold(t.val, w) {
			return true
		}
	}
	return false
}

func (t token) isPunct(p string) bool { return t.kind == tkPunct && t.val == p }
func (t token) isName() bool          { return t.kind == tkWord || t.kind == tkIdent }

type lexer struct {
	src    string
	pos    int
	mysql  bool // backslash escapes in strings and '#' comments
	peeked *token
}

func (lx *lexer) peek() token {
	if lx.peeked == nil {
		t := lx.scan()
		lx.peeked = &t
	}
	return *lx.peeked
}

func (lx *lexer) next() token {
	t := lx.peek()
	lx.peeked = nil
	return t
}

func (lx *lexer) at(i int, c byte) bool { return i < len(lx.src) && lx.src[i] == c }

func (lx *lexer) skipSpace() {
	for lx.pos < len(lx.src) {
		c := lx.src[lx.pos]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			lx.pos++
		case c == '-' && lx.at(lx.pos+1, '-'), c == '#' && lx.mysql:
			if i := strings.IndexByte(lx.src[lx.pos:], '\n'); i >= 0 {
				lx.pos += i + 1
			} else {
				lx.pos = len(lx.src)
			}
		case c == '/' && lx.at(lx.pos+1, '*'):
			if i := strings.Index(lx.src[lx.pos+2:], "*/"); i >= 0 {
				lx.pos += i + 4
			} else {
				lx.pos = len(lx.src)
			}
		default:
			r, size := utf8.DecodeRuneInString(lx.src[lx.pos:])
			if !unicode.IsSpace(r) && r != 0xFEFF {
				return
			}
			lx.pos += size
		}
	}
}

func (lx *lexer) lineStartAt(p int) bool {
	for ; p > 0; p-- {
		switch lx.src[p-1] {
		case '\n':
			return true
		case ' ', '\t', '\r':
		default:
			return false
		}
	}
	return true
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHex(c byte) bool { return isDigit(c) || (c|0x20) >= 'a' && (c|0x20) <= 'f' }

func (lx *lexer) scan() token {
	lx.skipSpace()
	start := lx.pos
	t := token{start: start, lineStart: lx.lineStartAt(start)}
	if start >= len(lx.src) {
		t.end = start
		return t
	}
	c := lx.src[start]
	prefixed := lx.at(start+1, '\'')
	switch {
	case c == '\'':
		t.kind, t.val = tkString, lx.quoted('\'', lx.mysql)
	case prefixed && (c == 'N' || c == 'n'):
		lx.pos++
		t.kind, t.val = tkString, lx.quoted('\'', lx.mysql)
	case prefixed && (c == 'E' || c == 'e'):
		lx.pos++
		t.kind, t.val = tkString, lx.quoted('\'', true)
	case prefixed && (c == 'X' || c == 'x'):
		lx.pos++
		t.kind, t.val = tkNumber, "0x"+lx.quoted('\'', false)
	case prefixed && (c == 'B' || c == 'b'):
		lx.pos++
		lx.quoted('\'', false)
		t.kind, t.val = tkNumber, lx.src[start:lx.pos]
	case c == '"' || c == '`':
		t.kind, t.val = tkIdent, lx.quoted(c, false)
	case c == '[' && start+1 < len(lx.src) && lx.src[start+1] != ']' && !isDigit(lx.src[start+1]):
		t.kind, t.val = tkIdent, lx.bracketed()
	case isDigit(c) || c == '.' && start+1 < len(lx.src) && isDigit(lx.src[start+1]):
		t.kind = tkNumber
		lx.number()
		t.val = lx.src[start:lx.pos]
	case c == ':' && lx.at(start+1, ':'):
		lx.pos += 2
		t.kind, t.val = tkPunct, "::"
	default:
		r, size := utf8.DecodeRuneInString(lx.src[start:])
		if isWordStart(r, lx.mysql) {
			lx.pos += size
			for lx.pos < len(lx.src) {
				r, size := utf8.DecodeRuneInString(lx.src[lx.pos:])
				if !isWordPart(r) {
					break
				}
				lx.pos += size
			}
			t.kind = tkWord
		} else {
			lx.pos += size
			t.kind = tkPunct
		}
		t.val = lx.src[start:lx.pos]
	}
	t.end = lx.pos
	return t
}

func isWordStart(r rune, mysql bool) bool {
	return unicode.IsLetter(r) || r == '_' || r == '@' || r == '#' && !mysql // #temp tables in SQL Server
}

func isWordPart(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$' || r == '#' || r == '@'
}

// quoted reads a literal enclosed in q (a doubled q stands for itself). With
// backslash, \n, \t, \' and the like are escapes, as in MySQL.
func (lx *lexer) quoted(q byte, backslash bool) string {
	lx.pos++ // opening quote
	var b strings.Builder
	for lx.pos < len(lx.src) {
		c := lx.src[lx.pos]
		switch {
		case c == q && lx.at(lx.pos+1, q):
			b.WriteByte(q)
			lx.pos += 2
		case c == q:
			lx.pos++
			return b.String()
		case c == '\\' && backslash && lx.pos+1 < len(lx.src):
			n := lx.src[lx.pos+1]
			switch n {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'b':
				b.WriteByte('\b')
			case '0':
				b.WriteByte(0)
			case 'Z':
				b.WriteByte(0x1a)
			case '%', '_': // kept escaped in MySQL
				b.WriteByte('\\')
				b.WriteByte(n)
			default:
				b.WriteByte(n)
			}
			lx.pos += 2
		default:
			b.WriteByte(c)
			lx.pos++
		}
	}
	return b.String() // unterminated
}

// bracketed reads an SQL Server [identifier]; "]]" stands for "]".
func (lx *lexer) bracketed() string {
	lx.pos++
	var b strings.Builder
	for lx.pos < len(lx.src) {
		c := lx.src[lx.pos]
		if c == ']' {
			if lx.at(lx.pos+1, ']') {
				b.WriteByte(']')
				lx.pos += 2
				continue
			}
			lx.pos++
			break
		}
		b.WriteByte(c)
		lx.pos++
	}
	return b.String()
}

func (lx *lexer) number() {
	s := lx.src
	if s[lx.pos] == '0' && (lx.at(lx.pos+1, 'x') || lx.at(lx.pos+1, 'X')) {
		lx.pos += 2
		for lx.pos < len(s) && isHex(s[lx.pos]) {
			lx.pos++
		}
		return
	}
	for lx.pos < len(s) && isDigit(s[lx.pos]) {
		lx.pos++
	}
	if lx.at(lx.pos, '.') {
		lx.pos++
		for lx.pos < len(s) && isDigit(s[lx.pos]) {
			lx.pos++
		}
	}
	if lx.at(lx.pos, 'e') || lx.at(lx.pos, 'E') {
		p := lx.pos + 1
		if lx.at(p, '+') || lx.at(p, '-') {
			p++
		}
		if p < len(s) && isDigit(s[p]) {
			lx.pos = p
			for lx.pos < len(s) && isDigit(s[lx.pos]) {
				lx.pos++
			}
		}
	}
}

// Words that start a statement. A statement also ends before one of them at
// the start of a line, which splits scripts without semicolons (SQL Server).
var stmtStart = map[string]bool{
	"CREATE": true, "INSERT": true, "REPLACE": true, "COPY": true, "ALTER": true, "DROP": true,
	"SET": true, "USE": true, "GO": true, "COMMENT": true, "BEGIN": true, "COMMIT": true,
	"ROLLBACK": true, "LOCK": true, "UNLOCK": true, "PRAGMA": true, "DELETE": true, "UPDATE": true,
	"SELECT": true, "GRANT": true, "REVOKE": true, "TRUNCATE": true, "DECLARE": true, "PRINT": true,
	"EXEC": true, "EXECUTE": true, "IF": true, "START": true, "DELIMITER": true, "ANALYZE": true,
	"VACUUM": true, "REINDEX": true,
}

// statement reads the tokens of the next statement. terminated is true when
// it ended with a semicolon.
func (lx *lexer) statement() (toks []token, terminated bool) {
	depth := 0
	for {
		t := lx.peek()
		switch {
		case t.kind == tkEOF:
			return toks, false
		case t.isPunct(";"):
			lx.next()
			if len(toks) > 0 {
				return toks, true
			}
			continue // empty statement
		case depth == 0 && len(toks) > 0 && t.lineStart && t.kind == tkWord && stmtStart[strings.ToUpper(t.val)]:
			return toks, false
		case t.isPunct("("):
			depth++
		case t.isPunct(")"):
			depth = max(0, depth-1)
		}
		toks = append(toks, lx.next())
	}
}

// copyData reads the rows that follow COPY ... FROM stdin; up to the "\." line.
func (lx *lexer) copyData() []string {
	nl := strings.IndexByte(lx.src[lx.pos:], '\n')
	if nl < 0 {
		lx.pos = len(lx.src)
		return nil
	}
	lx.pos += nl + 1
	var lines []string
	for lx.pos < len(lx.src) {
		line := lx.src[lx.pos:]
		if i := strings.IndexByte(line, '\n'); i >= 0 {
			line = line[:i]
			lx.pos += i + 1
		} else {
			lx.pos = len(lx.src)
		}
		line = strings.TrimSuffix(line, "\r")
		if line == `\.` {
			break
		}
		lines = append(lines, line)
	}
	return lines
}

/* ---------------- cursor over the tokens of a statement ---------------- */

type cursor struct {
	toks []token
	i    int
	src  string
}

func (c *cursor) at(k int) token {
	if c.i+k < len(c.toks) {
		return c.toks[c.i+k]
	}
	return token{kind: tkEOF}
}

func (c *cursor) peek() token { return c.at(0) }
func (c *cursor) done() bool  { return c.i >= len(c.toks) }

func (c *cursor) next() token {
	t := c.peek()
	if c.i < len(c.toks) {
		c.i++
	}
	return t
}

// accept consumes the words if they come next, in this order.
func (c *cursor) accept(words ...string) bool {
	for k, w := range words {
		if !c.at(k).is(w) {
			return false
		}
	}
	c.i += len(words)
	return true
}

func (c *cursor) acceptPunct(p string) bool {
	if c.peek().isPunct(p) {
		c.i++
		return true
	}
	return false
}

// group consumes a parenthesized group and returns the tokens inside it.
func (c *cursor) group() ([]token, bool) {
	if !c.peek().isPunct("(") {
		return nil, false
	}
	c.i++
	start, depth := c.i, 1
	for !c.done() {
		t := c.next()
		switch {
		case t.isPunct("("):
			depth++
		case t.isPunct(")"):
			if depth--; depth == 0 {
				return c.toks[start : c.i-1], true
			}
		}
	}
	return c.toks[start:], true
}

// name reads an identifier: a word or a quoted identifier.
func (c *cursor) name() (string, bool) {
	if t := c.peek(); t.isName() {
		c.i++
		return t.val, true
	}
	return "", false
}

// qname reads a dotted name such as schema.table.
func (c *cursor) qname() []string {
	var parts []string
	for {
		n, ok := c.name()
		if !ok {
			break
		}
		parts = append(parts, n)
		if !c.acceptPunct(".") {
			break
		}
	}
	return parts
}

// raw returns the source text of the tokens.
func (c *cursor) raw(toks []token) string {
	if len(toks) == 0 {
		return ""
	}
	return strings.TrimSpace(c.src[toks[0].start:toks[len(toks)-1].end])
}

// split splits tokens on commas outside parentheses.
func split(toks []token) [][]token {
	var (
		out   [][]token
		depth int
		start int
	)
	for i, t := range toks {
		switch {
		case t.isPunct("("):
			depth++
		case t.isPunct(")"):
			depth--
		case t.isPunct(",") && depth == 0:
			out = append(out, toks[start:i])
			start = i + 1
		}
	}
	if start < len(toks) || len(out) > 0 {
		out = append(out, toks[start:])
	}
	return out
}

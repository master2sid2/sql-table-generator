package table

import (
	"encoding/csv"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Input formats.
const (
	FormatAuto     = "auto"
	FormatCSV      = "csv"
	FormatTSV      = "tsv"
	FormatMySQL    = "mysql"    // +---+ boxed output (also markdown tables)
	FormatPipe     = "pipe"     // "a | b | c" rows without a frame (psql, pieces of mysql output)
	FormatVertical = "vertical" // mysql \G output
)

// ErrEmpty is returned when there is nothing to parse.
var ErrEmpty = errors.New("nothing to parse")

var (
	borderRe   = regexp.MustCompile(`^\+(-+\+)+$`)
	sepRe      = regexp.MustCompile(`^[|+:\s-]*--[|+:\s-]*$`) // |---|---|, ----+----
	verticalRe = regexp.MustCompile(`^\*+ *\d+\. row *\*+$`)
	footerRe   = regexp.MustCompile(`^(\d+ rows? in set|Empty set|\(\d+ rows?\)$)`)
	psqlSepRe  = regexp.MustCompile(`(?m)^\s*-+(\+-+)+\s*$`) // the line under psql column names
)

// Result is a parsed table together with what was learned about the input.
type Result struct {
	Table
	Format string
	// HasHeader reports whether Headers came from the data. When false the
	// headers are generated (col1, col2, ...) and every input line is in Rows.
	HasHeader bool
}

// Parse converts text in the given format (or auto-detected) into a Table.
// header says whether the first CSV/TSV record holds column names; the
// mysql formats carry their own header. The detected format is returned.
func Parse(text, format string, header bool) (Table, string, error) {
	res, err := ParseText(text, format, header)
	return res.Table, res.Format, err
}

// ParseText is Parse that also reports whether the header came from the data.
func ParseText(text, format string, header bool) (Result, error) {
	text = strings.TrimPrefix(text, "\uFEFF")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		return Result{Format: format}, ErrEmpty
	}
	if format == "" || format == FormatAuto {
		format = Detect(text)
	}
	var (
		t   Table
		err error
	)
	hasHeader := header
	switch format {
	case FormatMySQL, FormatPipe:
		t, hasHeader, err = parseBoxed(text)
	case FormatVertical:
		t, err = parseVertical(text)
		hasHeader = true
	case FormatTSV:
		t, err = parseDelimited(text, '\t', header)
	case FormatCSV:
		t, err = parseDelimited(text, detectDelimiter(text), header)
	default:
		return Result{Format: format}, fmt.Errorf("unknown format %q", format)
	}
	if err != nil {
		return Result{Format: format}, err
	}
	return Result{Table: Normalize(t, false), Format: format, HasHeader: hasHeader}, nil
}

// Detect guesses the format of text.
func Detect(text string) string {
	// psql output may start with a title line, such as \d's Table "public.users".
	if head, _, _ := strings.Cut(text, "\n\n"); psqlSepRe.MatchString(head) {
		return FormatPipe
	}
	for line := range strings.SplitSeq(text, "\n") {
		l := strings.TrimSpace(line)
		if l == "" || strings.HasPrefix(l, "mysql>") {
			continue
		}
		switch {
		case verticalRe.MatchString(l):
			return FormatVertical
		case strings.HasPrefix(l, "+-") || strings.HasPrefix(l, "|"):
			return FormatMySQL
		case strings.Contains(line, "\t"):
			return FormatTSV
		case looksPiped(l):
			return FormatPipe
		}
		return FormatCSV
	}
	return FormatCSV
}

// looksPiped reports whether '|' is the most frequent separator in the line.
func looksPiped(line string) bool {
	pipes := strings.Count(line, "|")
	return pipes > 0 && pipes >= strings.Count(line, ",") && pipes >= strings.Count(line, ";")
}

func detectDelimiter(text string) rune {
	first, _, _ := strings.Cut(text, "\n")
	counts := map[rune]int{}
	inQuote := false
	for _, r := range first {
		switch {
		case r == '"':
			inQuote = !inQuote
		case !inQuote && (r == ',' || r == ';' || r == '\t' || r == '|'):
			counts[r]++
		}
	}
	best, n := ',', 0
	for _, d := range []rune{',', ';', '\t', '|'} {
		if counts[d] > n {
			best, n = d, counts[d]
		}
	}
	return best
}

func parseDelimited(text string, delim rune, header bool) (Table, error) {
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = delim
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	if err != nil {
		return Table{}, fmt.Errorf("parse CSV: %w", err)
	}
	if len(recs) == 0 {
		return Table{}, ErrEmpty
	}
	if header {
		return Table{Headers: recs[0], Rows: recs[1:]}, nil
	}
	return Table{Headers: genHeaders(recs), Rows: recs}, nil
}

func genHeaders(rows [][]string) []string {
	n := 0
	for _, r := range rows {
		n = max(n, len(r))
	}
	h := make([]string, n)
	for i := range h {
		h[i] = fmt.Sprintf("col%d", i+1)
	}
	return h
}

// parseBoxed reads the mysql client table output. Column boundaries are taken
// from the +---+ border so cell values may contain '|'. Markdown tables
// (| a | b |, |---|---|), psql output (a | b, ---+---) and bare "a | b" rows
// are understood too. The header is the first row only when a separator line
// follows it.
func parseBoxed(text string) (Table, bool, error) {
	var (
		bounds     []int // display positions of '+' in the border line
		pipeRows   [][]string
		headerSeen bool // a separator line came right after the first pipe row
	)
	for raw := range strings.SplitSeq(text, "\n") {
		l := strings.TrimSpace(raw)
		switch {
		case l == "", strings.HasPrefix(l, "mysql>"), footerRe.MatchString(l):
			continue
		case borderRe.MatchString(l):
			if bounds == nil {
				for i, ch := range l {
					if ch == '+' {
						bounds = append(bounds, i)
					}
				}
			}
			if len(pipeRows) == 1 {
				headerSeen = true
			}
		case sepRe.MatchString(l):
			if len(pipeRows) == 1 {
				headerSeen = true
			}
		case strings.Contains(l, "|"):
			pipeRows = append(pipeRows, splitBoxed(l, bounds))
		}
	}
	if len(pipeRows) == 0 {
		return Table{}, false, ErrEmpty
	}
	if headerSeen {
		return Table{Headers: pipeRows[0], Rows: pipeRows[1:]}, true, nil
	}
	return Table{Headers: genHeaders(pipeRows), Rows: pipeRows}, false, nil
}

func splitBoxed(line string, bounds []int) []string {
	if len(bounds) >= 2 && DisplayWidth(line) == bounds[len(bounds)-1]+1 {
		if cells, ok := splitByBounds(line, bounds); ok {
			return cells
		}
	}
	return splitPipes(line)
}

func splitByBounds(line string, bounds []int) ([]string, bool) {
	cells := make([]strings.Builder, len(bounds)-1)
	pos, b := 0, 0
	for _, r := range line {
		if b < len(bounds) && pos == bounds[b] {
			if r != '|' {
				return nil, false
			}
			b++
		} else if b > 0 && b < len(bounds) {
			cells[b-1].WriteRune(r)
		}
		pos += runeWidth(r)
	}
	if b != len(bounds) {
		return nil, false
	}
	out := make([]string, len(cells))
	for i := range cells {
		out[i] = trimCell(cells[i].String())
	}
	return out, true
}

func splitPipes(line string) []string {
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	for i, p := range parts {
		parts[i] = trimCell(p)
	}
	return parts
}

func trimCell(s string) string { return strings.Trim(s, " ") }

// parseVertical reads mysql \G output:
//
//	*************************** 1. row ***************************
//	  id: 1
//	name: Alice
func parseVertical(text string) (Table, error) {
	var (
		t     Table
		index = map[string]int{}
		cur   []string
		in    bool
	)
	flush := func() {
		if in {
			t.Rows = append(t.Rows, cur)
		}
		cur, in = nil, true
	}
	for raw := range strings.SplitSeq(text, "\n") {
		l := strings.TrimSpace(raw)
		switch {
		case l == "", strings.HasPrefix(l, "mysql>"), footerRe.MatchString(l):
			continue
		case verticalRe.MatchString(l):
			flush()
			continue
		}
		if !in {
			continue
		}
		key, val, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimPrefix(val, " ")
		c, known := index[key]
		if !known {
			c = len(t.Headers)
			index[key] = c
			t.Headers = append(t.Headers, key)
		}
		for len(cur) <= c {
			cur = append(cur, "")
		}
		cur[c] = val
	}
	if in {
		t.Rows = append(t.Rows, cur)
	}
	if len(t.Headers) == 0 {
		return Table{}, ErrEmpty
	}
	return t, nil
}

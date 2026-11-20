package table

import (
	"fmt"
	"regexp"
	"strings"
)

// Table is a header row plus data rows. Rows may be ragged; they are padded on render.
type Table struct {
	Headers []string   `json:"headers"`
	Rows    [][]string `json:"rows"`
}

// Align modes.
const (
	AlignAuto  = "auto" // numbers right, text left (as the mysql client does)
	AlignLeft  = "left"
	AlignRight = "right"
)

// Options control how a table is rendered.
type Options struct {
	Align       string `json:"align"`
	EmptyAsNull bool   `json:"emptyAsNull"` // render empty cells as NULL
	Footer      bool   `json:"footer"`      // append "N rows in set (0.00 sec)"
	SkipEmpty   bool   `json:"skipEmpty"`   // drop fully empty rows and columns
}

var numberRe = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)

var cellEscaper = strings.NewReplacer("\r\n", `\n`, "\n", `\n`, "\r", `\r`, "\t", `\t`)

// Normalize pads all rows to the same width, escapes control characters and,
// if requested, drops fully empty rows and columns.
func Normalize(t Table, skipEmpty bool) Table {
	ncols := len(t.Headers)
	for _, r := range t.Rows {
		ncols = max(ncols, len(r))
	}
	fix := func(src []string) []string {
		out := make([]string, ncols)
		for i := range out {
			if i < len(src) {
				out[i] = cellEscaper.Replace(src[i])
			}
		}
		return out
	}
	res := Table{Headers: fix(t.Headers), Rows: make([][]string, 0, len(t.Rows))}
	for _, r := range t.Rows {
		row := fix(r)
		if skipEmpty && isBlank(row) {
			continue
		}
		res.Rows = append(res.Rows, row)
	}
	if !skipEmpty {
		return res
	}
	keep := make([]int, 0, ncols)
	for c := range ncols {
		if strings.TrimSpace(res.Headers[c]) != "" {
			keep = append(keep, c)
			continue
		}
		for _, r := range res.Rows {
			if strings.TrimSpace(r[c]) != "" {
				keep = append(keep, c)
				break
			}
		}
	}
	if len(keep) == ncols {
		return res
	}
	pick := func(src []string) []string {
		out := make([]string, len(keep))
		for i, c := range keep {
			out[i] = src[c]
		}
		return out
	}
	res.Headers = pick(res.Headers)
	for i, r := range res.Rows {
		res.Rows[i] = pick(r)
	}
	return res
}

func isBlank(row []string) bool {
	for _, v := range row {
		if strings.TrimSpace(v) != "" {
			return false
		}
	}
	return true
}

// Render formats the table the way the mysql command-line client does:
//
//	+----+-------+
//	| id | name  |
//	+----+-------+
//	|  1 | Alice |
//	+----+-------+
func Render(t Table, opt Options) string {
	t = Normalize(t, opt.SkipEmpty)
	ncols := len(t.Headers)
	if ncols == 0 {
		return ""
	}
	if opt.EmptyAsNull {
		for _, r := range t.Rows {
			for i, v := range r {
				if v == "" {
					r[i] = "NULL"
				}
			}
		}
	}

	widths := make([]int, ncols)
	for c, h := range t.Headers {
		widths[c] = DisplayWidth(h)
	}
	for _, r := range t.Rows {
		for c, v := range r {
			widths[c] = max(widths[c], DisplayWidth(v))
		}
	}

	right := make([]bool, ncols)
	for c := range ncols {
		switch opt.Align {
		case AlignRight:
			right[c] = true
		case AlignLeft:
		default:
			right[c] = isNumericColumn(t.Rows, c)
		}
	}

	var b strings.Builder
	border := func() {
		b.WriteByte('+')
		for _, w := range widths {
			b.WriteString(strings.Repeat("-", w+2))
			b.WriteByte('+')
		}
		b.WriteByte('\n')
	}
	line := func(cells []string, alignRight []bool) {
		b.WriteByte('|')
		for c, v := range cells {
			pad := strings.Repeat(" ", widths[c]-DisplayWidth(v))
			b.WriteByte(' ')
			if alignRight != nil && alignRight[c] {
				b.WriteString(pad)
				b.WriteString(v)
			} else {
				b.WriteString(v)
				b.WriteString(pad)
			}
			b.WriteString(" |")
		}
		b.WriteByte('\n')
	}

	border()
	line(t.Headers, nil) // headers are always left-aligned
	border()
	for _, r := range t.Rows {
		line(r, right)
	}
	if len(t.Rows) > 0 {
		border()
	}

	if opt.Footer {
		switch n := len(t.Rows); n {
		case 0:
			b.WriteString("Empty set (0.00 sec)\n")
		case 1:
			b.WriteString("1 row in set (0.00 sec)\n")
		default:
			fmt.Fprintf(&b, "%d rows in set (0.00 sec)\n", n)
		}
	}
	return b.String()
}

func isNumericColumn(rows [][]string, c int) bool {
	seen := false
	for _, r := range rows {
		v := strings.TrimSpace(r[c])
		if v == "" || v == "NULL" {
			continue
		}
		if !numberRe.MatchString(v) {
			return false
		}
		seen = true
	}
	return seen
}

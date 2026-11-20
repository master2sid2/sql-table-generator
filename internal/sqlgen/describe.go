package sqlgen

import (
	"regexp"
	"slices"
	"strings"

	"github.com/master2sid2/sql-table-generator/internal/table"
)

// Structure describes the columns the way MySQL DESCRIBE does, with the types
// of the given dialect. Generate must have filled in SQLType.
func Structure(p *Prepared, d string) table.Table {
	d = NormDialect(d)
	cols, _ := p.Kept()
	withComments := false
	for _, e := range cols {
		withComments = withComments || e.Comment != ""
	}
	t := table.Table{Headers: []string{"Field", "Type", "Null", "Key", "Default", "Extra"}}
	if withComments {
		t.Headers = append(t.Headers, "Comment")
	}
	extra := map[string]string{MySQL: "auto_increment", Postgres: "identity", SQLite: "autoincrement", MSSQL: "identity"}[d]
	for _, e := range cols {
		null, key, def, ext := "YES", "", "NULL", ""
		if e.NotNull || e.PrimaryKey || e.AutoIncrement {
			null = "NO"
		}
		switch {
		case e.PrimaryKey:
			key = "PRI"
		case e.Unique:
			key = "UNI"
		}
		if e.AutoIncrement {
			ext = extra
		} else if v := strings.TrimSpace(e.Default); v != "" {
			def = v
		}
		row := []string{e.Name, e.SQLType, null, key, def, ext}
		if withComments {
			row = append(row, oneLine(e.Comment))
		}
		t.Rows = append(t.Rows, row)
	}
	return t
}

var psqlIndexRe = regexp.MustCompile(`(?m)^\s+"[^"]*" (PRIMARY KEY|UNIQUE CONSTRAINT|UNIQUE), \w+ \(([^)]*)\)`)

// FromDescribe recognizes the output of MySQL DESCRIBE (and of Structure) or
// of psql \d and turns it into column definitions. raw is the text the table
// was parsed from: the keys of psql \d are in its "Indexes:" part.
func FromDescribe(t table.Table, raw string) (names []string, cols []Column, ok bool) {
	h := make([]string, len(t.Headers))
	for i, v := range t.Headers {
		h[i] = strings.ToLower(strings.TrimSpace(v))
	}
	find := func(name string) int {
		for i, v := range h {
			if v == name {
				return i
			}
		}
		return -1
	}
	cell := func(r []string, i int) string {
		if i < 0 || i >= len(r) {
			return ""
		}
		return strings.TrimSpace(r[i])
	}

	mysql := len(h) >= 6 && strings.Join(h[:6], ",") == "field,type,null,key,default,extra"
	psql := len(h) >= 3 && h[0] == "column" && h[1] == "type" && find("nullable") >= 0
	if !mysql && !psql {
		return nil, nil, false
	}
	nullI, defI, commentI := find("null"), find("default"), find("comment")
	if psql {
		nullI = find("nullable")
	}
	for _, r := range t.Rows {
		name := cell(r, 0)
		if name == "" {
			continue
		}
		col := typeFromString(cell(r, 1))
		def := cell(r, defI)
		low := strings.ToLower(def)
		if mysql {
			col.NotNull = strings.EqualFold(cell(r, nullI), "NO")
			switch strings.ToUpper(cell(r, 3)) {
			case "PRI":
				col.PrimaryKey = true
			case "UNI":
				col.Unique = true
			}
			ext := strings.ToLower(cell(r, 5))
			if strings.Contains(ext, "auto_increment") || strings.Contains(ext, "autoincrement") || strings.Contains(ext, "identity") {
				col.AutoIncrement = true
			}
		} else {
			col.NotNull = strings.EqualFold(cell(r, nullI), "not null")
		}
		switch {
		case strings.Contains(low, "nextval(") || strings.Contains(low, "as identity"):
			col.AutoIncrement = true
		case def != "" && !strings.EqualFold(def, "NULL"):
			col.Default = stripCast(def)
		}
		col.Comment = cell(r, commentI)
		names = append(names, name)
		cols = append(cols, col)
	}
	if psql {
		for _, m := range psqlIndexRe.FindAllStringSubmatch(raw, -1) {
			keys := strings.Split(m[2], ",")
			if m[1] != "PRIMARY KEY" && len(keys) > 1 {
				continue // a unique pair is not a unique column
			}
			for _, k := range keys {
				i := slices.Index(names, strings.Trim(strings.TrimSpace(k), `"`))
				switch {
				case i < 0:
				case m[1] == "PRIMARY KEY":
					cols[i].PrimaryKey = true
				default:
					cols[i].Unique = true
				}
			}
		}
	}
	return names, cols, len(names) > 0
}

// stripCast turns psql defaults such as 'abc'::character varying into abc.
func stripCast(def string) string {
	if !strings.HasPrefix(def, "'") {
		return def
	}
	if i := strings.LastIndex(def, "'::"); i > 0 {
		def = def[:i+1]
	}
	if len(def) >= 2 && strings.HasSuffix(def, "'") {
		return strings.ReplaceAll(def[1:len(def)-1], "''", "'")
	}
	return def
}

// typeFromString parses a type such as "varchar(255)" or "int unsigned".
func typeFromString(s string) Column {
	lx := &lexer{src: s}
	var toks []token
	for t := lx.next(); t.kind != tkEOF; t = lx.next() {
		toks = append(toks, t)
	}
	c := &cursor{toks: toks, src: s}
	ti := parseType(c)
	return Column{Type: ti.typ, Length: ti.length, Scale: ti.scale, AutoIncrement: ti.ai}
}

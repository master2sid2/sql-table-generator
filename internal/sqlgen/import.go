package sqlgen

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// ErrNoTable is returned by Import when the script has no table in it.
var ErrNoTable = errors.New("no CREATE TABLE, INSERT or COPY statement found")

// Imported is a table read from an SQL script.
type Imported struct {
	Table   string     `json:"tableName"`
	Headers []string   `json:"headers"`
	Rows    [][]string `json:"rows"`
	Columns []Column   `json:"columns"`
	// ForeignKeys are the multi-column foreign keys; single-column ones are in Columns.
	ForeignKeys []ForeignKey `json:"foreignKeys,omitempty"`
	Dialect     string       `json:"dialect,omitempty"` // the dialect the script looks like
	Notes       []string     `json:"notes,omitempty"`
}

var (
	// comments, blank lines and empty statements ("/*!40101 SET ... */;" in mysqldump) before the first statement
	sqlStartRe = regexp.MustCompile(`(?is)^(?:\s|(?:--|#)[^\n]*(?:\n|$)|/\*.*?\*/|;)*` +
		`(create|insert|replace|copy|alter|drop|set|use|begin|start|lock|pragma|go|comment|if)\b`)
	sqlBodyRe = regexp.MustCompile(`(?is)\b(?:create\s+(?:\w+\s+){0,3}table\s+[^(;]{1,300}\(|` +
		`(?:insert|replace)\s+[^;]+?\bvalues?\s*\(|copy\s+[^;]+?\bfrom\s+stdin\b)`)
)

// LooksLikeSQL reports whether text is an SQL script with a table in it.
func LooksLikeSQL(text string) bool {
	return sqlStartRe.MatchString(text) && sqlBodyRe.MatchString(text)
}

// detectDialect guesses which database the script was written for.
func detectDialect(src string) string {
	s := strings.ToUpper(src)
	best, bestScore := "", 0
	for _, d := range []struct {
		name    string
		markers []string
	}{
		{MySQL, []string{"`", "ENGINE=", "AUTO_INCREMENT", "/*!", "LOCK TABLES", "UNSIGNED", "CHARSET="}},
		{Postgres, []string{"::", "SERIAL", "NEXTVAL(", "FROM STDIN", "PUBLIC.", "AS IDENTITY", "JSONB", "BYTEA", "PG_CATALOG", "OWNER TO"}},
		{SQLite, []string{"AUTOINCREMENT", "WITHOUT ROWID", "PRAGMA", "SQLITE_"}},
		{MSSQL, []string{"[DBO]", "IDENTITY(", "NVARCHAR", "\nGO", "UNIQUEIDENTIFIER", "IDENTITY_INSERT", "DATETIME2", "CLUSTERED"}},
	} {
		score := 0
		for _, m := range d.markers {
			if strings.Contains(s, m) {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = d.name, score
		}
	}
	return best
}

type tableDef struct {
	name     string
	explicit bool // defined by CREATE TABLE
	names    []string
	cols     []Column
	rows     [][]string
	fks      []ForeignKey // multi-column foreign keys
	notes    []string
}

func (td *tableDef) note(format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	if !slices.Contains(td.notes, s) {
		td.notes = append(td.notes, s)
	}
}

func (td *tableDef) find(name string) int {
	for i, n := range td.names {
		if strings.EqualFold(n, name) {
			return i
		}
	}
	return -1
}

// column returns the index of the column, adding it if needed.
func (td *tableDef) column(name string) int {
	if i := td.find(name); i >= 0 {
		return i
	}
	td.names = append(td.names, name)
	td.cols = append(td.cols, Column{})
	return len(td.names) - 1
}

type importer struct {
	tables []*tableDef
}

// lookup finds a table by name; the schema is ignored.
func (im *importer) lookup(parts []string) *tableDef {
	if len(parts) == 0 {
		return nil
	}
	name := parts[len(parts)-1]
	for _, t := range im.tables {
		if strings.EqualFold(t.name, name) {
			return t
		}
	}
	return nil
}

func (im *importer) table(parts []string, create bool) *tableDef {
	if t := im.lookup(parts); t != nil {
		t.explicit = t.explicit || create
		return t
	}
	t := &tableDef{name: parts[len(parts)-1], explicit: create}
	im.tables = append(im.tables, t)
	return t
}

// Import reads CREATE TABLE, INSERT and COPY statements (and the ALTER TABLE
// and COMMENT ON statements dumps put keys and comments in) into a table.
// When the script has several tables, the first one is taken.
func Import(src string) (*Imported, error) {
	src = strings.TrimPrefix(src, "\uFEFF")
	dialect := detectDialect(src)
	lx := &lexer{src: src, mysql: dialect == MySQL}
	im := &importer{}
	for {
		toks, terminated := lx.statement()
		if len(toks) == 0 {
			break
		}
		c := &cursor{toks: toks, src: src}
		switch t := c.peek(); {
		case t.is("CREATE"):
			im.create(c)
		case t.is("INSERT", "REPLACE"):
			im.insert(c)
		case t.is("COPY"):
			if td, idx := im.copyHeader(c); td != nil && terminated {
				im.copyRows(td, idx, lx.copyData())
			}
		case t.is("ALTER"):
			im.alter(c)
		case t.is("COMMENT"):
			im.comment(c)
		}
	}

	var pick *tableDef
	var names []string
	for _, t := range im.tables {
		if strings.HasPrefix(strings.ToLower(t.name), "sqlite_") {
			continue // SQLite's own tables
		}
		if t.explicit || len(t.rows) > 0 {
			names = append(names, t.name)
			if pick == nil || !pick.explicit && t.explicit {
				pick = t
			}
		}
	}
	if pick == nil || len(pick.names) == 0 {
		return nil, ErrNoTable
	}
	notes := pick.notes
	if len(names) > 1 {
		notes = append([]string{fmt.Sprintf("the script has %d tables (%s): loaded %s",
			len(names), strings.Join(names, ", "), pick.name)}, notes...)
	}
	res := &Imported{
		Table:       pick.name,
		Headers:     pick.names,
		Columns:     pick.cols,
		ForeignKeys: pick.fks,
		Rows:        make([][]string, len(pick.rows)),
		Dialect:     dialect,
		Notes:       notes,
	}
	for i, r := range pick.rows {
		row := make([]string, len(pick.names))
		copy(row, r)
		res.Rows[i] = row
	}
	return res, nil
}

/* ---------------- CREATE TABLE ---------------- */

func (im *importer) create(c *cursor) {
	c.next()
	for c.peek().is("OR", "REPLACE", "TEMPORARY", "TEMP", "GLOBAL", "LOCAL", "UNLOGGED") {
		c.next()
	}
	if !c.accept("TABLE") {
		return
	}
	c.accept("IF", "NOT", "EXISTS")
	parts := c.qname()
	if len(parts) == 0 {
		return
	}
	defs, ok := c.group()
	if !ok {
		return // CREATE TABLE ... AS SELECT, LIKE
	}
	td := im.table(parts, true)
	for _, def := range split(defs) {
		dc := &cursor{toks: def, src: c.src}
		if isConstraint(dc.peek()) {
			im.constraint(td, dc)
		} else {
			im.columnDef(td, dc)
		}
	}
}

func isConstraint(t token) bool {
	return t.is("CONSTRAINT", "PRIMARY", "UNIQUE", "KEY", "INDEX", "FOREIGN", "CHECK",
		"FULLTEXT", "SPATIAL", "EXCLUDE", "PERIOD", "LIKE")
}

// columnDef reads "name type modifiers..." and adds the column, or replaces
// its definition when it already exists (ALTER TABLE ... MODIFY).
func (im *importer) columnDef(td *tableDef, c *cursor) {
	name, ok := c.name()
	if !ok {
		return
	}
	ti := parseType(c)
	if ti.note != "" {
		td.note("%s: %s", name, ti.note)
	}
	col := Column{Type: ti.typ, Length: ti.length, Scale: ti.scale, AutoIncrement: ti.ai}
	modifiers(td, c, &col)
	i := td.column(name)
	// MODIFY redefines the column but keeps its keys, which are indexes in MySQL.
	col.PrimaryKey = col.PrimaryKey || td.cols[i].PrimaryKey
	col.Unique = col.Unique || td.cols[i].Unique
	td.cols[i] = col
}

func modifiers(td *tableDef, c *cursor, col *Column) {
	constraint := "" // the name of CONSTRAINT name REFERENCES ...
	for !c.done() {
		t := c.next()
		if t.kind != tkWord {
			continue // a group after an unknown word is skipped as its tokens
		}
		switch strings.ToUpper(t.val) {
		case "NOT":
			if c.accept("NULL") {
				col.NotNull = true
			}
		case "DEFAULT":
			def, ai := defaultExpr(c)
			col.Default = def
			col.AutoIncrement = col.AutoIncrement || ai
		case "PRIMARY":
			c.accept("KEY")
			col.PrimaryKey = true
		case "UNIQUE":
			_ = c.accept("KEY") || c.accept("INDEX")
			col.Unique = true
		case "AUTO_INCREMENT", "AUTOINCREMENT":
			col.AutoIncrement = true
		case "IDENTITY":
			col.AutoIncrement = true
			c.group()
		case "GENERATED":
			c.accept("ALWAYS")
			c.accept("BY", "DEFAULT")
			c.accept("AS")
			if c.accept("IDENTITY") {
				col.AutoIncrement = true
			} else {
				td.note("computed columns are imported as plain columns")
			}
			c.group()
		case "AS": // SQL Server computed column
			c.group()
			td.note("computed columns are imported as plain columns")
		case "COMMENT":
			if s := c.peek(); s.kind == tkString {
				col.Comment = s.val
				c.next()
			}
		case "COLLATE", "CHARSET":
			c.next()
		case "CHARACTER":
			c.accept("SET")
			c.next()
		case "REFERENCES":
			table, cols := reference(c)
			if table != "" {
				col.Ref = Reference{Name: constraint, Table: table, Column: first(cols)}
				referenceActions(c, &col.Ref.OnDelete, &col.Ref.OnUpdate)
			}
		case "CHECK":
			c.group()
		case "ON": // ON UPDATE CURRENT_TIMESTAMP, ON DELETE CASCADE, ON CONFLICT ...
			c.next()
		case "CONSTRAINT":
			constraint, _ = c.name()
			continue
		}
		constraint = "" // a name belongs to the constraint right after it
	}
}

// constraint reads a table constraint: PRIMARY KEY (a, b), UNIQUE (a), and
// SQL Server's DEFAULT x FOR col. Indexes, foreign keys and checks are skipped.
func (im *importer) constraint(td *tableDef, c *cursor) {
	name := ""
	if c.accept("CONSTRAINT") {
		if t := c.peek(); t.kind == tkIdent || t.kind == tkWord && !t.is("PRIMARY", "UNIQUE", "FOREIGN", "CHECK", "DEFAULT", "EXCLUDE") {
			name = c.next().val
		}
	}
	switch {
	case c.accept("PRIMARY"):
		for _, n := range nameList(c) {
			if i := td.find(n); i >= 0 {
				td.cols[i].PrimaryKey = true
			}
		}
	case c.accept("UNIQUE"):
		if names := nameList(c); len(names) == 1 {
			if i := td.find(names[0]); i >= 0 {
				td.cols[i].Unique = true
			}
		}
	case c.accept("DEFAULT"):
		def, ai := defaultExpr(c)
		if c.accept("FOR") {
			if n, ok := c.name(); ok {
				if i := td.find(n); i >= 0 {
					td.cols[i].Default = def
					td.cols[i].AutoIncrement = td.cols[i].AutoIncrement || ai
				}
			}
		}
	case c.accept("FOREIGN"):
		foreignKey(td, c, name)
	}
}

// foreignKey reads the rest of FOREIGN KEY [index] (cols) REFERENCES table (cols) actions.
func foreignKey(td *tableDef, c *cursor, name string) {
	cols := nameList(c)
	if !c.accept("REFERENCES") {
		return
	}
	table, refs := reference(c)
	if table == "" || len(cols) == 0 {
		return
	}
	fk := ForeignKey{Name: name, Columns: cols, Table: table, RefColumns: refs}
	referenceActions(c, &fk.OnDelete, &fk.OnUpdate)
	if len(cols) > 1 {
		td.fks = append(td.fks, fk)
		return
	}
	if i := td.find(cols[0]); i >= 0 {
		td.cols[i].Ref = Reference{Name: name, Table: table, Column: first(refs), OnDelete: fk.OnDelete, OnUpdate: fk.OnUpdate}
	}
}

// reference reads "table (columns)" after REFERENCES. The schema is dropped,
// as it is for the imported table itself.
func reference(c *cursor) (string, []string) {
	parts := c.qname()
	if len(parts) == 0 {
		return "", nil
	}
	var cols []string
	if c.peek().isPunct("(") {
		cols = nameList(c)
	}
	return parts[len(parts)-1], cols
}

// referenceActions reads MATCH ..., ON DELETE action and ON UPDATE action.
// Anything else, such as MySQL's ON UPDATE CURRENT_TIMESTAMP, is left alone.
func referenceActions(c *cursor, onDelete, onUpdate *string) {
	for {
		if c.accept("MATCH") {
			c.next()
			continue
		}
		save := c.i
		if !c.accept("ON") {
			return
		}
		which := c.next()
		var act string
		switch {
		case c.accept("NO", "ACTION"):
			act = "NO ACTION"
		case c.accept("SET", "NULL"):
			act = "SET NULL"
		case c.accept("SET", "DEFAULT"):
			act = "SET DEFAULT"
		case c.peek().is("CASCADE", "RESTRICT"):
			act = strings.ToUpper(c.next().val)
		}
		switch {
		case act != "" && which.is("DELETE"):
			*onDelete = act
		case act != "" && which.is("UPDATE"):
			*onUpdate = act
		default:
			c.i = save
			return
		}
	}
}

func first(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}

// nameList reads the column list of a key: skips to "(" and returns the names.
func nameList(c *cursor) []string {
	for !c.done() && !c.peek().isPunct("(") {
		c.next()
	}
	g, _ := c.group()
	var names []string
	for _, item := range split(g) {
		if len(item) > 0 && item[0].isName() {
			names = append(names, item[0].val)
		}
	}
	return names
}

/* ---------------- types ---------------- */

type typeInfo struct {
	typ           string
	length, scale int
	ai            bool // serial types
	note          string
}

// Words that continue a type name: DOUBLE PRECISION, CHARACTER VARYING,
// TIMESTAMP WITH TIME ZONE, INT UNSIGNED ...
var typeCont = map[string]bool{
	"PRECISION": true, "VARYING": true, "UNSIGNED": true, "SIGNED": true, "ZEROFILL": true,
	"WITH": true, "WITHOUT": true, "TIME": true, "ZONE": true, "LOCAL": true,
}

// Words that start the modifiers of a column, so the column has no type (SQLite).
var notType = map[string]bool{
	"PRIMARY": true, "NOT": true, "NULL": true, "DEFAULT": true, "UNIQUE": true, "REFERENCES": true,
	"CHECK": true, "CONSTRAINT": true, "COLLATE": true, "GENERATED": true, "AS": true,
}

func parseType(c *cursor) typeInfo {
	first := c.peek()
	if !first.isName() || first.kind == tkWord && notType[strings.ToUpper(first.val)] {
		return typeInfo{}
	}
	c.next()
	words := []string{strings.ToLower(first.val)}
	var args []string
	array := false
	for {
		t := c.peek()
		switch {
		case t.isPunct("("):
			g, _ := c.group()
			args = args[:0]
			for _, item := range split(g) {
				args = append(args, strings.ToLower(itemText(c, item)))
			}
			continue
		case t.isPunct("["):
			c.next()
			c.acceptPunct("]")
			array = true
			continue
		case t.kind == tkWord && (typeCont[strings.ToUpper(t.val)] ||
			words[0] == "national" && t.is("CHARACTER", "CHAR", "VARCHAR")):
			words = append(words, strings.ToLower(t.val))
			c.next()
			continue
		}
		break
	}
	ti := mapType(words, args)
	if array {
		ti = typeInfo{typ: TypeText, note: "array type imported as TEXT"}
	}
	return ti
}

func itemText(c *cursor, item []token) string {
	if len(item) == 1 && item[0].kind == tkString {
		return item[0].val
	}
	return c.raw(item)
}

func mapType(words, args []string) typeInfo {
	arg := func(i int) int {
		if i < len(args) {
			if n, err := strconv.Atoi(strings.TrimSpace(args[i])); err == nil {
				return n
			}
		}
		return 0
	}
	full := strings.Join(words, " ")
	has := func(w string) bool { return strings.Contains(" "+full+" ", " "+w+" ") }
	ti := typeInfo{}
	switch words[0] {
	case "int", "integer", "int4", "mediumint", "int3", "middleint":
		ti.typ = TypeInt
	case "tinyint", "int1":
		ti.typ = TypeSmallInt
		if arg(0) == 1 {
			ti.typ = TypeBool // MySQL's BOOLEAN
		}
	case "smallint", "int2", "year":
		ti.typ = TypeSmallInt
	case "bigint", "int8":
		ti.typ = TypeBigInt
	case "serial", "serial4":
		ti.typ, ti.ai = TypeInt, true
	case "bigserial", "serial8":
		ti.typ, ti.ai = TypeBigInt, true
	case "smallserial", "serial2":
		ti.typ, ti.ai = TypeSmallInt, true
	case "decimal", "numeric", "dec", "fixed", "number":
		ti.typ, ti.length, ti.scale = TypeDecimal, arg(0), arg(1)
	case "money", "smallmoney":
		ti.typ, ti.length, ti.scale = TypeDecimal, 19, 4
	case "float", "double", "real", "float4", "float8", "binary_float", "binary_double":
		ti.typ = TypeFloat
	case "bit", "bool", "boolean":
		ti.typ = TypeBool
	case "varchar", "nvarchar", "varchar2", "nvarchar2", "national", "character", "char", "nchar", "bpchar":
		switch {
		case len(args) > 0 && args[0] == "max":
			ti.typ = TypeText
		case words[0] == "varchar" || words[0] == "nvarchar" || words[0] == "varchar2" ||
			words[0] == "nvarchar2" || has("varying") || has("varchar"):
			ti.typ, ti.length = TypeVarchar, arg(0)
		default:
			ti.typ, ti.length = TypeChar, arg(0)
		}
	case "text", "tinytext", "mediumtext", "longtext", "ntext", "clob", "nclob", "citext", "string", "xml":
		ti.typ = TypeText
	case "date":
		ti.typ = TypeDate
	case "time", "timetz":
		ti.typ = TypeTime
	case "datetime", "datetime2", "smalldatetime", "timestamp", "timestamptz", "datetimeoffset":
		ti.typ = TypeDateTime
	case "json", "jsonb":
		ti.typ = TypeJSON
	case "uuid", "uniqueidentifier":
		ti.typ = TypeUUID
	case "blob", "tinyblob", "mediumblob", "longblob", "bytea", "binary", "varbinary", "image", "raw":
		ti.typ = TypeBlob
	case "enum", "set":
		n := 1
		for _, a := range args {
			n = max(n, len([]rune(a)))
		}
		ti.typ, ti.length = TypeVarchar, n
		ti.note = strings.ToUpper(words[0]) + " imported as VARCHAR"
	default:
		ti.typ = TypeText
		ti.note = "unknown type " + strings.ToUpper(full) + " imported as TEXT"
	}
	return ti
}

/* ---------------- DEFAULT ---------------- */

// defaultExpr reads the expression after DEFAULT: a literal, a function call
// or a parenthesized expression, with an optional PostgreSQL ::cast. ai is
// true for nextval(...), the default of serial columns.
func defaultExpr(c *cursor) (def string, ai bool) {
	start := c.i
	if c.peek().isPunct("-") || c.peek().isPunct("+") {
		c.next()
	}
	switch t := c.peek(); {
	case t.kind == tkEOF:
		return "", false
	case t.isPunct("("):
		c.group()
	default:
		c.next()
		if t.kind == tkWord && c.peek().isPunct("(") {
			c.group()
		}
	}
	for c.acceptPunct("::") {
		c.name()
		for c.peek().is("VARYING", "PRECISION", "WITH", "WITHOUT", "TIME", "ZONE") {
			c.next()
		}
		c.group()
		if c.acceptPunct("[") {
			c.acceptPunct("]")
		}
	}
	return interpretDefault(c, c.toks[start:c.i])
}

func interpretDefault(c *cursor, toks []token) (string, bool) {
	// drop a ::cast
	for i, t := range toks {
		if t.isPunct("::") {
			toks = toks[:i]
			break
		}
	}
	// unwrap ((0)) as SQL Server writes defaults
	for len(toks) >= 2 && toks[0].isPunct("(") && closes(toks) {
		toks = toks[1 : len(toks)-1]
	}
	switch {
	case len(toks) == 0:
		return "", false
	case len(toks) == 1 && toks[0].kind == tkString:
		return toks[0].val, false
	case len(toks) == 1 && toks[0].is("NULL"):
		return "", false
	}
	raw := c.raw(toks)
	if strings.Contains(strings.ToLower(raw), "nextval(") {
		return "", true
	}
	return raw, false
}

// closes reports whether the "(" at toks[0] is closed by the last token.
func closes(toks []token) bool {
	depth := 0
	for i, t := range toks {
		switch {
		case t.isPunct("("):
			depth++
		case t.isPunct(")"):
			depth--
			if depth == 0 {
				return i == len(toks)-1
			}
		}
	}
	return false
}

/* ---------------- INSERT and COPY ---------------- */

func (im *importer) insert(c *cursor) {
	c.next()
	if c.accept("OR") { // SQLite: INSERT OR REPLACE / IGNORE
		c.next()
	}
	for c.peek().is("IGNORE", "LOW_PRIORITY", "DELAYED", "HIGH_PRIORITY") {
		c.next()
	}
	c.accept("INTO")
	parts := c.qname()
	if len(parts) == 0 {
		return
	}
	var names []string
	if g, ok := c.group(); ok {
		for _, item := range split(g) {
			if len(item) > 0 && item[0].isName() {
				names = append(names, item[0].val)
			}
		}
	}
	if !c.accept("VALUES") && !c.accept("VALUE") {
		return // INSERT ... SELECT
	}
	td := im.table(parts, false)
	idx := make([]int, len(names))
	for i, n := range names {
		idx[i] = td.column(n)
	}
	for {
		g, ok := c.group()
		if !ok {
			break
		}
		var row []string
		for k, item := range split(g) {
			col := -1
			switch {
			case len(names) == 0:
				for len(td.names) <= k {
					td.column(fmt.Sprintf("col%d", len(td.names)+1))
				}
				col = k
			case k < len(idx):
				col = idx[k]
			}
			if col < 0 {
				continue
			}
			for len(row) <= col {
				row = append(row, "")
			}
			row[col] = valueText(c, item)
		}
		td.rows = append(td.rows, row)
		if !c.acceptPunct(",") {
			break
		}
	}
}

// valueText is the cell text of a value in VALUES (...): strings unquoted,
// NULL as NULL, anything else (numbers, function calls) as written.
func valueText(c *cursor, item []token) string {
	switch {
	case len(item) == 0:
		return ""
	case item[0].kind == tkString && (len(item) == 1 || item[1].isPunct("::")):
		return item[0].val
	case len(item) == 1 && item[0].kind == tkIdent: // MySQL "double-quoted" string
		return item[0].val
	case len(item) >= 5 && item[0].is("CAST") && item[1].isPunct("(") && item[2].kind == tkString && item[3].is("AS"):
		return item[2].val // SQL Server scripts: CAST(N'2024-01-01' AS Date)
	}
	return c.raw(item)
}

// copyHeader reads COPY table (columns) FROM stdin.
func (im *importer) copyHeader(c *cursor) (*tableDef, []int) {
	c.next()
	parts := c.qname()
	if len(parts) == 0 {
		return nil, nil
	}
	var names []string
	if g, ok := c.group(); ok {
		for _, item := range split(g) {
			if len(item) > 0 && item[0].isName() {
				names = append(names, item[0].val)
			}
		}
	}
	if !c.accept("FROM", "STDIN") {
		return nil, nil
	}
	td := im.table(parts, false)
	idx := make([]int, len(names))
	for i, n := range names {
		idx[i] = td.column(n)
	}
	return td, idx
}

var copyUnescaper = strings.NewReplacer(`\\`, `\`, `\t`, "\t", `\n`, "\n", `\r`, "\r", `\b`, "\b", `\f`, "\f", `\v`, "\v")

func (im *importer) copyRows(td *tableDef, idx []int, lines []string) {
	for _, line := range lines {
		var row []string
		for k, f := range strings.Split(line, "\t") {
			col := k
			if len(idx) > 0 {
				if k >= len(idx) {
					break
				}
				col = idx[k]
			} else {
				for len(td.names) <= k {
					td.column(fmt.Sprintf("col%d", len(td.names)+1))
				}
			}
			for len(row) <= col {
				row = append(row, "")
			}
			if f == `\N` {
				row[col] = "NULL"
			} else {
				row[col] = copyUnescaper.Replace(f)
			}
		}
		td.rows = append(td.rows, row)
	}
}

/* ---------------- ALTER TABLE, COMMENT ON ---------------- */

func (im *importer) alter(c *cursor) {
	c.next()
	if !c.accept("TABLE") {
		return
	}
	c.accept("ONLY")
	c.accept("IF", "EXISTS")
	c.accept("ONLY")
	td := im.lookup(c.qname())
	if td == nil {
		return
	}
	for _, action := range split(c.toks[c.i:]) {
		a := &cursor{toks: action, src: c.src}
		switch {
		case a.accept("WITH", "CHECK", "ADD"), a.accept("WITH", "NOCHECK", "ADD"), a.accept("ADD"): // SSMS: WITH CHECK ADD
			a.accept("COLUMN")
			if isConstraint(a.peek()) || a.peek().is("DEFAULT") {
				im.constraint(td, a)
			} else {
				im.columnDef(td, a)
			}
		case a.accept("MODIFY"):
			a.accept("COLUMN")
			im.columnDef(td, a)
		case a.accept("CHANGE"): // CHANGE old new type ...
			a.accept("COLUMN")
			old, _ := a.name()
			if i := td.find(old); i >= 0 {
				if n := a.peek(); n.isName() {
					td.names[i] = n.val
				}
			}
			im.columnDef(td, a)
		case a.accept("ALTER"):
			a.accept("COLUMN")
			n, _ := a.name()
			i := td.find(n)
			if i < 0 {
				continue
			}
			switch {
			case a.accept("SET", "DEFAULT"):
				def, ai := defaultExpr(a)
				td.cols[i].Default = def
				td.cols[i].AutoIncrement = td.cols[i].AutoIncrement || ai
			case a.accept("SET", "NOT", "NULL"):
				td.cols[i].NotNull = true
			case a.accept("ADD", "GENERATED"):
				td.cols[i].AutoIncrement = true
			}
		}
	}
}

// comment reads COMMENT ON COLUMN table.column IS 'text' (PostgreSQL).
func (im *importer) comment(c *cursor) {
	c.next()
	if !c.accept("ON", "COLUMN") {
		return
	}
	parts := c.qname()
	if len(parts) < 2 || !c.accept("IS") || c.peek().kind != tkString {
		return
	}
	td := im.lookup(parts[:len(parts)-1])
	if td == nil {
		return
	}
	if i := td.find(parts[len(parts)-1]); i >= 0 {
		td.cols[i].Comment = c.peek().val
	}
}

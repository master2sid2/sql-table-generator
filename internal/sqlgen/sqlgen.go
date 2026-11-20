// Package sqlgen turns a table — column definitions plus data rows — into
// CREATE TABLE and INSERT statements for MySQL, PostgreSQL, SQLite and SQL
// Server, and reads such statements back into a table.
package sqlgen

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Dialects.
const (
	MySQL    = "mysql"
	Postgres = "postgres"
	SQLite   = "sqlite"
	MSSQL    = "mssql"
)

// Generic column types. Each is mapped to a concrete type of the dialect.
const (
	TypeAuto     = "" // inferred from the data
	TypeInt      = "int"
	TypeBigInt   = "bigint"
	TypeSmallInt = "smallint"
	TypeDecimal  = "decimal"
	TypeFloat    = "float"
	TypeBool     = "bool"
	TypeVarchar  = "varchar"
	TypeChar     = "char"
	TypeText     = "text"
	TypeDate     = "date"
	TypeTime     = "time"
	TypeDateTime = "datetime"
	TypeJSON     = "json"
	TypeUUID     = "uuid"
	TypeBlob     = "blob"
)

var knownTypes = map[string]bool{
	TypeInt: true, TypeBigInt: true, TypeSmallInt: true, TypeDecimal: true, TypeFloat: true,
	TypeBool: true, TypeVarchar: true, TypeChar: true, TypeText: true, TypeDate: true,
	TypeTime: true, TypeDateTime: true, TypeJSON: true, TypeUUID: true, TypeBlob: true,
}

func isInteger(t string) bool { return t == TypeInt || t == TypeBigInt || t == TypeSmallInt }
func isNumeric(t string) bool { return isInteger(t) || t == TypeDecimal || t == TypeFloat }

// isText reports whether values of the type are written as strings and an
// empty string is a valid value.
func isText(t string) bool {
	return t == TypeVarchar || t == TypeChar || t == TypeText || t == TypeJSON || t == TypeUUID
}

// Column describes a column; its name is the table header.
type Column struct {
	Type          string    `json:"type"`             // a Type* constant; TypeAuto infers it from the data
	Length        int       `json:"length,omitempty"` // VARCHAR/CHAR length, DECIMAL precision; 0 = from the data
	Scale         int       `json:"scale,omitempty"`  // DECIMAL scale
	NotNull       bool      `json:"notNull,omitempty"`
	PrimaryKey    bool      `json:"pk,omitempty"`
	Unique        bool      `json:"unique,omitempty"`
	AutoIncrement bool      `json:"ai,omitempty"`
	Default       string    `json:"default,omitempty"` // a value, or an expression such as CURRENT_TIMESTAMP
	Comment       string    `json:"comment,omitempty"`
	Ref           Reference `json:"ref,omitzero"` // a foreign key; Ref.Table == "" means none
}

// Reference is a foreign key of a single column.
type Reference struct {
	Name     string `json:"name,omitempty"`     // constraint name; generated when empty
	Table    string `json:"table"`              // referenced table, may be schema-qualified
	Column   string `json:"column,omitempty"`   // referenced column; "" means id
	OnDelete string `json:"onDelete,omitempty"` // CASCADE, SET NULL, SET DEFAULT, RESTRICT, NO ACTION
	OnUpdate string `json:"onUpdate,omitempty"`
}

// ForeignKey is a foreign key over several columns. The editor does not edit
// them, but they are kept when SQL is imported and generated again.
type ForeignKey struct {
	Name       string   `json:"name,omitempty"`
	Columns    []string `json:"columns"`
	Table      string   `json:"table"`
	RefColumns []string `json:"refColumns"`
	OnDelete   string   `json:"onDelete,omitempty"`
	OnUpdate   string   `json:"onUpdate,omitempty"`
}

// Effective is a column the way it goes into the SQL: the name is final, the
// type is resolved and lengths are filled in.
type Effective struct {
	Column
	Name     string `json:"name"`
	Inferred bool   `json:"inferred,omitempty"` // the type was inferred from the data
	Skip     bool   `json:"skip,omitempty"`     // no name and no data: left out
	SQLType  string `json:"sqlType,omitempty"`  // the type in the chosen dialect
}

// Prepared is a table ready to be turned into SQL.
type Prepared struct {
	Columns     []Effective  // one per source column; Skip marks the ones left out
	Rows        [][]string   // rows that are not blank, padded to len(Columns)
	ForeignKeys []ForeignKey // multi-column foreign keys
	Warnings    []string
}

// Kept returns the columns that go into the SQL with their source positions.
func (p *Prepared) Kept() ([]*Effective, []int) {
	var cols []*Effective
	var idx []int
	for i := range p.Columns {
		if !p.Columns[i].Skip {
			cols = append(cols, &p.Columns[i])
			idx = append(idx, i)
		}
	}
	return cols, idx
}

func (p *Prepared) warn(format string, args ...any) {
	p.Warnings = append(p.Warnings, fmt.Sprintf(format, args...))
}

// Prepare resolves the columns of a table: names, types (inferred where the
// type is TypeAuto) and lengths. Blank rows are dropped, and so are columns
// that have neither a name nor data.
func Prepare(headers []string, rows [][]string, cols []Column) *Prepared {
	n := max(len(headers), len(cols))
	for _, r := range rows {
		n = max(n, len(r))
	}
	p := &Prepared{Columns: make([]Effective, n)}
	for _, r := range rows {
		row := make([]string, n)
		copy(row, r)
		if !isBlank(row) {
			p.Rows = append(p.Rows, row)
		}
	}

	seen := map[string]bool{}
	for c := range n {
		e := Effective{}
		if c < len(cols) {
			e.Column = cols[c]
		}
		if c < len(headers) {
			e.Name = strings.TrimSpace(headers[c])
		}
		values := make([]string, len(p.Rows))
		hasData := false
		for i, r := range p.Rows {
			values[i] = r[c]
			hasData = hasData || strings.TrimSpace(r[c]) != ""
		}
		if e.Name == "" {
			if !hasData {
				e.Skip = true
				p.Columns[c] = e
				continue
			}
			e.Name = fmt.Sprintf("col%d", c+1)
			p.warn("column %d has no name: called it %s", c+1, e.Name)
		}
		if key := strings.ToLower(e.Name); seen[key] {
			p.warn("column name %q is used twice", e.Name)
		} else {
			seen[key] = true
		}
		p.resolveType(&e, values)
		p.Columns[c] = e
	}
	return p
}

func (p *Prepared) resolveType(e *Effective, values []string) {
	it := infer(values)
	if !knownTypes[e.Type] {
		e.Type = TypeAuto
	}
	if e.Type == TypeAuto {
		e.Inferred = true
		e.Type, e.Length, e.Scale = it.typ, it.length, it.scale
		// an empty key column is most likely an integer id
		if e.AutoIncrement && !isInteger(e.Type) || e.Ref.Table != "" && it.maxLen == 0 {
			e.Type, e.Length, e.Scale = TypeInt, 0, 0
		}
		return
	}
	switch e.Type {
	case TypeVarchar:
		if e.Length <= 0 {
			e.Length = varcharLen(it.maxLen)
		}
		e.Scale = 0
	case TypeChar:
		if e.Length <= 0 {
			e.Length = max(1, it.maxLen)
		}
		e.Scale = 0
	case TypeDecimal:
		if e.Length <= 0 {
			if it.typ == TypeDecimal {
				e.Length, e.Scale = it.length, it.scale
			} else {
				e.Length, e.Scale = 10, 2
			}
		}
		e.Scale = min(max(e.Scale, 0), e.Length)
	default:
		e.Length, e.Scale = 0, 0
	}
	if e.AutoIncrement && !isInteger(e.Type) {
		e.AutoIncrement = false
		p.warn("%s: auto increment needs an integer type, ignored", e.Name)
	}
}

func isBlank(row []string) bool {
	for _, v := range row {
		if strings.TrimSpace(v) != "" {
			return false
		}
	}
	return true
}

// isNull reports whether a cell holds no value: it is empty or says NULL.
func isNull(v string) bool {
	v = strings.TrimSpace(v)
	return v == "" || strings.EqualFold(v, "NULL")
}

/* ---------------- type inference ---------------- */

var (
	numRe      = regexp.MustCompile(`^[+-]?(\d*)(?:\.(\d*))?$`)
	floatRe    = regexp.MustCompile(`^[+-]?(\d+\.?\d*|\.\d+)([eE][+-]?\d+)?$`)
	dateRe     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	dateTimeRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}(:?\d{2})?)?$`)
	timeRe     = regexp.MustCompile(`^\d{2}:\d{2}(:\d{2}(\.\d+)?)?$`)
	uuidRe     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

type inferred struct {
	typ           string
	length, scale int
	maxLen        int // longest value in characters
}

// decimalParts splits a plain decimal number into its integer and fraction
// digits. Numbers with leading zeros ("007", zip codes) are not numbers.
func decimalParts(v string) (intDigits, fracDigits int, ok bool) {
	m := numRe.FindStringSubmatch(v)
	if m == nil || m[1]+m[2] == "" {
		return 0, 0, false
	}
	if leadingZero(v) {
		return 0, 0, false
	}
	return len(strings.TrimLeft(m[1], "0")), len(m[2]), true
}

// leadingZero reports numbers written with leading zeros, such as zip codes.
func leadingZero(v string) bool {
	v = strings.TrimLeft(v, "+-")
	return len(v) > 1 && v[0] == '0' && isDigit(v[1])
}

func infer(values []string) inferred {
	var (
		isInt, isDec, isFloat, isBool      = true, true, true, true
		isDate, isDateTime, isTime, isUUID = true, true, true, true
		isJSON                             = true
		big                                bool
		maxInt, maxFrac, maxLen, nonEmpty  int
		hasDot                             bool
	)
	for _, raw := range values {
		if isNull(raw) {
			continue
		}
		nonEmpty++
		maxLen = max(maxLen, utf8.RuneCountInString(raw))
		v := strings.TrimSpace(raw)

		if ip, fp, ok := decimalParts(v); ok {
			maxInt, maxFrac = max(maxInt, ip), max(maxFrac, fp)
			if strings.Contains(v, ".") {
				hasDot, isInt = true, false
			} else if n, err := strconv.ParseInt(v, 10, 64); err != nil {
				isInt = false
			} else if n > 1<<31-1 || n < -1<<31 {
				big = true
			}
		} else {
			isInt, isDec = false, false
		}
		isFloat = isFloat && floatRe.MatchString(v) && !leadingZero(v)
		isBool = isBool && (strings.EqualFold(v, "true") || strings.EqualFold(v, "false"))
		isDate = isDate && dateRe.MatchString(v)
		isDateTime = isDateTime && (dateRe.MatchString(v) || dateTimeRe.MatchString(v))
		isTime = isTime && timeRe.MatchString(v)
		isUUID = isUUID && uuidRe.MatchString(v)
		isJSON = isJSON && (strings.HasPrefix(v, "{") || strings.HasPrefix(v, "[")) && json.Valid([]byte(v))
	}

	res := inferred{maxLen: maxLen}
	switch {
	case nonEmpty == 0:
		res.typ, res.length = TypeVarchar, 255
	case isInt && big:
		res.typ = TypeBigInt
	case isInt:
		res.typ = TypeInt
	case isDec && hasDot && maxInt+maxFrac <= 38:
		res.typ, res.length, res.scale = TypeDecimal, max(10, maxInt+maxFrac), maxFrac
	case isFloat:
		res.typ = TypeFloat
	case isBool:
		res.typ = TypeBool
	case isDate:
		res.typ = TypeDate
	case isDateTime:
		res.typ = TypeDateTime
	case isTime:
		res.typ = TypeTime
	case isUUID:
		res.typ = TypeUUID
	case isJSON:
		res.typ = TypeJSON
	case maxLen > 1024:
		res.typ = TypeText
	default:
		res.typ, res.length = TypeVarchar, varcharLen(maxLen)
	}
	return res
}

// varcharLen rounds a length up to a size people usually pick.
func varcharLen(n int) int {
	for _, l := range []int{16, 32, 64, 128, 255, 512, 1024} {
		if n <= l {
			return l
		}
	}
	return (n + 255) / 256 * 256
}

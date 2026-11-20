package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/master2sid2/sql-table-generator/internal/sqlgen"
	"github.com/master2sid2/sql-table-generator/internal/table"
)

const maxBody = 20 << 20 // 20 MiB

type generateRequest struct {
	Table   table.Table     `json:"table"`
	Columns []sqlgen.Column `json:"columns"`
	Options sqlgen.Options  `json:"options"`
}

type generateResponse struct {
	SQL       string             `json:"sql"`
	Preview   string             `json:"preview"`   // the data as a mysql-client table
	Structure string             `json:"structure"` // the columns as DESCRIBE shows them
	Columns   []sqlgen.Effective `json:"columns"`   // one per column of the request
	Warnings  []string           `json:"warnings"`
}

type parseRequest struct {
	Text   string `json:"text"`
	Format string `json:"format"`
	Header bool   `json:"header"`
}

type parseResponse struct {
	table.Table
	Format    string          `json:"format"`
	HasHeader bool            `json:"hasHeader"` // false: headers are generated, all lines are in rows
	Columns   []sqlgen.Column `json:"columns,omitempty"`
	TableName string          `json:"tableName,omitempty"`
	Dialect   string          `json:"dialect,omitempty"`
	Notes     []string        `json:"notes,omitempty"`
}

func handleGenerate(w http.ResponseWriter, r *http.Request) {
	var req generateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	p := sqlgen.Prepare(req.Table.Headers, req.Table.Rows, req.Columns)
	sql, warnings := sqlgen.Generate(p, req.Options)

	cols, idx := p.Kept()
	data := table.Table{Headers: make([]string, len(cols)), Rows: make([][]string, len(p.Rows))}
	for i, e := range cols {
		data.Headers[i] = e.Name
	}
	for r, row := range p.Rows {
		data.Rows[r] = make([]string, len(idx))
		for i, c := range idx {
			data.Rows[r][i] = row[c]
		}
	}
	preview := table.Render(data, table.Options{Align: table.AlignAuto, EmptyAsNull: req.Options.EmptyAsNull})
	structure := ""
	if len(cols) > 0 {
		structure = table.Render(sqlgen.Structure(p, req.Options.Dialect), table.Options{Align: table.AlignLeft})
	}
	writeJSON(w, http.StatusOK, generateResponse{
		SQL:       sql,
		Preview:   preview,
		Structure: structure,
		Columns:   p.Columns,
		Warnings:  append(p.Warnings, warnings...),
	})
}

func handleParse(w http.ResponseWriter, r *http.Request) {
	var req parseRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	respondParsed(w, req.Text, req.Format, req.Header, "")
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	file, fh, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "cannot read the uploaded file: "+err.Error())
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeError(w, http.StatusBadRequest, "cannot read the uploaded file: "+err.Error())
		return
	}
	respondParsed(w, table.DecodeBytes(data), r.FormValue("format"), r.FormValue("header") != "false", fh.Filename)
}

// respondParsed reads an SQL script, a DESCRIBE output, or table data
// (CSV, TSV, mysql client output ...).
func respondParsed(w http.ResponseWriter, text, format string, header bool, filename string) {
	isSQL := format == "sql"
	if format == "" || format == table.FormatAuto {
		isSQL = strings.HasSuffix(strings.ToLower(filename), ".sql") || sqlgen.LooksLikeSQL(text)
	}
	if isSQL {
		imp, err := sqlgen.Import(text)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, parseResponse{
			Table:     table.Table{Headers: imp.Headers, Rows: imp.Rows},
			Format:    "sql",
			HasHeader: true,
			Columns:   imp.Columns,
			TableName: imp.Table,
			Dialect:   imp.Dialect,
			Notes:     imp.Notes,
		})
		return
	}

	res, err := table.ParseText(text, format, header)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if names, cols, ok := sqlgen.FromDescribe(res.Table, text); ok {
		writeJSON(w, http.StatusOK, parseResponse{
			Table:     table.Table{Headers: names, Rows: [][]string{}},
			Format:    "describe",
			HasHeader: true,
			Columns:   cols,
		})
		return
	}
	writeJSON(w, http.StatusOK, parseResponse{Table: res.Table, Format: res.Format, HasHeader: res.HasHeader})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		status := http.StatusBadRequest
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			status = http.StatusRequestEntityTooLarge
		}
		writeError(w, status, "invalid request: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

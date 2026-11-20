package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func post(t *testing.T, h http.HandlerFunc, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
	var resp map[string]any
	if err := json.NewDecoder(bytes.NewReader(rec.Body.Bytes())).Decode(&resp); err != nil {
		t.Fatalf("decode %q: %v", rec.Body, err)
	}
	return rec, resp
}

func TestHandleGenerate(t *testing.T) {
	body := `{"table":{"headers":["id","name"],"rows":[["1","Alice"],["10",""]]},
		"columns":[{"type":"","pk":true}],
		"options":{"dialect":"postgres","table":"people","create":true,"insert":true,"emptyAsNull":true}}`
	rec := httptest.NewRecorder()
	handleGenerate(rec, httptest.NewRequest(http.MethodPost, "/api/generate", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var resp generateResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	wantSQL := "CREATE TABLE people (\n" +
		"  id   INTEGER     NOT NULL PRIMARY KEY,\n" +
		"  name VARCHAR(16)\n" +
		");\n\n" +
		"INSERT INTO people (id, name) VALUES\n" +
		"  (1, 'Alice'),\n" +
		"  (10, NULL);\n"
	if resp.SQL != wantSQL {
		t.Errorf("sql:\n%s\nwant:\n%s", resp.SQL, wantSQL)
	}
	wantPreview := "+----+-------+\n" +
		"| id | name  |\n" +
		"+----+-------+\n" +
		"|  1 | Alice |\n" +
		"| 10 | NULL  |\n" +
		"+----+-------+\n"
	if resp.Preview != wantPreview {
		t.Errorf("preview:\n%s\nwant:\n%s", resp.Preview, wantPreview)
	}
	if !strings.Contains(resp.Structure, "| id    | INTEGER     | NO   | PRI |") {
		t.Errorf("structure:\n%s", resp.Structure)
	}
	if len(resp.Columns) != 2 || resp.Columns[1].SQLType != "VARCHAR(16)" || !resp.Columns[1].Inferred {
		t.Errorf("columns: %+v", resp.Columns)
	}
}

func TestHandleParse(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		status     int
		wantFormat string
		wantErr    string
	}{
		{"csv", `{"text":"a;b\n1;2","format":"auto","header":true}`, http.StatusOK, "csv", ""},
		{"mysql box", `{"text":"+---+\n| a |\n+---+\n| 1 |\n+---+","format":"auto"}`, http.StatusOK, "mysql", ""},
		{"sql", `{"text":"CREATE TABLE t (id INT PRIMARY KEY);\nINSERT INTO t VALUES (1);","format":"auto"}`, http.StatusOK, "sql", ""},
		{"forced sql without table", `{"text":"SELECT 1","format":"sql"}`, http.StatusUnprocessableEntity, "", "no CREATE TABLE"},
		{"describe", `{"text":"+-------+------+------+-----+---------+-------+\n| Field | Type | Null | Key | Default | Extra |\n+-------+------+------+-----+---------+-------+\n| id    | int  | NO   | PRI | NULL    |       |\n+-------+------+------+-----+---------+-------+","format":"auto"}`, http.StatusOK, "describe", ""},
		{"empty", `{"text":"   ","format":"auto"}`, http.StatusUnprocessableEntity, "", "nothing to parse"},
		{"unknown format", `{"text":"a","format":"xml"}`, http.StatusUnprocessableEntity, "", "unknown format"},
		{"not json", `{`, http.StatusBadRequest, "", "invalid request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec, resp := post(t, handleParse, tt.body)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.status, rec.Body)
			}
			if tt.wantFormat != "" && resp["format"] != tt.wantFormat {
				t.Errorf("format = %v, want %s", resp["format"], tt.wantFormat)
			}
			if tt.wantErr != "" && !strings.Contains(resp["error"].(string), tt.wantErr) {
				t.Errorf("error = %v, want %q", resp["error"], tt.wantErr)
			}
		})
	}
}

func TestHandleParseSQLColumns(t *testing.T) {
	_, resp := post(t, handleParse, `{"text":"CREATE TABLE `+"`users`"+` (`+"`id`"+` int NOT NULL AUTO_INCREMENT PRIMARY KEY, name varchar(40));","format":"auto"}`)
	cols, _ := json.Marshal(resp["columns"])
	if resp["tableName"] != "users" || resp["dialect"] != "mysql" ||
		string(cols) != `[{"ai":true,"notNull":true,"pk":true,"type":"int"},{"length":40,"type":"varchar"}]` {
		t.Errorf("got %v", resp)
	}
}

func TestHandleParseTooLarge(t *testing.T) {
	body := `{"text":"` + strings.Repeat("x", maxBody) + `"}`
	rec := httptest.NewRecorder()
	handleParse(rec, httptest.NewRequest(http.MethodPost, "/api/parse", strings.NewReader(body)))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

func upload(t *testing.T, name string, data []byte) parseResponse {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	_ = mw.WriteField("format", "auto")
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	handleUpload(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	var resp parseResponse
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestHandleUpload(t *testing.T) {
	// Windows-1251 "Имя,Цвет\nИван,Синий" as exported by a Cyrillic-locale Excel.
	resp := upload(t, "data.csv", []byte{0xC8, 0xEC, 0xFF, ',', 0xD6, 0xE2, 0xE5, 0xF2, '\n', 0xC8, 0xE2, 0xE0, 0xED, ',', 0xD1, 0xE8, 0xED, 0xE8, 0xE9})
	if strings.Join(resp.Headers, ",") != "Имя,Цвет" || len(resp.Rows) != 1 || strings.Join(resp.Rows[0], ",") != "Иван,Синий" {
		t.Errorf("got headers %v rows %v", resp.Headers, resp.Rows)
	}
}

func TestHandleUploadSQL(t *testing.T) {
	// A .sql file is read as SQL even when it starts with something unusual.
	resp := upload(t, "dump.sql", []byte("\\connect shop\nCREATE TABLE t (a int);\nINSERT INTO t VALUES (7);\n"))
	if resp.Format != "sql" || resp.TableName != "t" || len(resp.Rows) != 1 || resp.Rows[0][0] != "7" {
		t.Errorf("got %+v", resp)
	}
}

func TestHandleUploadWithoutFile(t *testing.T) {
	rec := httptest.NewRecorder()
	handleUpload(rec, httptest.NewRequest(http.MethodPost, "/api/upload", strings.NewReader("")))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

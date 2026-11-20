package table

import (
	"reflect"
	"testing"
)

func TestRender(t *testing.T) {
	tb := Table{
		Headers: []string{"id", "name", "city"},
		Rows: [][]string{
			{"1", "Alice", "Яблоко"},
			{"20", "Bob | B", ""},
		},
	}
	got := Render(tb, Options{Align: AlignAuto, EmptyAsNull: true, Footer: true})
	want := "" +
		"+----+---------+--------+\n" +
		"| id | name    | city   |\n" +
		"+----+---------+--------+\n" +
		"|  1 | Alice   | Яблоко |\n" +
		"| 20 | Bob | B | NULL   |\n" +
		"+----+---------+--------+\n" +
		"2 rows in set (0.00 sec)\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderWide(t *testing.T) {
	got := Render(Table{Headers: []string{"a"}, Rows: [][]string{{"漢字"}}}, Options{})
	want := "+------+\n| a    |\n+------+\n| 漢字 |\n+------+\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSkipEmpty(t *testing.T) {
	tb := Table{Headers: []string{"a", "", "b"}, Rows: [][]string{{"1", "", "2"}, {"", "", ""}}}
	n := Normalize(tb, true)
	want := Table{Headers: []string{"a", "b"}, Rows: [][]string{{"1", "2"}}}
	if !reflect.DeepEqual(n, want) {
		t.Fatalf("got %#v", n)
	}
}

func TestRoundTrip(t *testing.T) {
	tb := Table{
		Headers: []string{"id", "name", "note"},
		Rows: [][]string{
			{"1", "Иван", "a | b"},
			{"2", "漢字", "NULL"},
		},
	}
	text := "mysql> select * from t;\n" + Render(tb, Options{Footer: true})
	got, format, err := Parse(text, FormatAuto, true)
	if err != nil {
		t.Fatal(err)
	}
	if format != FormatMySQL {
		t.Fatalf("format = %s", format)
	}
	if !reflect.DeepEqual(got, tb) {
		t.Fatalf("got %#v", got)
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name, text string
		header     bool
		format     string
		want       Table
	}{
		{
			name: "csv comma", text: "a,b\n1,\"x, y\"\n", header: true, format: FormatCSV,
			want: Table{Headers: []string{"a", "b"}, Rows: [][]string{{"1", "x, y"}}},
		},
		{
			name: "csv semicolon ragged", text: "a;b;c\n1;2\n", header: true, format: FormatCSV,
			want: Table{Headers: []string{"a", "b", "c"}, Rows: [][]string{{"1", "2", ""}}},
		},
		{
			name: "tsv no header", text: "1\t2\n3\t4", header: false, format: FormatTSV,
			want: Table{Headers: []string{"col1", "col2"}, Rows: [][]string{{"1", "2"}, {"3", "4"}}},
		},
		{
			name: "markdown", text: "| a | b |\n|---|:--|\n| 1 | 2 |", header: true, format: FormatMySQL,
			want: Table{Headers: []string{"a", "b"}, Rows: [][]string{{"1", "2"}}},
		},
		{
			name: "pipe rows only", text: "|  1 | x |\n|  2 | y |", header: true, format: FormatMySQL,
			want: Table{Headers: []string{"col1", "col2"}, Rows: [][]string{{"1", "x"}, {"2", "y"}}},
		},
		{
			name: "bare pipe row (piece of mysql output)", text: "05:00:02 | 05:00:02 | <1 s", header: true, format: FormatPipe,
			want: Table{Headers: []string{"col1", "col2", "col3"}, Rows: [][]string{{"05:00:02", "05:00:02", "<1 s"}}},
		},
		{
			name: "psql", text: " id | name\n----+------\n  1 | Ann\n(1 row)\n", header: false, format: FormatPipe,
			want: Table{Headers: []string{"id", "name"}, Rows: [][]string{{"1", "Ann"}}},
		},
		{
			name: "commas win over a single pipe", text: "a,b|c,d\n", header: false, format: FormatCSV,
			want: Table{Headers: []string{"col1", "col2", "col3"}, Rows: [][]string{{"a", "b|c", "d"}}},
		},
		{
			name:   "vertical",
			text:   "*************************** 1. row ***************************\n  id: 1\nname: Alice\n*************************** 2. row ***************************\n  id: 2\nname: Bob\n2 rows in set (0.00 sec)",
			header: true, format: FormatVertical,
			want: Table{Headers: []string{"id", "name"}, Rows: [][]string{{"1", "Alice"}, {"2", "Bob"}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, format, err := Parse(tt.text, FormatAuto, tt.header)
			if err != nil {
				t.Fatal(err)
			}
			if format != tt.format {
				t.Errorf("format = %s, want %s", format, tt.format)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %#v\nwant %#v", got, tt.want)
			}
		})
	}
}

func TestDecodeCP1251(t *testing.T) {
	if got := DecodeBytes([]byte{0xCF, 0xF0, 0xE8, 0xE2, 0xE5, 0xF2, 0x2C, 0xA8}); got != "Привет,Ё" {
		t.Fatalf("got %q", got)
	}
	if got := DecodeBytes([]byte{0xFF, 0xFE, 'h', 0, 0x1F, 0x04}); got != "hП" {
		t.Fatalf("got %q", got)
	}
}

func TestDisplayWidth(t *testing.T) {
	tests := map[string]int{
		"abc":    3,
		"Привет": 6,
		"漢字":     4,
		"🚀":      2, // transport & map symbols
		"🪐":      2, // symbols & pictographs extended-A
		"😀":      2,
		"é":      1, // e + combining acute accent
	}
	for s, want := range tests {
		if got := DisplayWidth(s); got != want {
			t.Errorf("DisplayWidth(%q) = %d, want %d", s, got, want)
		}
	}
}

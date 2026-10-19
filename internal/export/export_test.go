package export

import (
	"bytes"
	"encoding/json"
	"testing"

	"gotable/internal/db"
	"gotable/internal/model"
)

func sample() *db.ResultSet {
	return &db.ResultSet{
		Columns: []string{"id", "name", "note", "price"},
		Rows: [][]db.Value{
			{db.Text("1"), db.Text("Alice"), db.NullValue, db.Text("1.5")},
			{db.Text("2"), db.Text(`Bob "B", Jr.`), db.Text("line1\nline2"), db.Text("20")},
			{db.Text("3"), db.Text(" pad "), db.Text(""), db.Text("x.5")},
		},
	}
}

func TestCSVDefault(t *testing.T) {
	var b bytes.Buffer
	if err := CSV(&b, sample(), DefaultCSVOptions()); err != nil {
		t.Fatal(err)
	}
	want := "id,name,note,price\n" +
		"1,Alice,NULL,1.5\n" +
		"2,\"Bob \"\"B\"\", Jr.\",\"line1\nline2\",20\n" +
		"3,\" pad \",,x.5\n"
	if b.String() != want {
		t.Errorf("got\n%q\nwant\n%q", b.String(), want)
	}
}

func TestCSVOptions(t *testing.T) {
	var b bytes.Buffer
	o := CSVOptions{Delimiter: ';', Quote: QuoteAlways, LineBreak: "\r\n", NullAsEmpty: true, DecimalComma: true}
	if err := CSV(&b, sample(), o); err != nil {
		t.Fatal(err)
	}
	want := "\"1\";\"Alice\";;\"1,5\"\r\n" +
		"\"2\";\"Bob \"\"B\"\", Jr.\";\"line1\nline2\";\"20\"\r\n" +
		"\"3\";\" pad \";\"\";\"x.5\"\r\n"
	if b.String() != want {
		t.Errorf("got\n%q\nwant\n%q", b.String(), want)
	}
}

func TestSQL(t *testing.T) {
	var b bytes.Buffer
	d := db.DialectFor(model.DriverMySQL)
	err := SQL(&b, d, sample(), SQLOptions{Table: db.TableRef{Name: "people"}, BatchSize: 2, CreateDDL: "CREATE TABLE people (id int);\n"})
	if err != nil {
		t.Fatal(err)
	}
	want := "CREATE TABLE people (id int);\n\n" +
		"INSERT INTO `people` (`id`, `name`, `note`, `price`) VALUES\n" +
		"  ('1', 'Alice', NULL, '1.5'),\n" +
		"  ('2', 'Bob \"B\", Jr.', 'line1\nline2', '20');\n" +
		"INSERT INTO `people` (`id`, `name`, `note`, `price`) VALUES\n" +
		"  ('3', ' pad ', '', 'x.5');\n"
	if b.String() != want {
		t.Errorf("got\n%s\nwant\n%s", b.String(), want)
	}

	b.Reset()
	SQL(&b, db.DialectFor(model.DriverPostgres), &db.ResultSet{Columns: []string{"a"}, Rows: [][]db.Value{{db.Text("it's")}}},
		SQLOptions{Table: db.TableRef{Schema: "s", Name: "t"}})
	if got := b.String(); got != `INSERT INTO "s"."t" ("a") VALUES ('it''s');`+"\n" {
		t.Error(got)
	}
}

func TestJSON(t *testing.T) {
	var b bytes.Buffer
	if err := JSON(&b, sample()); err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	if err := json.Unmarshal(b.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, b.String())
	}
	if len(out) != 3 || out[0]["note"] != nil || out[1]["note"] != "line1\nline2" {
		t.Errorf("%v", out)
	}
	// Keys keep column order.
	if !bytes.HasPrefix(b.Bytes(), []byte("[\n  {\"id\": \"1\", \"name\"")) {
		t.Errorf("order: %s", b.String())
	}
	b.Reset()
	JSON(&b, &db.ResultSet{Columns: []string{"a"}})
	if b.String() != "[]\n" {
		t.Errorf("empty: %q", b.String())
	}
}

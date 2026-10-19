package db

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuildAddColumn(t *testing.T) {
	tr := TableRef{Schema: "public", Name: "people"}
	cases := []struct {
		d    Dialect
		c    ColumnDef
		want []string
	}{
		{pg, ColumnDef{Name: "email", Type: "varchar(255)", Nullable: true},
			[]string{`ALTER TABLE "public"."people" ADD COLUMN "email" varchar(255);`}},
		{pg, ColumnDef{Name: "created_at", Type: "timestamp with time zone", DefaultKind: DefaultExpression, Default: "now()", Comment: "row's birth"},
			[]string{`ALTER TABLE "public"."people" ADD COLUMN "created_at" timestamp with time zone NOT NULL DEFAULT now();`,
				`COMMENT ON COLUMN "public"."people"."created_at" IS 'row''s birth';`}},
		{pg, ColumnDef{Name: "tags", Type: "text[]", Nullable: true, DefaultKind: DefaultNull},
			[]string{`ALTER TABLE "public"."people" ADD COLUMN "tags" text[] DEFAULT NULL;`}},
		{my, ColumnDef{Name: "status", Type: "enum('new','done')", DefaultKind: DefaultValue, Default: "new", Comment: "state", Position: "name"},
			[]string{"ALTER TABLE `public`.`people` ADD COLUMN `status` enum('new','done') NOT NULL DEFAULT 'new' COMMENT 'state' AFTER `name`;"}},
		{my, ColumnDef{Name: "id2", Type: "int", Nullable: true, Position: "first"},
			[]string{"ALTER TABLE `public`.`people` ADD COLUMN `id2` int FIRST;"}},
		{sqlite, ColumnDef{Name: "score", Type: "numeric(10,2)", DefaultKind: DefaultValue, Default: "0"},
			[]string{`ALTER TABLE "people" ADD COLUMN "score" numeric(10,2) NOT NULL DEFAULT '0';`}},
	}
	for _, c := range cases {
		sq := c.d
		tbl := tr
		if sq.Kind() == "sqlite" {
			tbl = TableRef{Schema: "main", Name: "people"}
		}
		got, err := BuildAddColumn(sq, tbl, c.c)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %+v:\n got %q %v\nwant %q", sq.Kind(), c.c, got, err, c.want)
		}
	}
}

func TestAddColumnValidation(t *testing.T) {
	tr := TableRef{Name: "t"}
	for _, c := range []struct {
		d    Dialect
		c    ColumnDef
		want string
	}{
		{pg, ColumnDef{Type: "int"}, "name is required"},
		{pg, ColumnDef{Name: "a"}, "type is required"},
		{pg, ColumnDef{Name: "a", Type: "int; DROP TABLE x"}, "doesn't look like a column type"},
		{pg, ColumnDef{Name: "a", Type: "int", DefaultKind: DefaultExpression}, "enter the default expression"},
		{pg, ColumnDef{Name: "a", Type: "int", DefaultKind: DefaultExpression, Default: "1; DROP TABLE t"}, "can't contain ';'"},
		{pg, ColumnDef{Name: "a", Type: "int", DefaultKind: DefaultNull}, "NOT NULL column can't default to NULL"},
		{pg, ColumnDef{Name: "a", Type: "int", Nullable: true, Position: "first"}, "only be chosen on MySQL"},
		{sqlite, ColumnDef{Name: "a", Type: "int"}, "NOT NULL column if it has a default"},
		{sqlite, ColumnDef{Name: "a", Type: "text", Nullable: true, DefaultKind: DefaultExpression, Default: "CURRENT_TIMESTAMP"}, "constant default"},
		{sqlite, ColumnDef{Name: "a", Type: "text", Nullable: true, Comment: "x"}, "doesn't support column comments"},
	} {
		_, err := BuildAddColumn(c.d, tr, c.c)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s %+v: %v, want %q", c.d.Kind(), c.c, err, c.want)
		}
	}
}

func TestBuildDrop(t *testing.T) {
	objs := []Object{{Schema: "s", Name: "a"}, {Schema: "s", Name: "v", Kind: KindView}, {Schema: "s", Name: "b"}}
	for _, c := range []struct {
		d       Dialect
		cascade bool
		want    []string
	}{
		{pg, false, []string{`DROP VIEW "s"."v";`, `DROP TABLE "s"."a", "s"."b";`}},
		{pg, true, []string{`DROP VIEW "s"."v" CASCADE;`, `DROP TABLE "s"."a", "s"."b" CASCADE;`}},
		{my, true, []string{"SET FOREIGN_KEY_CHECKS = 0;", "DROP VIEW `s`.`v`;", "DROP TABLE `s`.`a`, `s`.`b`;", "SET FOREIGN_KEY_CHECKS = 1;"}},
		{sqlite, true, []string{"PRAGMA foreign_keys = OFF;", `DROP VIEW "s"."v";`, `DROP TABLE "s"."a";`, `DROP TABLE "s"."b";`, "PRAGMA foreign_keys = ON;"}},
	} {
		got, err := BuildDrop(c.d, objs, c.cascade)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s cascade=%v:\n got %q\nwant %q", c.d.Kind(), c.cascade, got, c.want)
		}
	}
	if _, err := BuildDrop(pg, nil, false); err == nil {
		t.Error("empty selection must fail")
	}
}

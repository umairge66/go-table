package db

import "testing"

func TestPgEscapeLiteral(t *testing.T) {
	for def, want := range map[string]string{
		`e'it\'s'::STRING`:   "it's",
		`e'a\\b\nc'::STRING`: "a\\b\nc",
		`E'x''y':::STRING`:   "x'y",
		`e'pending'::STRING`: "pending",
	} {
		m := pgEscapeLiteral.FindStringSubmatch(def)
		if m == nil {
			t.Errorf("%s: no match", def)
			continue
		}
		if got := pgUnescape(m[1]); got != want {
			t.Errorf("%s: %q, want %q", def, got, want)
		}
	}
	if pgEscapeLiteral.MatchString(`now()`) || pgEscapeLiteral.MatchString(`e'a'::STRING || 'b'`) {
		t.Error("matched an expression")
	}
}

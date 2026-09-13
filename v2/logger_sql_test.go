package core

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The guarantee everything else rests on: highlighting adds escape codes and
// nothing else. A statement copied out of a log has to be the one the database
// ran, character for character.
func TestHighlightSQL_preservesTheStatement(t *testing.T) {
	for _, sql := range []string{
		`SELECT * FROM "users" WHERE "users"."deleted_at" IS NULL ORDER BY created_at DESC LIMIT 2`,
		`INSERT INTO "users" ("id","email") VALUES ('a-1','someone@example.co')`,
		`UPDATE users SET full_name = 'O''Brien' WHERE id = 42`,
		`SELECT count(*) FROM users WHERE (LOWER(email) LIKE '%a@b%' OR full_name ILIKE '%x%')`,
		`SELECT 1`,
		`DELETE FROM t WHERE n BETWEEN 1.5 AND 99`,
		``,
		`weird ) ( ;; -- trailing`,
		`SELECT 'unterminated`,
		`SELECT "unterminated`,
	} {
		assert.Equal(t, sql, stripANSI(highlightSQL(sql)), "highlighting must not change the text")
	}
}

func TestHighlightSQL_coloursTheThreeKinds(t *testing.T) {
	out := highlightSQL(`SELECT * FROM users WHERE name = 'bob' AND age > 30`)

	assert.Contains(t, out, ansiBold+ansiBlue+"SELECT"+ansiReset, "keywords")
	assert.Contains(t, out, ansiGreen+"'bob'"+ansiReset, "string literals")
	assert.Contains(t, out, ansiCyan+"30"+ansiReset, "numbers")
}

// Names are what the reader is scanning for; colouring them competes with the
// keywords.
func TestHighlightSQL_leavesIdentifiersPlain(t *testing.T) {
	out := highlightSQL(`SELECT * FROM "users" WHERE "users"."id" = 1`)

	assert.Contains(t, out, `"users"`, "quoted identifiers are untouched")
	assert.NotContains(t, out, ansiGreen+`"users"`)
}

// A keyword is a keyword whatever case it was written in; an ordinary word is
// left alone even when it contains one.
func TestHighlightSQL_matchesKeywordsWholeAndCaseInsensitively(t *testing.T) {
	assert.Contains(t, highlightSQL(`select * from t`), ansiBold+ansiBlue+"select"+ansiReset)

	// "selection" is not SELECT, and "ordered" is not ORDER
	out := highlightSQL(`SELECT selection, ordered FROM t`)
	assert.NotContains(t, out, ansiBold+ansiBlue+"selection")
	assert.NotContains(t, out, ansiBold+ansiBlue+"ordered")
}

// A literal that happens to contain SQL must stay one green run, or a value
// someone typed would be rendered as if it were code.
func TestHighlightSQL_doesNotLookInsideLiterals(t *testing.T) {
	out := highlightSQL(`SELECT * FROM t WHERE note = 'SELECT FROM WHERE'`)

	assert.Contains(t, out, ansiGreen+`'SELECT FROM WHERE'`+ansiReset)
}

// The plain path has to stay byte-identical: a redirected log or a JSON pipeline
// must never receive escape codes.
func TestPretty_sqlIsHighlightedOnlyWithColour(t *testing.T) {
	stmt := `SELECT * FROM "users" WHERE id = 1`

	plain, plainBuf := pretty(false)
	plain.Debug("query", "sql", stmt)
	assert.NotContains(t, plainBuf.String(), "\x1b[")
	assert.Contains(t, plainBuf.String(), stmt)

	coloured, colourBuf := pretty(true)
	coloured.Debug("query", "sql", stmt)
	require.Contains(t, colourBuf.String(), ansiBold+ansiBlue+"SELECT"+ansiReset)
	assert.Contains(t, stripANSI(colourBuf.String()), stmt, "the statement survives")
}

// A stack is raw for the same reason SQL is, but it is not SQL.
func TestPretty_stackIsNotSQLHighlighted(t *testing.T) {
	log, buf := pretty(true)
	log.Error("boom", "stack", "main.run\n\tmain.go:12")

	assert.NotContains(t, buf.String(), ansiBlue, "a stack trace is left alone")
}

func TestPretty_jsonNeverHighlights(t *testing.T) {
	env := mustEnv(t, map[string]string{"ENV": "dev", "LOG_SIMPLE": "false"})
	t.Setenv("LOG_COLOR", "true")

	var buf strings.Builder
	NewLoggerTo(&buf, env).Debug("query", "sql", "SELECT 1")

	assert.NotContains(t, buf.String(), "\x1b[")
}

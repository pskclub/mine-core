package core

import "strings"

// Highlighting for the sql= value of a log line.
//
// A statement is the longest thing on a query line and the part the eye actually
// has to parse. Colouring its three kinds of token — what the statement *does*,
// what it *contains*, and the names it uses — turns a wall of text into
// something scannable without changing a character of it.
//
// Deliberately restrained: keywords, literals and numbers, nothing else. The
// point is to find the shape of a query at a glance, not to reproduce an IDE.
// Identifiers stay plain because they are the nouns you are reading for.

const ansiBlue = "\x1b[34m"

// sqlKeywords is matched case-insensitively. It covers the statements and
// clauses a service actually emits; an unknown word simply stays plain, which is
// the right failure — a missing highlight is invisible, a wrong one misleads.
var sqlKeywords = map[string]bool{
	// statements
	"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true, "WITH": true,
	"CREATE": true, "ALTER": true, "DROP": true, "TRUNCATE": true, "EXPLAIN": true,
	// clauses
	"FROM": true, "WHERE": true, "SET": true, "VALUES": true, "INTO": true,
	"ORDER": true, "GROUP": true, "HAVING": true, "BY": true, "LIMIT": true,
	"OFFSET": true, "RETURNING": true, "DISTINCT": true, "AS": true,
	// joins
	"JOIN": true, "LEFT": true, "RIGHT": true, "INNER": true, "OUTER": true,
	"FULL": true, "CROSS": true, "ON": true, "USING": true,
	// predicates
	"AND": true, "OR": true, "NOT": true, "IN": true, "EXISTS": true,
	"BETWEEN": true, "LIKE": true, "ILIKE": true, "IS": true, "NULL": true,
	"ASC": true, "DESC": true, "CASE": true, "WHEN": true, "THEN": true,
	"ELSE": true, "END": true,
	// transactions and conflict handling
	"BEGIN": true, "COMMIT": true, "ROLLBACK": true, "SAVEPOINT": true,
	"CONFLICT": true, "DO": true, "NOTHING": true, "DUPLICATE": true,
	// misc
	"TRUE": true, "FALSE": true, "UNION": true, "ALL": true, "COUNT": true,
	"FOR": true, "SHARE": true, "LOCK": true,
}

// highlightSQL colours a statement for a terminal.
//
// Every byte of the input reaches the output; only escape sequences are added.
// Callers must apply it for colour output only — the plain form has to stay
// exactly what the database received, or a log becomes useless for copying a
// query out of.
func highlightSQL(sql string) string {
	var b strings.Builder
	b.Grow(len(sql) + len(sql)/3)

	for i := 0; i < len(sql); {
		switch c := sql[i]; {
		case c == '\'':
			// a string literal, up to the closing quote; '' is an escaped quote
			// and does not end it
			j := i + 1
			for j < len(sql) {
				if sql[j] != '\'' {
					j++
					continue
				}
				if j+1 < len(sql) && sql[j+1] == '\'' {
					j += 2
					continue
				}
				j++
				break
			}
			b.WriteString(ansiGreen)
			b.WriteString(sql[i:j])
			b.WriteString(ansiReset)
			i = j

		case c == '"' || c == '`':
			// a quoted identifier: left plain, because names are what the reader
			// is looking for and colouring them competes with the keywords
			j := i + 1
			for j < len(sql) && sql[j] != c {
				j++
			}
			if j < len(sql) {
				j++ // the closing quote
			}
			b.WriteString(sql[i:j])
			i = j

		case isDigit(c):
			j := i
			for j < len(sql) && (isDigit(sql[j]) || sql[j] == '.') {
				j++
			}
			b.WriteString(ansiCyan)
			b.WriteString(sql[i:j])
			b.WriteString(ansiReset)
			i = j

		case isWordByte(c):
			j := i
			for j < len(sql) && (isWordByte(sql[j]) || isDigit(sql[j])) {
				j++
			}
			word := sql[i:j]
			if sqlKeywords[strings.ToUpper(word)] {
				b.WriteString(ansiBold)
				b.WriteString(ansiBlue)
				b.WriteString(word)
				b.WriteString(ansiReset)
			} else {
				b.WriteString(word)
			}
			i = j

		default:
			b.WriteByte(c)
			i++
		}
	}

	return b.String()
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isWordByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

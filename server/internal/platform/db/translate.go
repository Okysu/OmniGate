package db

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// translateCache memoizes translated statements (store SQL is a small, fixed set).
var translateCache sync.Map // string -> string

// translateSQLite rewrites a PostgreSQL-flavoured statement for SQLite:
//
//   - $N placeholders → ?N (repeated and out-of-order references keep working);
//   - "x = ANY($N)" → "x IN (SELECT value FROM json_each(?N))" (the slice
//     argument is bound as a JSON array);
//   - casts (::type, ::type[]) are dropped;
//   - FOR UPDATE / FOR SHARE [OF t] are dropped (every SQLite transaction
//     holds the database write lock);
//   - now() → the current UTC time in the canonical timestamp format;
//   - GREATEST/LEAST → the multi-argument max/min; jsonb_* → json_*.
//
// String literals, quoted identifiers and comments are left untouched.
func translateSQLite(q string) string {
	if v, ok := translateCache.Load(q); ok {
		return v.(string)
	}
	out := translateUncached(q)
	translateCache.Store(q, out)
	return out
}

var (
	reAny       = regexp.MustCompile(`(?i)=\s*ANY\s*\(\s*\$(\d+)\s*(?:::\s*[a-z_]+\s*\[\])?\s*\)`)
	reCast      = regexp.MustCompile(`(?i)::\s*[a-z_][a-z0-9_]*(?:\s*\[\])?`)
	reLock      = regexp.MustCompile(`(?i)\bFOR\s+(?:UPDATE|SHARE|NO\s+KEY\s+UPDATE|KEY\s+SHARE)(?:\s+OF\s+[a-z_][a-z0-9_]*(?:\s*,\s*[a-z_][a-z0-9_]*)*)?(?:\s+(?:NOWAIT|SKIP\s+LOCKED))?`)
	reNow       = regexp.MustCompile(`(?i)\bnow\s*\(\s*\)`)
	reGreatest  = regexp.MustCompile(`(?i)\bGREATEST\s*\(`)
	reLeast     = regexp.MustCompile(`(?i)\bLEAST\s*\(`)
	reJSONB     = regexp.MustCompile(`(?i)\bjsonb_(array_length|each|typeof|extract_path)\s*\(`)
	rePlacehold = regexp.MustCompile(`\$(\d+)`)
)

func translateUncached(q string) string {
	code, lits := maskLiterals(q)
	code = reAny.ReplaceAllString(code, "IN (SELECT value FROM json_each($$$1))")
	code = reCast.ReplaceAllString(code, "")
	code = reLock.ReplaceAllString(code, "")
	code = reNow.ReplaceAllString(code, sqliteNow)
	code = reGreatest.ReplaceAllString(code, "max(")
	code = reLeast.ReplaceAllString(code, "min(")
	code = reJSONB.ReplaceAllString(code, "json_$1(")
	code = rePlacehold.ReplaceAllString(code, "?$1")
	return unmaskLiterals(code, lits)
}

// maskLiterals replaces string literals, quoted identifiers and comments with
// \x00<index>\x00 markers so the rewrites above only see SQL code.
func maskLiterals(q string) (string, []string) {
	var b strings.Builder
	var lits []string
	mark := func(s string) {
		b.WriteByte(0)
		b.WriteString(strconv.Itoa(len(lits)))
		b.WriteByte(0)
		lits = append(lits, s)
	}
	for i := 0; i < len(q); {
		c := q[i]
		switch {
		case c == '\'' || c == '"':
			j := i + 1
			for j < len(q) {
				if q[j] == c {
					if j+1 < len(q) && q[j+1] == c { // doubled quote escape
						j += 2
						continue
					}
					break
				}
				j++
			}
			end := min(j+1, len(q))
			mark(q[i:end])
			i = end
		case c == '-' && i+1 < len(q) && q[i+1] == '-':
			j := strings.IndexByte(q[i:], '\n')
			if j < 0 {
				j = len(q) - i
			}
			mark(q[i : i+j])
			i += j
		case c == '/' && i+1 < len(q) && q[i+1] == '*':
			j := strings.Index(q[i+2:], "*/")
			end := len(q)
			if j >= 0 {
				end = i + 2 + j + 2
			}
			mark(q[i:end])
			i = end
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), lits
}

func unmaskLiterals(code string, lits []string) string {
	if len(lits) == 0 {
		return code
	}
	var b strings.Builder
	for i := 0; i < len(code); {
		if code[i] != 0 {
			b.WriteByte(code[i])
			i++
			continue
		}
		j := strings.IndexByte(code[i+1:], 0)
		n, _ := strconv.Atoi(code[i+1 : i+1+j])
		b.WriteString(lits[n])
		i += j + 2
	}
	return b.String()
}

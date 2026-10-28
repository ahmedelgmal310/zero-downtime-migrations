// Package names holds the single definition of how a full name maps to the
// split first_name/last_name columns. The SQL trigger and the backfill use the
// same rule (split on the first space), so every code path agrees on the data.
package names

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxLen is the longest full name the API accepts, in bytes.
const MaxLen = 200

// ErrInvalid is returned by Validate for names the API rejects.
var ErrInvalid = errors.New("name must be 1-200 bytes of valid UTF-8 with no leading or trailing whitespace")

// Split breaks a full name on its first space: "Mary Ann Smith" becomes
// ("Mary", "Ann Smith") and a single word becomes (word, "").
// It mirrors the SQL used by the expand trigger and the backfill:
//
//	split_part(name, ' ', 1)
//	CASE WHEN strpos(name, ' ') > 0 THEN substr(name, strpos(name, ' ') + 1) ELSE '' END
func Split(full string) (first, last string) {
	first, last, _ = strings.Cut(full, " ")
	return first, last
}

// Join is the inverse of Split for every validated name.
func Join(first, last string) string {
	if last == "" {
		return first
	}
	return first + " " + last
}

// Validate reports whether a full name is accepted by the API. Rejecting
// leading and trailing whitespace keeps Join(Split(n)) == n for every stored row.
func Validate(full string) error {
	if full == "" || len(full) > MaxLen || !utf8.ValidString(full) || strings.ContainsRune(full, 0) {
		return ErrInvalid
	}
	first, _ := utf8.DecodeRuneInString(full)
	last, _ := utf8.DecodeLastRuneInString(full)
	if unicode.IsSpace(first) || unicode.IsSpace(last) {
		return ErrInvalid
	}
	return nil
}

package labstatus

import (
	"errors"
	"strings"
	"unicode/utf8"
)

// ValidateStatementForm rejects the captured subject-only failure and malformed
// text without rewriting it. This is a narrow form check, not a grammar test or
// proof of completeness, source support, or agreement with classified fields.
// Historical checkpoint readers must select their original version's rules.
func ValidateStatementForm(record Record) error {
	if record.Statement == "" || len(record.Statement) > maxTextLength ||
		!utf8.ValidString(record.Statement) || strings.ContainsRune(record.Statement, 0) ||
		strings.TrimSpace(record.Statement) != record.Statement {
		return errors.New("candidate statement must be bounded exact text without surrounding whitespace")
	}
	if strings.EqualFold(record.Statement, strings.TrimSpace(record.Subject)) {
		return errors.New("candidate statement repeats its subject instead of expressing a proposition")
	}
	return nil
}

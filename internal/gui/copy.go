//go:build gui

package gui

import (
	"strings"
	"unicode"
)

// Alert copy grammar:
//   - a title is a short sentence-case outcome with no terminal period;
//   - a body is one full sentence, capitalised and closed with punctuation;
//   - a routine success with nothing to add shows the title only.
//
// alertBody is the single boundary that enforces this, so backend strings and
// call sites cannot drift apart.

// genericBodies are backend confirmations that only restate an action. They
// never become a toast body: the title already says the same thing.
var genericBodies = map[string]bool{
	"done":                                      true,
	"failed":                                    true,
	"error":                                     true,
	"action failed":                             true,
	"the change could not be applied":           true,
	"config applied":                            true,
	"config saved":                              true,
	"config unchanged":                          true,
	"service started":                           true,
	"service stopped":                           true,
	"service installed":                         true,
	"interception enabled":                      true,
	"interception restored":                     true,
	"lists refreshed":                           true,
	"dns cache cleared":                         true,
	"windows dns cache cleared":                 true,
	"service could not be installed":            true,
	"service could not be stopped":              true,
	"could not clear the windows dns cache":     true,
	"could not remove the service registration": true,
}

// alertBody returns the body to show under title, or "" when it adds nothing:
// empty, punctuation-only, a restatement of the title, or a generic backend
// confirmation. Anything else is normalised into one sentence.
func alertBody(title, body string) string {
	s := sentence(body)
	if s == "" || !hasLetter(s) || sameMessage(title, s) || genericBody(s) {
		return ""
	}

	return capBody(s)
}

// sentence trims and collapses whitespace, capitalises the first letter, and
// closes the text with terminal punctuation unless it already has one.  A
// leading URL keeps its scheme lowercased; a leading quote or bracket is
// skipped to the first letter instead of being uppercased itself.
func sentence(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return ""
	}

	r := []rune(s)
	if !startsWithURL(s) {
		for i, c := range r {
			if unicode.IsLetter(c) {
				r[i] = unicode.ToUpper(c)

				break
			}
		}
		s = string(r)
	}

	if last := r[len(r)-1]; !strings.ContainsRune(".?!\u2026", last) {
		s += "."
	}

	return s
}

// startsWithURL reports a scheme at the very start, so sentence does not turn
// "https://…" into "Https://…".
func startsWithURL(s string) bool {
	end := strings.Index(s, "://")
	if end <= 0 {
		return false
	}
	for _, r := range s[:end] {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '+' && r != '-' && r != '.' {
			return false
		}
	}

	return true
}

// sameMessage reports whether two messages say the same thing, ignoring case,
// spacing, and terminal punctuation.
func sameMessage(a, b string) bool {
	na, nb := normaliseMessage(a), normaliseMessage(b)

	return na != "" && na == nb
}

// genericBody reports whether a normalised body is backend jargon.
func genericBody(s string) bool {
	n := normaliseMessage(s)
	if genericBodies[n] {
		return true
	}

	// The uninstall helper appends its restore plan in parentheses; the title
	// "Service removed" already carries the outcome.
	return strings.HasPrefix(n, "service uninstalled (")
}

// normaliseMessage lowercases and drops terminal punctuation for comparison.
func normaliseMessage(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))

	return strings.TrimRight(s, ".?!\u2026")
}

// hasLetter rejects punctuation-only strings so they never render as a body.
func hasLetter(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) {
			return true
		}
	}

	return false
}

// capBody limits a body to 200 characters, never splitting a rune, and marks
// the truncation with an ellipsis.
func capBody(s string) string {
	const maxBody = 200

	r := []rune(s)
	if len(r) <= maxBody {
		return s
	}

	return strings.TrimSpace(string(r[:maxBody])) + "\u2026"
}

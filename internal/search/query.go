package search

import (
	"fmt"
	"strings"
)

// The query language, and the reasoning behind keeping it small.
//
//	golang concurrency      both terms, ranked (an implicit OR that prefers AND)
//	"exact phrase"          the words, adjacent, in order
//	+required               the document must contain this
//	-excluded               the document must not contain this
//	title:golang            scoped to one field
//
// Everything here exists because a user typed it and meant something specific.
// A full boolean grammar with parentheses and precedence is easy to add and
// almost nobody uses it correctly -- the prefix operators cover what people
// actually reach for, and they cannot be nested wrongly.

// Occur says how a clause participates in matching.
type Occur int

const (
	// Should: contributes to the score, not required. The default.
	Should Occur = iota
	// Must: the document has to contain it.
	Must
	// MustNot: the document must not.
	MustNot
)

// Clause is one component of a query.
type Clause struct {
	Occur Occur
	// Field scopes the clause, empty meaning "anywhere".
	Field string
	// Terms holds one term, or several for a phrase.
	Terms []string
	// Phrase requires the terms to be adjacent and in order.
	Phrase bool
	// Raw is what the user typed, kept for display and highlighting.
	Raw string
}

// Query is a parsed search.
type Query struct {
	Clauses []Clause
	Raw     string
}

// IsEmpty reports whether the query would match nothing meaningful.
func (q *Query) IsEmpty() bool {
	for _, c := range q.Clauses {
		if c.Occur != MustNot && len(c.Terms) > 0 {
			return false
		}
	}
	return true
}

// TermsFor returns every positive term, for snippet highlighting.
func (q *Query) TermsFor() []string {
	var out []string
	for _, c := range q.Clauses {
		if c.Occur == MustNot {
			continue
		}
		out = append(out, c.Terms...)
	}
	return out
}

// analyzerFunc turns raw text into index terms. It is passed in rather than
// constructed so the query goes through exactly the same pipeline the
// documents did -- a query analysed differently cannot match anything.
type analyzerFunc func(string) []string

// ParseQuery parses a query string.
//
// The parser is a small scanner rather than a grammar, because the language has
// no nesting. It walks the input once, peeling off a prefix operator and a
// field qualifier, then reading either a quoted phrase or a bare term.
func ParseQuery(raw string, analyze analyzerFunc) (*Query, error) {
	query := &Query{Raw: raw}
	runes := []rune(raw)
	i := 0

	for i < len(runes) {
		// Skip whitespace.
		for i < len(runes) && isSpace(runes[i]) {
			i++
		}
		if i >= len(runes) {
			break
		}

		clause := Clause{Occur: Should}
		start := i

		// A leading + or - sets how the clause must occur.
		switch runes[i] {
		case '+':
			clause.Occur = Must
			i++
		case '-':
			// A bare '-' with nothing after it is just a stray character.
			if i+1 < len(runes) && !isSpace(runes[i+1]) {
				clause.Occur = MustNot
				i++
			}
		}
		if i >= len(runes) {
			break
		}

		// A field qualifier: letters followed by a colon, and then something.
		if field, consumed := scanField(runes[i:]); field != "" {
			clause.Field = field
			i += consumed
		}
		if i >= len(runes) {
			break
		}

		if runes[i] == '"' {
			// A quoted phrase runs to the closing quote, or to the end of the
			// input if the user never typed one -- refusing an unbalanced
			// quote would be pedantic when the intent is obvious.
			i++
			phraseStart := i
			for i < len(runes) && runes[i] != '"' {
				i++
			}
			text := string(runes[phraseStart:i])
			if i < len(runes) {
				i++ // closing quote
			}
			clause.Phrase = true
			clause.Terms = analyze(text)
			clause.Raw = text
		} else {
			termStart := i
			for i < len(runes) && !isSpace(runes[i]) {
				i++
			}
			text := string(runes[termStart:i])
			clause.Terms = analyze(text)
			clause.Raw = text
			// A single word can analyse to several terms -- "state-of-the-art"
			// does -- and treating those as a phrase is what the user meant.
			if len(clause.Terms) > 1 {
				clause.Phrase = true
			}
		}

		// A clause whose text was entirely stopwords analyses to nothing.
		// Dropping it silently is right: the alternative is a query for "the"
		// returning an error rather than results.
		if len(clause.Terms) == 0 {
			if i == start {
				i++ // guarantee progress, so a stray character cannot loop
			}
			continue
		}
		query.Clauses = append(query.Clauses, clause)
	}

	if len(query.Clauses) == 0 && strings.TrimSpace(raw) != "" {
		return query, nil // every term was a stopword; not an error
	}
	return query, nil
}

// scanField recognises a `field:` prefix, returning the name and how many
// runes it consumed.
func scanField(runes []rune) (string, int) {
	for i, r := range runes {
		if r == ':' {
			// A colon at the start, or with nothing after it, is not a field.
			if i == 0 || i+1 >= len(runes) || isSpace(runes[i+1]) {
				return "", 0
			}
			name := strings.ToLower(string(runes[:i]))
			// Only known fields, so a URL like "http://x" is not read as a
			// field qualifier named "http".
			switch name {
			case "title", "body", "uri":
				return name, i + 1
			}
			return "", 0
		}
		if isSpace(r) || r == '"' {
			return "", 0
		}
	}
	return "", 0
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

// String renders a query back, for logging and for the explain output.
func (q *Query) String() string {
	parts := make([]string, 0, len(q.Clauses))
	for _, c := range q.Clauses {
		var b strings.Builder
		switch c.Occur {
		case Must:
			b.WriteString("+")
		case MustNot:
			b.WriteString("-")
		}
		if c.Field != "" {
			b.WriteString(c.Field + ":")
		}
		if c.Phrase {
			b.WriteString(fmt.Sprintf("%q", strings.Join(c.Terms, " ")))
		} else {
			b.WriteString(strings.Join(c.Terms, " "))
		}
		parts = append(parts, b.String())
	}
	return strings.Join(parts, " ")
}

// indexTerm builds the term as stored, honouring any field scope.
func (c Clause) indexTerm(term string) string {
	if c.Field == "" || c.Field == "uri" {
		return term
	}
	return c.Field + ":" + term
}

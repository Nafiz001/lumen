// Package analysis turns text into the terms an index actually stores.
//
// This is the least glamorous part of a search engine and the one that decides
// most of its quality. Everything downstream -- the index, the ranking, the
// query -- operates on whatever comes out of here, so a document and a query
// only ever match if both were analysed the same way. A tokenizer that splits
// "e-mail" differently from the query parser produces a search engine that
// cannot find its own documents, and nothing further down can repair that.
//
// The pipeline is: split into tokens, lowercase, drop stopwords, reduce to
// stems. Each stage throws information away on purpose, and each is a trade of
// precision for recall.
package analysis

import (
	"strings"
	"unicode"
)

// Token is one term with the position it occupied in the source text.
//
// Position is what makes phrase search possible. Without it the index can only
// answer "do these words appear in this document"; with it, it can answer "do
// they appear next to each other", which is what a user typing a quoted phrase
// actually means.
type Token struct {
	Term string
	// Position is the token's ordinal in the document, counted before
	// stopwords are removed so that phrase distances stay honest.
	Position int
	// Start and End locate the token in the original text, for highlighting
	// snippets without re-tokenising.
	Start, End int
}

// Analyzer converts text to tokens.
type Analyzer struct {
	stopwords map[string]bool
	stem      bool
	minLength int
	maxLength int
}

// Option configures an Analyzer.
type Option func(*Analyzer)

// WithStopwords replaces the stopword list. An empty list disables removal.
func WithStopwords(words []string) Option {
	return func(a *Analyzer) {
		a.stopwords = make(map[string]bool, len(words))
		for _, w := range words {
			a.stopwords[strings.ToLower(w)] = true
		}
	}
}

// WithStemming turns stemming on or off.
func WithStemming(on bool) Option {
	return func(a *Analyzer) { a.stem = on }
}

// New builds an analyzer with sensible defaults.
func New(opts ...Option) *Analyzer {
	a := &Analyzer{
		stopwords: defaultStopwords(),
		stem:      true,
		minLength: 1,
		// A cap on term length. Real text does not contain 200-character
		// words, but minified JavaScript and base64 blobs do, and without a
		// limit they become index terms nobody will ever search for.
		maxLength: 64,
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Analyze runs the full pipeline over a document.
func (a *Analyzer) Analyze(text string) []Token {
	tokens := a.Tokenize(text)
	out := tokens[:0]

	for _, tok := range tokens {
		term := strings.ToLower(tok.Term)
		if len(term) < a.minLength || len(term) > a.maxLength {
			continue
		}
		// Stopwords are dropped after lowercasing but before stemming, so
		// "The" and "the" are both caught.
		if a.stopwords[term] {
			continue
		}
		if a.stem {
			term = Stem(term)
		}
		if term == "" {
			continue
		}
		tok.Term = term
		out = append(out, tok)
	}
	return out
}

// AnalyzeTerms returns just the terms, for callers that do not need positions.
func (a *Analyzer) AnalyzeTerms(text string) []string {
	tokens := a.Analyze(text)
	terms := make([]string, len(tokens))
	for i, t := range tokens {
		terms[i] = t.Term
	}
	return terms
}

// Tokenize splits text into raw tokens without lowercasing or stemming.
//
// The rule is: a token is a run of letters and digits, with an apostrophe or a
// hyphen allowed inside a word but not at its edge. That keeps "don't" and
// "state-of-the-art" together while splitting on real punctuation. It is a
// judgement call rather than a standard -- there is no correct answer, only
// one that has to be applied identically to documents and queries.
func (a *Analyzer) Tokenize(text string) []Token {
	var tokens []Token
	runes := []rune(text)
	position := 0

	i := 0
	for i < len(runes) {
		if !isWordRune(runes[i]) {
			i++
			continue
		}

		start := i
		for i < len(runes) {
			if isWordRune(runes[i]) {
				i++
				continue
			}
			// An internal apostrophe or hyphen continues the token only when a
			// word character follows it.
			if (runes[i] == '\'' || runes[i] == '’' || runes[i] == '-') &&
				i+1 < len(runes) && isWordRune(runes[i+1]) {
				i++
				continue
			}
			break
		}

		tokens = append(tokens, Token{
			Term:     string(runes[start:i]),
			Position: position,
			Start:    start,
			End:      i,
		})
		position++
	}
	return tokens
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

// defaultStopwords is a small English list.
//
// Stopwords are a trade, not a free win. Dropping "the" saves a great deal of
// index space, because it appears in nearly every document and its postings
// list is therefore the longest one there is -- while telling you almost
// nothing about which document you want. But dropping them makes some queries
// unanswerable: "The Who", "to be or not to be", "The Office" all become empty.
//
// The list is kept deliberately short for that reason. BM25 already discounts
// common terms heavily through inverse document frequency, so the ranking
// barely needs the help; the space saving is the real argument.
func defaultStopwords() map[string]bool {
	words := []string{
		"a", "an", "and", "are", "as", "at", "be", "but", "by",
		"for", "if", "in", "into", "is", "it", "no", "not", "of",
		"on", "or", "such", "that", "the", "their", "then", "there",
		"these", "they", "this", "to", "was", "will", "with",
	}
	set := make(map[string]bool, len(words))
	for _, w := range words {
		set[w] = true
	}
	return set
}

// IsStopword reports whether a term is in the analyzer's stoplist.
func (a *Analyzer) IsStopword(term string) bool {
	return a.stopwords[strings.ToLower(term)]
}

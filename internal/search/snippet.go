package search

import (
	"html"
	"strings"

	"github.com/nafizahmed/lumen/internal/analysis"
	"github.com/nafizahmed/lumen/internal/index"
)

// Snippets: the short excerpt shown under each result.
//
// This is not decoration. A result list without snippets forces the user to
// open every link to find out whether it is the one they wanted, which is
// exactly the work a search engine exists to save. A good snippet answers "why
// did this match" before the user clicks.
//
// The approach here is to find the densest window of query terms in the
// document and show that, rather than the first hundred characters -- the
// opening of a document is very often boilerplate that says nothing about why
// it matched.

const (
	// snippetPadding is how much context to keep around the matched window.
	snippetPadding = 60
	// highlightOpen and highlightClose mark matched terms. They are markers
	// rather than HTML so the caller decides how to render them, and so the
	// terminal and the web UI can differ.
	highlightOpen  = "\x02"
	highlightClose = "\x03"
)

// snippet builds an excerpt of a document around its best match.
func (s *Searcher) snippet(doc *index.Document, terms []string, maxLength int) string {
	text := doc.Body
	if strings.TrimSpace(text) == "" {
		text = doc.Title
	}
	if text == "" {
		return ""
	}
	if len(terms) == 0 {
		return truncate(text, maxLength)
	}

	// The document is re-tokenised here rather than reusing the index's
	// positions, because a snippet needs character offsets into the original
	// text and the index stores token ordinals. Re-tokenising one document at
	// display time is cheap; storing offsets for every token of every document
	// would be a large index for a small convenience.
	analyzer := s.idx.Analyzer()
	tokens := analyzer.Tokenize(text)
	if len(tokens) == 0 {
		return truncate(text, maxLength)
	}

	wanted := make(map[string]bool, len(terms))
	for _, t := range terms {
		wanted[t] = true
	}

	// Which tokens match, by index into the token slice.
	var matches []int
	for i, tok := range tokens {
		stemmed := analyzer.AnalyzeTerms(tok.Term)
		for _, st := range stemmed {
			if wanted[st] {
				matches = append(matches, i)
				break
			}
		}
	}
	if len(matches) == 0 {
		return truncate(text, maxLength)
	}

	best := densestWindow(matches, tokens, maxLength)
	return buildSnippet(text, tokens, matches, best, maxLength)
}

// densestWindow finds the match index whose surrounding window holds the most
// matches, so the snippet shows where the query terms cluster.
func densestWindow(matches []int, tokens []analysisToken, maxLength int) int {
	if len(matches) == 1 {
		return matches[0]
	}

	bestStart := matches[0]
	bestCount := 0

	for _, start := range matches {
		windowEnd := tokens[start].Start + maxLength
		count := 0
		for _, m := range matches {
			if tokens[m].Start >= tokens[start].Start && tokens[m].End <= windowEnd {
				count++
			}
		}
		if count > bestCount {
			bestCount = count
			bestStart = start
		}
	}
	return bestStart
}

// buildSnippet cuts the excerpt and marks the matched terms inside it.
func buildSnippet(text string, tokens []analysisToken, matches []int, anchor, maxLength int) string {
	runes := []rune(text)

	start := tokens[anchor].Start - snippetPadding
	if start < 0 {
		start = 0
	}
	end := start + maxLength
	if end > len(runes) {
		end = len(runes)
		start = end - maxLength
		if start < 0 {
			start = 0
		}
	}

	// Trim to word boundaries, so the snippet does not begin or end mid-word.
	if start > 0 {
		for start < len(runes) && !isSpace(runes[start]) {
			start++
		}
		for start < len(runes) && isSpace(runes[start]) {
			start++
		}
	}
	if end < len(runes) {
		for end > start && !isSpace(runes[end-1]) {
			end--
		}
	}
	if start >= end {
		return truncate(text, maxLength)
	}

	// Mark matches that fall inside the window, walking backwards so earlier
	// offsets stay valid as markers are inserted.
	var inWindow []analysisToken
	for _, m := range matches {
		tok := tokens[m]
		if tok.Start >= start && tok.End <= end {
			inWindow = append(inWindow, tok)
		}
	}

	out := make([]rune, 0, end-start+len(inWindow)*2)
	cursor := start
	for _, tok := range inWindow {
		if tok.Start < cursor {
			continue // overlapping match, already covered
		}
		out = append(out, runes[cursor:tok.Start]...)
		out = append(out, []rune(highlightOpen)...)
		out = append(out, runes[tok.Start:tok.End]...)
		out = append(out, []rune(highlightClose)...)
		cursor = tok.End
	}
	out = append(out, runes[cursor:end]...)

	snippet := string(out)
	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(runes) {
		snippet = snippet + "..."
	}
	return strings.Join(strings.Fields(snippet), " ")
}

func truncate(text string, maxLength int) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= maxLength {
		return text
	}
	cut := maxLength
	for cut > 0 && !isSpace(runes[cut]) {
		cut--
	}
	if cut == 0 {
		cut = maxLength
	}
	return string(runes[:cut]) + "..."
}

// RenderSnippet converts the internal highlight markers into a chosen pair.
//
// The markers are control characters so they cannot occur in real text, which
// means a document containing "<b>" cannot fake a highlight, and the caller can
// render into HTML or ANSI without the snippet builder knowing which.
func RenderSnippet(snippet, open, close string) string {
	snippet = strings.ReplaceAll(snippet, highlightOpen, open)
	return strings.ReplaceAll(snippet, highlightClose, close)
}

// RenderSnippetHTML escapes the snippet for HTML, then turns the markers into
// <mark> tags.
//
// The order matters and is the whole point of using control characters as
// markers. Escaping happens first, so any markup in the indexed document is
// neutralised; the tags are introduced afterwards, from characters the escape
// pass left alone and that no real document can contain. A document whose body
// literally reads "<mark>" therefore renders as text, and cannot extend a
// highlight over the rest of the page.
func RenderSnippetHTML(snippet string) string {
	return RenderSnippet(html.EscapeString(snippet), "<mark>", "</mark>")
}

// StripHighlights removes the markers entirely.
func StripHighlights(snippet string) string {
	return RenderSnippet(snippet, "", "")
}

// analysisToken aliases the analyzer's token so the snippet code reads without
// a package qualifier on every line.
type analysisToken = analysis.Token

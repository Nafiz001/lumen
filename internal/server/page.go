package server

import (
	"fmt"
	"html"
	"html/template"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/Nafiz001/lumen/internal/search"
)

// The search page. It is built from a view model and an html/template, so
// every value is escaped by the template and the one piece of markup that
// is not text, the snippet with its highlights, is marked safe in exactly one
// place (see snippetHTML).

const perPage = 10

// termColors is how many colours the term legend cycles through.
const termColors = 4

type pageData struct {
	Style    template.CSS
	Query    string
	Title    string
	Docs     int
	Terms    int
	Postings int

	Home  bool
	Error string
	Empty bool

	Total      int
	Took       string
	Parsed     string
	ShowParsed bool
	Legend     []legendTerm
	Hits       []hitView

	Pages []pageLink
	Prev  string
	Next  string

	Examples []example
	Syntax   []example
	Steps    []step
}

type legendTerm struct {
	Term  string
	Color int
	Docs  int
}

type hitView struct {
	Rank    int
	Title   string
	URI     string
	Link    bool
	Path    []string
	Snippet template.HTML
	Score   string
	Bar     []segment
	Rows    []explainRow
	Also    []alsoView
}

type segment struct {
	Width float64
	Color int // 0..termColors-1, or -1 for a boost that belongs to no single term
	Label string
}

type explainRow struct {
	Term  string
	Color int
	TF    uint32
	DF    int
	IDF   string
	Adds  string
}

type alsoView struct {
	URI  string
	Path string
}

type pageLink struct {
	N       int
	Href    string
	Current bool
	Gap     bool
}

type example struct {
	Query string
	Note  string
}

type step struct {
	Name string
	Text string
}

var homeExamples = []example{
	{"goroutines channels", "two words, ranked by relevance"},
	{`"memory model"`, "the words adjacent and in order"},
	{"generics +constraints", "this word is required"},
	{"context -deprecated", "this word must not appear"},
	{"title:fuzzing", "only in the title"},
}

var homeSteps = []step{
	{"Analyse", "Text is split into words, lower-cased, stripped of stopwords and stemmed, so running finds runs."},
	{"Index", "An inverted index keeps every position of every word, delta and varint compressed, so phrases can be matched."},
	{"Rank", "Okapi BM25 scores each document by how rare and how frequent your words are, with a boost for titles and phrases."},
	{"Show", "The best passage is cut around the matches and lit. Open any result to see the numbers behind its score."},
}

func (s *Server) buildPage(query string, page int, results *search.Results, elapsed time.Duration, searchErr error) pageData {
	stats := s.idx.Stats()
	d := pageData{
		Style:    template.CSS(pageStyle),
		Query:    query,
		Title:    "lumen, a search engine written from scratch",
		Docs:     stats.Documents,
		Terms:    stats.Terms,
		Postings: stats.Postings,
		Examples: homeExamples,
		Syntax:   homeExamples,
		Steps:    homeSteps,
	}
	if query != "" {
		d.Title = query + " - lumen"
	}

	switch {
	case searchErr != nil:
		d.Error = searchErr.Error()
	case query == "":
		d.Home = true
	case results == nil || len(results.Hits) == 0:
		d.Empty = true
	default:
		d.Total = results.Total
		d.Took = fmt.Sprintf("%.1f", float64(elapsed.Microseconds())/1000)
		d.Parsed = results.Parsed
		d.ShowParsed = results.Parsed != query
		d.Hits, d.Legend = buildHits(results, page)
		d.Pages, d.Prev, d.Next = buildPager(query, page, results.Total)
	}
	return d
}

// buildHits turns ranked results into what the page shows: duplicates merged,
// the site name that every title ends with removed, and each score drawn as a
// bar of light, one segment per query term.
func buildHits(results *search.Results, page int) ([]hitView, []legendTerm) {
	hits := results.Hits
	suffix := commonTitleSuffix(hits)

	var maxScore float64
	for _, h := range hits {
		maxScore = math.Max(maxScore, h.Score)
	}

	// Terms get a colour in the order they are first seen, and the legend
	// remembers how many documents contain each one: the rarer a word, the
	// more it moves a score.
	colour := map[string]int{}
	var legend []legendTerm
	colourOf := func(term string, docs int) int {
		if c, ok := colour[term]; ok {
			return c
		}
		c := len(colour) % termColors
		colour[term] = c
		legend = append(legend, legendTerm{Term: term, Color: c, Docs: docs})
		return c
	}

	var views []hitView
	seen := map[string]int{} // duplicate key -> index into views
	for i, h := range hits {
		snippet := snippetHTML(h.Snippet)
		key := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(displayTitle(h), suffix))) + "|" + snippetKey(h.Snippet)
		if at, dup := seen[key]; dup {
			views[at].Also = append(views[at].Also, alsoView{URI: h.URI, Path: strings.Join(uriPath(h.URI), " / ")})
			continue
		}
		seen[key] = len(views)

		v := hitView{
			Rank:    page*perPage + i + 1,
			Title:   strings.TrimSuffix(displayTitle(h), suffix),
			URI:     h.URI,
			Link:    strings.HasPrefix(h.URI, "http://") || strings.HasPrefix(h.URI, "https://"),
			Path:    uriPath(h.URI),
			Snippet: snippet,
			Score:   fmt.Sprintf("%.2f", h.Score),
		}
		var sum float64
		for _, e := range h.Explanations {
			c := colourOf(e.Term, e.DocFrequency)
			sum += e.Contribution
			v.Rows = append(v.Rows, explainRow{
				Term: e.Term, Color: c, TF: e.TermFrequency, DF: e.DocFrequency,
				IDF: fmt.Sprintf("%.2f", e.IDF), Adds: fmt.Sprintf("%.2f", e.Contribution),
			})
			if maxScore > 0 {
				v.Bar = append(v.Bar, segment{Width: e.Contribution / maxScore * 100, Color: c, Label: e.Term})
			}
		}
		// Title and phrase boosts are applied on top of the terms, so they are
		// the part of the score that no single word owns.
		if rest := h.Score - sum; rest > 0.005 && maxScore > 0 {
			v.Bar = append(v.Bar, segment{Width: rest / maxScore * 100, Color: -1, Label: "title and phrase boost"})
			v.Rows = append(v.Rows, explainRow{Term: "title and phrase boost", Color: -1, Adds: fmt.Sprintf("%.2f", rest)})
		}
		views = append(views, v)
	}
	return views, legend
}

// snippetHTML makes a snippet safe to put in the page. The text is unescaped
// once first, because the crawler keeps character references from <pre> blocks
// as literal "&#34;", and escaped again by RenderSnippetHTML before the
// highlight markers become tags, so a document that contains "<script>"
// cannot inject one.
func snippetHTML(raw string) template.HTML {
	return template.HTML(search.RenderSnippetHTML(html.UnescapeString(raw))) //nolint:gosec // escaped inside RenderSnippetHTML
}

// snippetKey identifies a passage for duplicate detection: its words without
// the highlight markers or spacing.
func snippetKey(snippet string) string {
	plain := strings.Join(strings.Fields(search.StripHighlights(snippet)), " ")
	if r := []rune(plain); len(r) > 100 {
		plain = string(r[:100])
	}
	return strings.ToLower(plain)
}

// uriPath splits a page address into the pieces a reader recognises:
// "https://go.dev/blog/slices" becomes go.dev, blog, slices.
func uriPath(uri string) []string {
	u, err := url.Parse(uri)
	if err != nil || u.Host == "" {
		parts := strings.FieldsFunc(uri, func(r rune) bool { return r == '/' || r == '\\' })
		return parts
	}
	out := []string{u.Host}
	for _, p := range strings.Split(strings.Trim(u.Path, "/"), "/") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// commonTitleSuffix finds a site name that most titles on the page end with,
// such as " - The Go Programming Language", so it can be left off. It only
// acts when at least three results share it, so one title that happens to
// contain a dash is left alone.
func commonTitleSuffix(hits []search.Result) string {
	counts := map[string]int{}
	for _, h := range hits {
		t := displayTitle(h)
		for _, sep := range []string{" - ", " | "} {
			if i := strings.LastIndex(t, sep); i > 0 {
				counts[t[i:]]++
			}
		}
	}
	best, n := "", 0
	for s, c := range counts {
		if c > n {
			best, n = s, c
		}
	}
	if n >= 3 && n*2 >= len(hits) {
		return best
	}
	return ""
}

// buildPager returns numbered page links with gaps, plus previous and next.
func buildPager(query string, page, total int) (links []pageLink, prev, next string) {
	pages := (total + perPage - 1) / perPage
	href := func(p int) string { return fmt.Sprintf("/?q=%s&p=%d", url.QueryEscape(query), p) }
	if page > 0 {
		prev = href(page - 1)
	}
	if page+1 < pages {
		next = href(page + 1)
	}
	if pages <= 1 {
		return nil, prev, next
	}
	show := map[int]bool{0: true, pages - 1: true}
	for p := page - 2; p <= page+2; p++ {
		if p >= 0 && p < pages {
			show[p] = true
		}
	}
	last := -1
	for p := 0; p < pages; p++ {
		if !show[p] {
			continue
		}
		if last >= 0 && p > last+1 {
			links = append(links, pageLink{Gap: true})
		}
		links = append(links, pageLink{N: p + 1, Href: href(p), Current: p == page})
		last = p
	}
	return links, prev, next
}

var pageTemplate = template.Must(template.New("page").Funcs(template.FuncMap{
	"join": strings.Join,
	"q":    url.QueryEscape,
	"add":  func(a, b int) int { return a + b },
	"pct":  func(f float64) string { return fmt.Sprintf("%.2f%%", f) },
	"num": func(n int) string {
		s := fmt.Sprint(n)
		for i := len(s) - 3; i > 0; i -= 3 {
			s = s[:i] + "," + s[i:]
		}
		return s
	},
}).Parse(pageHTML))

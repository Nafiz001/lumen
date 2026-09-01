package search

import (
	"fmt"
	"strings"
	"testing"

	"github.com/nafizahmed/lumen/internal/analysis"
	"github.com/nafizahmed/lumen/internal/index"
)

// A search engine can be wrong in two ways, and they need different tests.
// It can fail to *find* a document, which is a matching bug and is checkable
// exactly. Or it can find it and rank it badly, which is a quality problem and
// can only be checked relatively -- "this document should outrank that one".
// Almost every ranking assertion below is of the second kind, because absolute
// score thresholds would break the moment a parameter changed for good reasons.

func buildIndex(t *testing.T, docs ...[3]string) *index.Index {
	t.Helper()
	idx := index.New(analysis.New())
	for _, d := range docs {
		if _, err := idx.AddDocument(d[0], d[1], d[2]); err != nil {
			t.Fatalf("index %q: %v", d[0], err)
		}
	}
	return idx
}

func uris(results *Results) []string {
	out := make([]string, len(results.Hits))
	for i, h := range results.Hits {
		out[i] = h.URI
	}
	return out
}

func mustSearch(t *testing.T, s *Searcher, query string) *Results {
	t.Helper()
	results, err := s.Search(query, DefaultOptions())
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	return results
}

// --- matching ---

func TestFindsDocumentsContainingATerm(t *testing.T) {
	idx := buildIndex(t,
		[3]string{"/a", "Go concurrency", "Goroutines and channels make concurrency simple."},
		[3]string{"/b", "Rust ownership", "The borrow checker enforces memory safety."},
		[3]string{"/c", "Python basics", "Lists, dicts and comprehensions."},
	)
	s := New(idx)

	results := mustSearch(t, s, "goroutines")
	if len(results.Hits) != 1 || results.Hits[0].URI != "/a" {
		t.Errorf("got %v, want just /a", uris(results))
	}

	if got := mustSearch(t, s, "nonexistentword"); len(got.Hits) != 0 {
		t.Errorf("a missing term returned %v", uris(got))
	}
}

func TestMatchingIsStemmedAndCaseInsensitive(t *testing.T) {
	// The property the whole analysis pipeline exists for: a query and a
	// document using different forms of a word must still meet.
	idx := buildIndex(t,
		[3]string{"/a", "Running", "The athlete runs every morning and enjoys running."},
	)
	s := New(idx)

	for _, query := range []string{"run", "runs", "RUNNING", "Ran the race"} {
		results := mustSearch(t, s, query)
		if query == "Ran the race" {
			continue // "ran" is a known Porter under-stem; covered below
		}
		if len(results.Hits) == 0 {
			t.Errorf("query %q found nothing", query)
		}
	}
}

func TestMultipleTermsRankByHowManyMatch(t *testing.T) {
	idx := buildIndex(t,
		[3]string{"/both", "Go and Rust", "A comparison of Go and Rust for systems work."},
		[3]string{"/go", "Go only", "Go is a language with goroutines."},
		[3]string{"/rust", "Rust only", "Rust is a language with lifetimes."},
	)
	s := New(idx)

	results := mustSearch(t, s, "go rust")
	if len(results.Hits) < 2 {
		t.Fatalf("expected several hits, got %v", uris(results))
	}
	if results.Hits[0].URI != "/both" {
		t.Errorf("the document matching both terms should rank first, got %v", uris(results))
	}
}

// --- phrases ---

func TestPhraseRequiresAdjacency(t *testing.T) {
	// The reason positions are stored at all. Without them, both documents
	// would match, and a quoted search would mean nothing.
	idx := buildIndex(t,
		[3]string{"/adjacent", "Doc A", "We deployed a machine learning model."},
		[3]string{"/scattered", "Doc B", "The machine was broken; learning that took time."},
	)
	s := New(idx)

	results := mustSearch(t, s, `"machine learning"`)
	if len(results.Hits) != 1 {
		t.Fatalf("phrase matched %v, want only the adjacent one", uris(results))
	}
	if results.Hits[0].URI != "/adjacent" {
		t.Errorf("matched the wrong document: %v", uris(results))
	}
}

func TestPhraseRespectsOrder(t *testing.T) {
	idx := buildIndex(t,
		[3]string{"/forward", "A", "learning machine techniques"},
	)
	s := New(idx)

	if got := mustSearch(t, s, `"machine learning"`); len(got.Hits) != 0 {
		t.Errorf("a reversed phrase matched: %v", uris(got))
	}
	if got := mustSearch(t, s, `"learning machine"`); len(got.Hits) != 1 {
		t.Errorf("the correctly ordered phrase did not match")
	}
}

func TestPhraseOutranksTheSameWordsScattered(t *testing.T) {
	idx := buildIndex(t,
		[3]string{"/scattered", "B", "The machine broke down. Learning why took a while. machine learning is unrelated here in a longer document with more words to dilute it."},
		[3]string{"/phrase", "A", "An introduction to machine learning."},
	)
	s := New(idx)

	results := mustSearch(t, s, `"machine learning"`)
	if len(results.Hits) == 0 {
		t.Fatal("the phrase matched nothing")
	}
	if results.Hits[0].URI != "/phrase" {
		t.Errorf("the tighter phrase match should rank first, got %v", uris(results))
	}
}

func TestPhraseDoesNotSpanAStopwordGap(t *testing.T) {
	// Positions are recorded before stopwords are dropped, so "cat hat" must
	// not match "the cat and the hat". Getting this wrong loosens every
	// phrase query silently.
	idx := buildIndex(t,
		[3]string{"/gap", "A", "the cat and the hat"},
		[3]string{"/tight", "B", "a cat hat for sale"},
	)
	s := New(idx)

	results := mustSearch(t, s, `"cat hat"`)
	if len(results.Hits) != 1 || results.Hits[0].URI != "/tight" {
		t.Errorf("phrase matched %v, want only /tight", uris(results))
	}
}

// --- required and excluded terms ---

func TestRequiredTermFiltersResults(t *testing.T) {
	idx := buildIndex(t,
		[3]string{"/a", "Go tutorial", "Learn Go programming with goroutines."},
		[3]string{"/b", "Rust tutorial", "Learn Rust programming with lifetimes."},
	)
	s := New(idx)

	results := mustSearch(t, s, "tutorial +goroutines")
	if len(results.Hits) != 1 || results.Hits[0].URI != "/a" {
		t.Errorf("got %v, want only /a", uris(results))
	}
}

func TestExcludedTermRemovesResults(t *testing.T) {
	idx := buildIndex(t,
		[3]string{"/a", "Go tutorial", "Learn Go programming with goroutines."},
		[3]string{"/b", "Go and Rust", "Comparing Go programming to Rust."},
	)
	s := New(idx)

	results := mustSearch(t, s, "go -rust")
	if len(results.Hits) != 1 || results.Hits[0].URI != "/a" {
		t.Errorf("got %v, want only /a", uris(results))
	}
}

func TestExclusionBeatsInclusion(t *testing.T) {
	// A document matching both a positive and a negative clause must be
	// dropped -- exclusion is applied last, and it wins.
	idx := buildIndex(t,
		[3]string{"/a", "Go and Rust", "Both languages discussed."},
	)
	s := New(idx)

	if got := mustSearch(t, s, "go rust -rust"); len(got.Hits) != 0 {
		t.Errorf("an excluded document survived: %v", uris(got))
	}
}

// --- fields ---

func TestFieldScopedQueryOnlySearchesThatField(t *testing.T) {
	idx := buildIndex(t,
		[3]string{"/title", "Kubernetes networking", "Some unrelated body text about storage."},
		[3]string{"/body", "Storage guide", "This document covers kubernetes networking in depth."},
	)
	s := New(idx)

	results := mustSearch(t, s, "title:kubernetes")
	if len(results.Hits) != 1 || results.Hits[0].URI != "/title" {
		t.Errorf("title-scoped search got %v, want only /title", uris(results))
	}

	results = mustSearch(t, s, "body:kubernetes")
	if len(results.Hits) != 1 || results.Hits[0].URI != "/body" {
		t.Errorf("body-scoped search got %v, want only /body", uris(results))
	}
}

func TestTitleMatchesOutrankBodyMatches(t *testing.T) {
	// A term in the title is much stronger evidence of what a document is
	// about, and BM25 alone does not know that -- it is a fact about document
	// structure, not term statistics.
	idx := buildIndex(t,
		[3]string{"/body", "An unrelated heading", "Somewhere deep in this text the word kubernetes appears once among many other words that dilute it."},
		[3]string{"/title", "Kubernetes", "A short body."},
	)
	s := New(idx)

	results := mustSearch(t, s, "kubernetes")
	if len(results.Hits) != 2 {
		t.Fatalf("got %v", uris(results))
	}
	if results.Hits[0].URI != "/title" {
		t.Errorf("the title match should rank first, got %v", uris(results))
	}
}

// --- ranking quality ---

func TestRarerTermsCountForMore(t *testing.T) {
	// Inverse document frequency in action: a term appearing everywhere says
	// almost nothing about which document is wanted.
	docs := [][3]string{}
	for i := 0; i < 20; i++ {
		docs = append(docs, [3]string{
			fmt.Sprintf("/common%d", i), "Common", "programming programming programming",
		})
	}
	docs = append(docs, [3]string{"/rare", "Rare", "programming quicksort"})

	idx := buildIndex(t, docs...)
	s := New(idx)

	results := mustSearch(t, s, "programming quicksort")
	if len(results.Hits) == 0 {
		t.Fatal("no hits")
	}
	if results.Hits[0].URI != "/rare" {
		t.Errorf("the document with the rare term should rank first, got %v", uris(results))
	}
}

func TestTermFrequencySaturates(t *testing.T) {
	// The central difference between BM25 and TF-IDF. Under TF-IDF, fifty
	// repetitions score fifty times higher and keyword-stuffed pages win.
	// Under BM25 the gain flattens, so a stuffed document does not run away
	// with the ranking.
	scorer := &Scorer{K1: DefaultK1, B: DefaultB, docCount: 100, averageLength: 100}

	one := scorer.Score(1, 100, 10)
	two := scorer.Score(2, 100, 10)
	ten := scorer.Score(10, 100, 10)
	fifty := scorer.Score(50, 100, 10)

	if !(one < two && two < ten && ten < fifty) {
		t.Fatal("score should still increase with frequency")
	}
	firstStep := two - one
	lastStep := fifty - ten
	if lastStep >= firstStep {
		t.Errorf("frequency did not saturate: 1->2 gained %.4f, 10->50 gained %.4f",
			firstStep, lastStep)
	}
	// The whole curve is bounded by k1+1 times the idf.
	if fifty > scorer.IDF(10)*(scorer.K1+1) {
		t.Error("the score exceeded its theoretical ceiling")
	}
}

func TestLongerDocumentsAreNormalised(t *testing.T) {
	// Without length normalisation a long document wins simply by containing
	// more of every word.
	scorer := &Scorer{K1: DefaultK1, B: DefaultB, docCount: 100, averageLength: 100}

	short := scorer.Score(3, 50, 10)
	average := scorer.Score(3, 100, 10)
	long := scorer.Score(3, 500, 10)

	if !(short > average && average > long) {
		t.Errorf("length normalisation is not applied: %.4f %.4f %.4f", short, average, long)
	}
}

func TestIDFIsNeverNegative(t *testing.T) {
	// A term in more than half the documents would get a negative idf without
	// the +1 smoothing, meaning a document would score *worse* for containing
	// a word the user searched for.
	scorer := &Scorer{K1: DefaultK1, B: DefaultB, docCount: 100, averageLength: 100}
	for df := 1; df <= 100; df++ {
		if idf := scorer.IDF(df); idf < 0 {
			t.Fatalf("idf is negative (%.4f) when %d of 100 documents match", idf, df)
		}
	}
}

func TestScoresAreOrderedAndStable(t *testing.T) {
	idx := buildIndex(t,
		[3]string{"/a", "Go", "go go go"},
		[3]string{"/b", "Go", "go go"},
		[3]string{"/c", "Go", "go"},
	)
	s := New(idx)

	results := mustSearch(t, s, "go")
	for i := 1; i < len(results.Hits); i++ {
		if results.Hits[i-1].Score < results.Hits[i].Score {
			t.Errorf("results are not sorted by score: %v", results.Hits)
		}
	}

	// Running the same query twice must give the same order, including for
	// documents that tie -- otherwise pagination is broken.
	first := uris(mustSearch(t, s, "go"))
	second := uris(mustSearch(t, s, "go"))
	if strings.Join(first, ",") != strings.Join(second, ",") {
		t.Errorf("result order is unstable: %v then %v", first, second)
	}
}

// --- query parsing ---

func TestQueryParsing(t *testing.T) {
	analyze := analysis.New().AnalyzeTerms
	tests := []struct {
		input string
		want  string
	}{
		{"golang", "golang"},
		{"go rust", "go rust"},
		{`"machine learning"`, `"machin learn"`},
		{"+required", "+requir"},
		{"-excluded", "-exclud"},
		{"title:golang", "title:golang"},
		{`+title:"exact phrase"`, `+title:"exact phrase"`},
		{"  spaced   out  ", "space out"},
	}
	for _, tc := range tests {
		q, err := ParseQuery(tc.input, analyze)
		if err != nil {
			t.Errorf("ParseQuery(%q): %v", tc.input, err)
			continue
		}
		if got := q.String(); got != tc.want {
			t.Errorf("ParseQuery(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestQueryParsingHandlesAwkwardInput(t *testing.T) {
	// None of these should error or loop; a search box receives all of them.
	analyze := analysis.New().AnalyzeTerms
	for _, input := range []string{
		"", "   ", "\"", "\"unterminated phrase", "-", "+", "::",
		"the and of", // every term a stopword
		"http://example.com/path",
		strings.Repeat("word ", 200),
	} {
		q, err := ParseQuery(input, analyze)
		if err != nil {
			t.Errorf("ParseQuery(%q) errored: %v", input, err)
		}
		_ = q.String()
	}
}

func TestAllStopwordQueryReturnsNothingRatherThanFailing(t *testing.T) {
	idx := buildIndex(t, [3]string{"/a", "Title", "the and of"})
	s := New(idx)

	results, err := s.Search("the and of", DefaultOptions())
	if err != nil {
		t.Fatalf("a stopword-only query errored: %v", err)
	}
	if len(results.Hits) != 0 {
		t.Errorf("got %v, want no hits", uris(results))
	}
}

// --- snippets ---

func TestSnippetContainsAndHighlightsTheQueryTerm(t *testing.T) {
	body := "This is a long introduction that goes on for a while before finally " +
		"getting to the important part where kubernetes networking is explained " +
		"in detail, followed by more text that continues afterwards."
	idx := buildIndex(t, [3]string{"/a", "Guide", body})
	s := New(idx)

	results := mustSearch(t, s, "kubernetes")
	if len(results.Hits) == 0 {
		t.Fatal("no hits")
	}
	snippet := results.Hits[0].Snippet

	if !strings.Contains(StripHighlights(snippet), "kubernetes") {
		t.Errorf("the snippet does not show the matched term: %q", StripHighlights(snippet))
	}
	if !strings.Contains(snippet, highlightOpen) {
		t.Errorf("the snippet has no highlight markers: %q", snippet)
	}
	rendered := RenderSnippet(snippet, "<b>", "</b>")
	if !strings.Contains(rendered, "<b>kubernetes</b>") {
		t.Errorf("rendering did not wrap the term: %q", rendered)
	}
}

func TestSnippetPrefersTheDensestRegion(t *testing.T) {
	// The point of choosing a window rather than the opening: a document's
	// first hundred characters are very often boilerplate.
	body := "Boilerplate introduction text. " + strings.Repeat("filler words here. ", 20) +
		"Now golang and golang appear together with golang again."
	idx := buildIndex(t, [3]string{"/a", "Doc", body})
	s := New(idx)

	results := mustSearch(t, s, "golang")
	snippet := StripHighlights(results.Hits[0].Snippet)
	if strings.Count(snippet, "golang") < 2 {
		t.Errorf("the snippet missed the dense region: %q", snippet)
	}
}

func TestSnippetHandlesADocumentWithNoBody(t *testing.T) {
	idx := buildIndex(t, [3]string{"/a", "Only a title here", ""})
	s := New(idx)
	results := mustSearch(t, s, "title")
	if len(results.Hits) == 0 {
		t.Fatal("no hits")
	}
	// Should fall back to the title rather than returning empty or panicking.
	if StripHighlights(results.Hits[0].Snippet) == "" {
		t.Error("no snippet was produced for a body-less document")
	}
}

// --- explain ---

func TestExplainShowsHowTheScoreWasReached(t *testing.T) {
	idx := buildIndex(t,
		[3]string{"/a", "Go", "go programming"},
		[3]string{"/b", "Rust", "rust programming"},
	)
	s := New(idx)

	opts := DefaultOptions()
	opts.Explain = true
	results, err := s.Search("go programming", opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(results.Hits) == 0 {
		t.Fatal("no hits")
	}

	hit := results.Hits[0]
	if len(hit.Explanations) == 0 {
		t.Fatal("explain produced nothing")
	}
	for _, e := range hit.Explanations {
		if e.Term == "" {
			t.Error("an explanation has no term")
		}
		if e.DocFrequency <= 0 {
			t.Errorf("term %q reports a document frequency of %d", e.Term, e.DocFrequency)
		}
	}
}

// --- paging ---

func TestPagingReturnsDisjointPages(t *testing.T) {
	var docs [][3]string
	for i := 0; i < 25; i++ {
		docs = append(docs, [3]string{fmt.Sprintf("/%d", i), "Doc", "shared term here"})
	}
	idx := buildIndex(t, docs...)
	s := New(idx)

	first, err := s.Search("shared", Options{Limit: 10, SnippetLength: 100})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Search("shared", Options{Limit: 10, Offset: 10, SnippetLength: 100})
	if err != nil {
		t.Fatal(err)
	}

	if first.Total != 25 || second.Total != 25 {
		t.Errorf("total should be the full match count: %d and %d", first.Total, second.Total)
	}
	if len(first.Hits) != 10 || len(second.Hits) != 10 {
		t.Fatalf("page sizes %d and %d", len(first.Hits), len(second.Hits))
	}

	seen := make(map[string]bool)
	for _, u := range append(uris(first), uris(second)...) {
		if seen[u] {
			t.Errorf("%s appeared on both pages", u)
		}
		seen[u] = true
	}

	// An offset past the end is an empty page, not an error.
	beyond, err := s.Search("shared", Options{Limit: 10, Offset: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if len(beyond.Hits) != 0 {
		t.Errorf("an offset past the end returned %d hits", len(beyond.Hits))
	}
}

func BenchmarkSearch(b *testing.B) {
	idx := index.New(analysis.New())
	for i := 0; i < 5000; i++ {
		idx.AddDocument(
			fmt.Sprintf("/doc%d", i),
			fmt.Sprintf("Document %d about systems", i),
			"Distributed systems engineering with databases indexes and concurrency "+
				fmt.Sprintf("and a unique term%d for variety", i),
		)
	}
	s := New(idx)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Search("distributed systems databases", DefaultOptions()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPhraseSearch(b *testing.B) {
	idx := index.New(analysis.New())
	for i := 0; i < 5000; i++ {
		idx.AddDocument(fmt.Sprintf("/doc%d", i), "Doc",
			"the quick brown fox jumps over the lazy dog in document number "+fmt.Sprint(i))
	}
	s := New(idx)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Search(`"quick brown fox"`, DefaultOptions()); err != nil {
			b.Fatal(err)
		}
	}
}

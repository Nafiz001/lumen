package server

import (
	"strings"
	"testing"

	"github.com/Nafiz001/lumen/internal/search"
)

func TestCommonTitleSuffixIsDroppedOnlyWhenShared(t *testing.T) {
	site := " - The Go Programming Language"
	shared := []search.Result{
		{Title: "Slices" + site}, {Title: "Maps" + site}, {Title: "Errors" + site}, {Title: "Plain title"},
	}
	if got := commonTitleSuffix(shared); got != site {
		t.Errorf("suffix = %q, want %q", got, site)
	}

	// One title with a dash is a title, not a site name.
	lone := []search.Result{{Title: "Go - a language"}, {Title: "Slices"}, {Title: "Maps"}}
	if got := commonTitleSuffix(lone); got != "" {
		t.Errorf("a single dashed title was treated as a site name: %q", got)
	}
}

func TestSnippetHTMLFixesDoubleEscapedReferencesAndStaysSafe(t *testing.T) {
	// The crawler keeps character references from <pre> blocks as literal
	// text, so the snippet reads &#34;hi&#34;. It must show as a quote, not
	// as the characters "&#34;", and real markup must still be neutralised.
	got := string(snippetHTML(`say &#34;hi&#34; and <script>alert(1)</script>`))
	if strings.Contains(got, "&amp;#34;") {
		t.Errorf("a character reference was shown literally: %s", got)
	}
	if strings.Contains(got, "<script>") {
		t.Errorf("markup reached the page unescaped: %s", got)
	}
}

func TestURIPathSplitsWebAndFileAddresses(t *testing.T) {
	cases := map[string][]string{
		"https://go.dev/blog/slices/": {"go.dev", "blog", "slices"},
		"https://go.dev/":             {"go.dev"},
		"notes/go/channels.md":        {"notes", "go", "channels.md"},
	}
	for uri, want := range cases {
		got := uriPath(uri)
		if strings.Join(got, "/") != strings.Join(want, "/") {
			t.Errorf("uriPath(%q) = %v, want %v", uri, got, want)
		}
	}
}

func TestBuildPagerNumbersWithGaps(t *testing.T) {
	links, prev, next := buildPager("go", 0, 154) // 16 pages
	if prev != "" || next == "" {
		t.Errorf("first page: prev=%q next=%q", prev, next)
	}
	var gap bool
	var current int
	for _, l := range links {
		gap = gap || l.Gap
		if l.Current {
			current = l.N
		}
	}
	if !gap || current != 1 {
		t.Errorf("want a gap and page 1 current, got gap=%v current=%d", gap, current)
	}
	if last := links[len(links)-1]; last.N != 16 {
		t.Errorf("the last page link is %d, want 16", last.N)
	}

	if links, _, _ := buildPager("go", 0, 7); links != nil {
		t.Error("a single page should have no numbered pager")
	}
}

func TestBuildHitsMergesDuplicatesAndDrawsTheLamp(t *testing.T) {
	exp := func(term string, c float64) search.Explanation {
		return search.Explanation{Term: term, TermFrequency: 3, DocFrequency: 10, IDF: 1.5, Contribution: c}
	}
	same := "Data Race Detector - The Go Programming Language"
	res := &search.Results{Total: 4, Hits: []search.Result{
		{Title: same, URI: "https://go.dev/doc/articles/race_detector.html", Score: 6, Snippet: "conflicting accesses", Explanations: []search.Explanation{exp("race", 4), exp("detector", 2)}},
		{Title: same, URI: "https://go.dev/doc/articles/race_detector", Score: 6, Snippet: "conflicting accesses", Explanations: []search.Explanation{exp("race", 4), exp("detector", 2)}},
		{Title: "Slices - The Go Programming Language", URI: "https://go.dev/blog/slices", Score: 3, Snippet: "a slice is", Explanations: []search.Explanation{exp("race", 3)}},
		{Title: "Maps - The Go Programming Language", URI: "https://go.dev/blog/maps", Score: 3.5, Snippet: "a map is", Explanations: []search.Explanation{exp("detector", 3), exp("race", 0.4)}},
	}}

	hits, legend := buildHits(res, 0)
	if len(hits) != 3 {
		t.Fatalf("%d hits, want the two copies merged into one (3)", len(hits))
	}
	if len(hits[0].Also) != 1 || !strings.HasSuffix(hits[0].Also[0].URI, "race_detector") {
		t.Errorf("the duplicate was not listed as an alternative address: %+v", hits[0].Also)
	}
	if hits[0].Title != "Data Race Detector" {
		t.Errorf("the site name was not removed from the title: %q", hits[0].Title)
	}
	if len(legend) != 2 || legend[0].Term != "race" || legend[1].Color != 1 {
		t.Errorf("legend = %+v, want race then detector with different colours", legend)
	}
	// The best hit's bar is the whole width: its segments add up to 100%.
	var sum float64
	for _, s := range hits[0].Bar {
		sum += s.Width
	}
	if sum < 99.9 || sum > 100.1 {
		t.Errorf("the best result's bar is %.1f%% wide, want 100%%", sum)
	}
}

func TestSearchPageShowsTheLampAndTheExplanation(t *testing.T) {
	srv := testServer(t)
	page := get(t, srv, "/?q=goroutines+channels").Body.String()
	for _, want := range []string{`class="lamp"`, "Why this ranked here", `class="legend"`, "Okapi BM25"} {
		if !strings.Contains(page, want) {
			t.Errorf("results page is missing %q", want)
		}
	}
}

func TestLandingPageHasTheIndexAndTheSyntax(t *testing.T) {
	srv := testServer(t)
	page := get(t, srv, "/").Body.String()
	for _, want := range []string{"How it works", "Query syntax", `class="word"`, "distinct terms"} {
		if !strings.Contains(page, want) {
			t.Errorf("landing page is missing %q", want)
		}
	}
}

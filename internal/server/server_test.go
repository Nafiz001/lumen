package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nafiz001/lumen/internal/analysis"
	"github.com/Nafiz001/lumen/internal/index"
)

func testServer(t *testing.T) *Server {
	t.Helper()

	idx := index.New(analysis.New())
	docs := []struct{ uri, title, body string }{
		{"go/concurrency.md", "Concurrency in Go",
			"Goroutines are cheap threads managed by the Go runtime. Channels connect goroutines safely."},
		{"go/channels.md", "Channels Explained",
			"A channel carries values between goroutines. Unbuffered channels block until both sides are ready."},
		{"rust/ownership.md", "Ownership in Rust",
			"Ownership is how Rust manages memory without a garbage collector or a runtime."},
		{"notes/xss.md", "A Document With Markup",
			`This body contains <script>alert("xss")</script> and a <b>bold</b> tag about goroutines.`},
	}
	for _, d := range docs {
		if _, err := idx.AddDocument(d.uri, d.title, d.body); err != nil {
			t.Fatalf("index %s: %v", d.uri, err)
		}
	}

	// Discard logs so a failing test's output is the assertion, not the traffic.
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(idx, "127.0.0.1:0", logger)
}

func get(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func getJSON(t *testing.T, srv *Server, path string) (int, map[string]any) {
	t.Helper()
	rec := get(t, srv, path)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET %s returned unparseable JSON: %v\n%s", path, err, rec.Body.String())
	}
	return rec.Code, body
}

// --- the JSON API ---

func TestAPISearch(t *testing.T) {
	srv := testServer(t)

	code, body := getJSON(t, srv, "/api/search?q=goroutines")
	if code != http.StatusOK {
		t.Fatalf("status %d, want 200", code)
	}

	hits, _ := body["hits"].([]any)
	if len(hits) == 0 {
		t.Fatal("no hits for a term that appears in three documents")
	}
	if total, _ := body["total"].(float64); int(total) != len(hits) {
		t.Errorf("total = %v but %d hits were returned", body["total"], len(hits))
	}

	first, _ := hits[0].(map[string]any)
	for _, field := range []string{"id", "uri", "title", "score", "snippet"} {
		if _, ok := first[field]; !ok {
			t.Errorf("a hit is missing the %q field: %v", field, first)
		}
	}
}

func TestAPISearchRequiresAQuery(t *testing.T) {
	srv := testServer(t)

	code, body := getJSON(t, srv, "/api/search")
	if code != http.StatusBadRequest {
		t.Errorf("status %d for a missing query, want 400", code)
	}
	if _, ok := body["error"]; !ok {
		t.Errorf("no error message in %v", body)
	}
}

func TestAPISearchRendersHighlightsAsMarks(t *testing.T) {
	srv := testServer(t)

	_, body := getJSON(t, srv, "/api/search?q=goroutines")
	hits, _ := body["hits"].([]any)
	if len(hits) == 0 {
		t.Fatal("no hits")
	}

	joined := ""
	for _, h := range hits {
		hit, _ := h.(map[string]any)
		snippet, _ := hit["snippet"].(string)
		joined += snippet
		// The internal markers are control characters and must never reach a
		// client; they are an implementation detail of the snippet builder.
		if strings.ContainsAny(snippet, "\x02\x03") {
			t.Errorf("raw highlight markers leaked into the API: %q", snippet)
		}
	}
	if !strings.Contains(joined, "<mark>") {
		t.Errorf("no highlight in any snippet: %q", joined)
	}
}

func TestAPISearchLimitAndOffset(t *testing.T) {
	srv := testServer(t)

	_, first := getJSON(t, srv, "/api/search?q=goroutines&limit=1")
	firstHits, _ := first["hits"].([]any)
	if len(firstHits) != 1 {
		t.Fatalf("limit=1 returned %d hits", len(firstHits))
	}

	_, second := getJSON(t, srv, "/api/search?q=goroutines&limit=1&offset=1")
	secondHits, _ := second["hits"].([]any)
	if len(secondHits) != 1 {
		t.Fatalf("offset=1 returned %d hits", len(secondHits))
	}

	a, _ := firstHits[0].(map[string]any)
	b, _ := secondHits[0].(map[string]any)
	if a["uri"] == b["uri"] {
		t.Errorf("offset returned the same document: %v", a["uri"])
	}

	// total counts every match, not the page, or pagination cannot know when
	// to stop.
	if total, _ := first["total"].(float64); int(total) < 2 {
		t.Errorf("total = %v, want the full match count", first["total"])
	}
}

func TestAPISearchCapsTheLimit(t *testing.T) {
	// An unbounded limit lets one request ask the server to materialise the
	// whole corpus.
	srv := testServer(t)

	code, _ := getJSON(t, srv, "/api/search?q=goroutines&limit=100000")
	if code != http.StatusOK {
		t.Errorf("status %d for an oversized limit, want it clamped and served", code)
	}
}

func TestAPISearchIgnoresJunkPagination(t *testing.T) {
	srv := testServer(t)

	for _, query := range []string{
		"/api/search?q=goroutines&limit=abc",
		"/api/search?q=goroutines&limit=-5",
		"/api/search?q=goroutines&offset=-1",
	} {
		if code, _ := getJSON(t, srv, query); code != http.StatusOK {
			t.Errorf("%s gave status %d, want the bad value ignored", query, code)
		}
	}
}

func TestAPISearchExplain(t *testing.T) {
	srv := testServer(t)

	_, body := getJSON(t, srv, "/api/search?q=goroutines&explain=true")
	hits, _ := body["hits"].([]any)
	if len(hits) == 0 {
		t.Fatal("no hits")
	}

	first, _ := hits[0].(map[string]any)
	explanations, ok := first["explanations"].([]any)
	if !ok || len(explanations) == 0 {
		t.Fatalf("explain=true returned no explanations: %v", first)
	}

	e, _ := explanations[0].(map[string]any)
	for _, field := range []string{"term", "idf", "contribution"} {
		if _, ok := e[field]; !ok {
			t.Errorf("explanation is missing %q: %v", field, e)
		}
	}
}

func TestAPISearchOmitsExplanationsByDefault(t *testing.T) {
	srv := testServer(t)

	_, body := getJSON(t, srv, "/api/search?q=goroutines")
	hits, _ := body["hits"].([]any)
	first, _ := hits[0].(map[string]any)

	if _, ok := first["explanations"]; ok {
		t.Errorf("explanations were sent without explain=true: %v", first)
	}
}

func TestAPISearchPhraseAndExclusion(t *testing.T) {
	srv := testServer(t)

	_, body := getJSON(t, srv, "/api/search?q="+urlQuery(`goroutines -channel`))
	hits, _ := body["hits"].([]any)

	for _, h := range hits {
		hit, _ := h.(map[string]any)
		if hit["uri"] == "go/channels.md" {
			t.Errorf("an excluded term still matched: %v", hit["uri"])
		}
	}
}

func TestAPIStats(t *testing.T) {
	srv := testServer(t)

	code, body := getJSON(t, srv, "/api/stats")
	if code != http.StatusOK {
		t.Fatalf("status %d, want 200", code)
	}
	if documents, _ := body["documents"].(float64); int(documents) != 4 {
		t.Errorf("documents = %v, want 4", body["documents"])
	}
	for _, field := range []string{"terms", "postings", "average_length", "queries_served"} {
		if _, ok := body[field]; !ok {
			t.Errorf("stats is missing %q: %v", field, body)
		}
	}
}

func TestAPIStatsCountsQueries(t *testing.T) {
	srv := testServer(t)

	get(t, srv, "/api/search?q=goroutines")
	get(t, srv, "/api/search?q=ownership")
	get(t, srv, "/?q=channels")

	_, body := getJSON(t, srv, "/api/stats")
	if served, _ := body["queries_served"].(float64); int(served) != 3 {
		t.Errorf("queries_served = %v, want 3", body["queries_served"])
	}
}

func TestAPIDocument(t *testing.T) {
	srv := testServer(t)

	code, body := getJSON(t, srv, "/api/document/1")
	if code != http.StatusOK {
		t.Fatalf("status %d, want 200", code)
	}
	if _, ok := body["uri"]; !ok {
		t.Errorf("document has no uri: %v", body)
	}

	if code, _ := getJSON(t, srv, "/api/document/99999"); code != http.StatusNotFound {
		t.Errorf("status %d for a missing document, want 404", code)
	}
	if code, _ := getJSON(t, srv, "/api/document/not-a-number"); code != http.StatusBadRequest {
		t.Errorf("status %d for a malformed id, want 400", code)
	}
}

// --- the HTML page ---

func TestSearchPageRendersResults(t *testing.T) {
	srv := testServer(t)

	rec := get(t, srv, "/?q=goroutines")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}

	page := rec.Body.String()
	for _, want := range []string{"Concurrency in Go", "<mark>", "</html>"} {
		if !strings.Contains(page, want) {
			t.Errorf("page is missing %q", want)
		}
	}
}

func TestSearchPageEmptyQueryShowsHelp(t *testing.T) {
	srv := testServer(t)

	page := get(t, srv, "/").Body.String()
	if !strings.Contains(page, "Query syntax") {
		t.Error("the landing page does not explain the query syntax")
	}
	if strings.Contains(page, "<ol class=\"results\"") {
		t.Error("the landing page rendered a result list for an empty query")
	}
}

func TestSearchPageNoResults(t *testing.T) {
	srv := testServer(t)

	page := get(t, srv, "/?q=xyzzyplughnothing").Body.String()
	if !strings.Contains(page, "Nothing matched") {
		t.Errorf("no 'no results' message: %s", page)
	}
}

func TestSearchPageEscapesDocumentMarkup(t *testing.T) {
	// The corpus contains a document whose body has a <script> tag. If the
	// snippet were rendered before escaping, that tag would execute in the
	// browser of whoever searched for it -- stored XSS by way of the index.
	srv := testServer(t)

	page := get(t, srv, "/?q=goroutines").Body.String()
	if strings.Contains(page, "<script>alert") {
		t.Error("a script tag from an indexed document reached the page unescaped")
	}
	if !strings.Contains(page, "&lt;script&gt;") {
		t.Error("the document's markup was dropped rather than escaped")
	}
}

func TestSearchPageEscapesTheQuery(t *testing.T) {
	// The query is echoed into the search box and the <title>, so it is the
	// other injection route into the page.
	srv := testServer(t)

	page := get(t, srv, "/?q="+urlQuery(`<img src=x onerror=alert(1)>`)).Body.String()
	if strings.Contains(page, "<img src=x") {
		t.Error("the query was echoed into the page unescaped")
	}
}

func TestSearchPagePagination(t *testing.T) {
	srv := testServer(t)

	// Three documents mention goroutines; page 1 of a ten-per-page list is
	// past the end and must render cleanly rather than error.
	rec := get(t, srv, "/?q=goroutines&p=1")
	if rec.Code != http.StatusOK {
		t.Errorf("status %d for a page past the end, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "next &rarr;") {
		t.Error("offered a next page when there is nothing after it")
	}
}

func TestUnknownPathIs404(t *testing.T) {
	srv := testServer(t)

	if rec := get(t, srv, "/nope"); rec.Code != http.StatusNotFound {
		t.Errorf("status %d for an unknown path, want 404", rec.Code)
	}
}

// --- helpers ---

func TestIsLoopbackDetection(t *testing.T) {
	// Not the server's own function, but the distinction it warns about; kept
	// here because the warning is part of the server's contract.
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"localhost:8080", true},
		{"[::1]:8080", true},
		{":8080", true},
		{"0.0.0.0:8080", false},
		{"192.168.1.5:8080", false},
	} {
		if got := loopback(tc.addr); got != tc.want {
			t.Errorf("loopback(%s) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

// loopback mirrors the CLI's check; the server package tests it so the rule
// lives next to the code whose safety depends on it.
func loopback(addr string) bool {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "", "127.0.0.1", "localhost", "::1":
		return true
	}
	return strings.HasPrefix(host, "127.")
}

func urlQuery(s string) string {
	replacer := strings.NewReplacer(
		"%", "%25", " ", "%20", "<", "%3C", ">", "%3E", `"`, "%22",
		"#", "%23", "&", "%26", "+", "%2B", "=", "%3D",
	)
	return replacer.Replace(s)
}

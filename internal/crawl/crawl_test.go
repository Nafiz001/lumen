package crawl

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// --- the file crawler ---

// writeTree builds a directory of files for one test and returns its root.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return root
}

// collect runs a crawl and returns the documents by URI.
func collect(t *testing.T, root string, opts FileOptions) map[string]Doc {
	t.Helper()
	docs := make(map[string]Doc)
	crawler := NewFileCrawler(opts)
	count, err := crawler.Crawl(root, func(d Doc) error {
		docs[d.URI] = d
		return nil
	})
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	if count != len(docs) {
		t.Fatalf("crawl reported %d documents but emitted %d", count, len(docs))
	}
	return docs
}

func uris(docs map[string]Doc) []string {
	out := make([]string, 0, len(docs))
	for uri := range docs {
		out = append(out, uri)
	}
	sort.Strings(out)
	return out
}

func TestFileCrawlerSelectsTextFiles(t *testing.T) {
	root := writeTree(t, map[string]string{
		"notes.md":          "# Notes\n\nSomething worth finding.",
		"main.go":           "package main\n",
		"photo.jpg":         "not really a jpeg, but the extension is what matters",
		"archive.zip":       "nor a zip",
		"deep/nested.txt":   "still indexed, nesting is not a reason to skip",
		"empty.txt":         "",
		".hidden.md":        "dotfiles are skipped by default",
		".git/config":       "[core]",
		"node_modules/a.js": "module.exports = {}",
	})

	docs := collect(t, root, DefaultFileOptions())
	got := uris(docs)
	want := []string{"deep/nested.txt", "main.go", "notes.md"}

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("indexed %v, want %v", got, want)
	}
}

func TestFileCrawlerSkipsEmptyFiles(t *testing.T) {
	// An empty file has no terms, so indexing it adds a document that can never
	// match anything while still counting against the average length that BM25
	// normalises against.
	root := writeTree(t, map[string]string{"empty.txt": "", "full.txt": "words"})

	docs := collect(t, root, DefaultFileOptions())
	if _, ok := docs["empty.txt"]; ok {
		t.Error("indexed an empty file")
	}
	if _, ok := docs["full.txt"]; !ok {
		t.Error("did not index a non-empty file")
	}
}

func TestFileCrawlerRespectsMaxFileSize(t *testing.T) {
	root := writeTree(t, map[string]string{
		"small.txt": "tiny",
		"large.txt": strings.Repeat("x ", 500),
	})

	opts := DefaultFileOptions()
	opts.MaxFileSize = 100

	docs := collect(t, root, opts)
	if _, ok := docs["large.txt"]; ok {
		t.Error("indexed a file over the size limit")
	}
	if _, ok := docs["small.txt"]; !ok {
		t.Error("skipped a file under the size limit")
	}
}

func TestFileCrawlerSkipsBinaryContent(t *testing.T) {
	// A .log that is actually a core dump has an allowed extension and is still
	// not text; the NUL byte is what gives it away.
	root := writeTree(t, map[string]string{
		"real.log": "2024-01-01 started fine",
		"fake.log": "header\x00\x00\x00binary payload",
	})

	docs := collect(t, root, DefaultFileOptions())
	if _, ok := docs["fake.log"]; ok {
		t.Error("indexed a file containing NUL bytes")
	}
	if _, ok := docs["real.log"]; !ok {
		t.Error("skipped a genuine text log")
	}
}

func TestFileCrawlerCustomExtensions(t *testing.T) {
	root := writeTree(t, map[string]string{
		"a.txt": "text",
		"b.go":  "package b",
		"c.md":  "# markdown",
	})

	opts := DefaultFileOptions()
	opts.Extensions = []string{".md"}

	docs := collect(t, root, opts)
	if len(docs) != 1 {
		t.Fatalf("indexed %v, want only c.md", uris(docs))
	}
	if _, ok := docs["c.md"]; !ok {
		t.Errorf("indexed %v, want c.md", uris(docs))
	}
}

func TestFileCrawlerIncludesHiddenWhenAsked(t *testing.T) {
	root := writeTree(t, map[string]string{
		".env.txt":      "SECRET=1",
		"visible.txt":   "public",
		".config/a.txt": "in a hidden directory",
	})

	opts := DefaultFileOptions()
	opts.SkipHidden = false

	docs := collect(t, root, opts)
	if len(docs) != 3 {
		t.Errorf("indexed %v, want all three", uris(docs))
	}
}

func TestFileCrawlerTitles(t *testing.T) {
	root := writeTree(t, map[string]string{
		"heading.md": "# The Real Title\n\nbody text",
		"spaced.md":  "\n\n   # Indented Heading\n\nbody",
		"nothing.md": "no heading here, just prose",
		"buried.md":  "intro paragraph\n\n# A Section Heading\n",
		"code.go":    "package main",
	})

	docs := collect(t, root, DefaultFileOptions())

	for _, tc := range []struct{ uri, want string }{
		{"heading.md", "The Real Title"},
		{"spaced.md", "Indented Heading"},
		{"nothing.md", "nothing.md"},
		// A heading below prose is a section, not the document's title.
		{"buried.md", "buried.md"},
		{"code.go", "code.go"},
	} {
		if got := docs[tc.uri].Title; got != tc.want {
			t.Errorf("title of %s = %q, want %q", tc.uri, got, tc.want)
		}
	}
}

func TestFileCrawlerExtractsHTMLBodies(t *testing.T) {
	root := writeTree(t, map[string]string{
		"page.html": `<html><head><title>Ignored Here</title>` +
			`<style>body{color:red}</style></head>` +
			`<body><p>Readable prose.</p><script>var x=1;</script></body></html>`,
	})

	docs := collect(t, root, DefaultFileOptions())
	body := docs["page.html"].Body

	if !strings.Contains(body, "Readable prose") {
		t.Errorf("body lost the prose: %q", body)
	}
	for _, unwanted := range []string{"var x=1", "color:red", "<p>"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("body kept %q: %q", unwanted, body)
		}
	}
}

func TestFileCrawlerURIsUseForwardSlashes(t *testing.T) {
	// The URI ends up in a URL and in a JSON file, so it must not carry
	// Windows separators even when the crawl ran on Windows.
	root := writeTree(t, map[string]string{"a/b/c.txt": "nested"})

	docs := collect(t, root, DefaultFileOptions())
	if _, ok := docs["a/b/c.txt"]; !ok {
		t.Errorf("URIs were %v, want a/b/c.txt", uris(docs))
	}
}

func TestValidateRoot(t *testing.T) {
	dir := t.TempDir()
	if err := ValidateRoot(dir); err != nil {
		t.Errorf("ValidateRoot on a directory: %v", err)
	}

	file := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRoot(file); err == nil {
		t.Error("ValidateRoot accepted a file")
	}
	if err := ValidateRoot(filepath.Join(dir, "nope")); err == nil {
		t.Error("ValidateRoot accepted a missing path")
	}
}

// --- HTML extraction ---

func TestExtractTitleAndText(t *testing.T) {
	html := `<!doctype html>
<html><head>
  <title>  Concurrency in Go  </title>
  <style>.a{color:#fff}</style>
  <script>function f(){return "<p>not text</p>";}</script>
</head>
<body>
  <!-- a comment that should vanish -->
  <h1>Goroutines</h1>
  <p>A goroutine is cheap.</p>
  <p>Channels connect them.</p>
</body></html>`

	title, text := ExtractTitleAndText(html)

	if title != "Concurrency in Go" {
		t.Errorf("title = %q, want %q", title, "Concurrency in Go")
	}
	for _, want := range []string{"Goroutines", "A goroutine is cheap.", "Channels connect them."} {
		if !strings.Contains(text, want) {
			t.Errorf("text lost %q: %q", want, text)
		}
	}
	for _, unwanted := range []string{"color:#fff", "function f", "not text", "a comment"} {
		if strings.Contains(text, unwanted) {
			t.Errorf("text kept %q: %q", unwanted, text)
		}
	}
}

func TestExtractTextSeparatesBlocks(t *testing.T) {
	// Without a boundary at block tags, "<p>one</p><p>two</p>" collapses into
	// "onetwo" and the index gains a term nobody will search for while losing
	// the two that they will.
	text := ExtractText(`<p>alpha</p><p>beta</p><div>gamma</div><li>delta</li>`)

	for _, want := range []string{"alpha", "beta", "gamma", "delta"} {
		if !strings.Contains(text, want) {
			t.Errorf("lost %q from %q", want, text)
		}
	}
	if strings.Contains(text, "alphabeta") {
		t.Errorf("block tags did not separate words: %q", text)
	}
}

func TestExtractTextInlineTagsDoNotSplitWords(t *testing.T) {
	// An inline tag inside a word is a formatting artefact, not a boundary.
	// This is the compromise the scanner makes: a space is safer than nothing,
	// because a run-together word is unsearchable while a split one still has
	// two findable halves.
	text := ExtractText(`<p>data<b>base</b></p>`)
	if !strings.Contains(text, "data") || !strings.Contains(text, "base") {
		t.Errorf("lost part of the text: %q", text)
	}
}

func TestDecodeEntities(t *testing.T) {
	text := ExtractText(`<p>Tom &amp; Jerry said &quot;hi&quot; &mdash; loudly&hellip;</p>`)

	for _, want := range []string{"Tom & Jerry", `"hi"`, "loudly..."} {
		if !strings.Contains(text, want) {
			t.Errorf("entity not decoded, %q missing from %q", want, text)
		}
	}
}

func TestExtractTextUnterminatedTag(t *testing.T) {
	// Malformed markup must not panic or loop; losing the tail is acceptable.
	text := ExtractText(`<p>visible</p><div class="broken`)
	if !strings.Contains(text, "visible") {
		t.Errorf("lost the well-formed part: %q", text)
	}
}

func TestExtractLinks(t *testing.T) {
	base, _ := url.Parse("https://example.com/docs/guide.html")
	html := `
	  <a href="/absolute">a</a>
	  <a href='relative.html'>b</a>
	  <a href="../up.html">c</a>
	  <a href="https://other.example.org/x">d</a>
	  <a href="#anchor">e</a>
	  <a href="mailto:someone@example.com">f</a>
	  <a href="javascript:void(0)">g</a>
	  <a href="/absolute">duplicate</a>
	  <link href="/style.css" rel="stylesheet">
	`

	links := ExtractLinks(html, base)
	got := make([]string, len(links))
	for i, l := range links {
		got[i] = l.String()
	}
	joined := strings.Join(got, " ")

	for _, want := range []string{
		"https://example.com/absolute",
		"https://example.com/docs/relative.html",
		"https://example.com/up.html",
		"https://other.example.org/x",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing link %s, got %v", want, got)
		}
	}
	for _, unwanted := range []string{"mailto:", "javascript:", "#anchor"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("kept a non-page link %q: %v", unwanted, got)
		}
	}
	// The duplicate href must appear once; a crawler that queues it twice
	// spends its page budget re-fetching what it already has.
	if n := strings.Count(joined, "https://example.com/absolute"); n != 1 {
		t.Errorf("the repeated href appeared %d times: %v", n, got)
	}
}

// --- URL normalisation ---

func TestNormalizeURL(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		{"https://example.com/page#section", "https://example.com/page"},
		{"https://example.com/page/", "https://example.com/page"},
		{"https://example.com/", "https://example.com/"},
		{"https://example.com", "https://example.com/"},
		{"https://EXAMPLE.com/Page", "https://example.com/Page"},
		{"https://example.com/p?utm_source=x&id=7", "https://example.com/p?id=7"},
		{"https://example.com/p?fbclid=abc", "https://example.com/p"},
		{"https://example.com/p?gclid=abc&q=go", "https://example.com/p?q=go"},
	}

	for _, tc := range tests {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatalf("parse %s: %v", tc.raw, err)
		}
		if got := normalizeURL(u); got != tc.want {
			t.Errorf("normalizeURL(%s) = %s, want %s", tc.raw, got, tc.want)
		}
	}
}

func TestNormalizeURLPathCaseIsPreserved(t *testing.T) {
	// The host is case-insensitive and the path is not: lowercasing the path
	// would make the crawler ask for a URL the server does not serve.
	u, _ := url.Parse("https://Example.com/CaseSensitive/Path")
	if got := normalizeURL(u); got != "https://example.com/CaseSensitive/Path" {
		t.Errorf("normalizeURL lowercased the path: %s", got)
	}
}

// --- robots.txt ---

func TestParseRobotsOnlyReadsTheWildcardGroup(t *testing.T) {
	rules := parseRobots(`
# a comment
User-agent: GoogleBot
Disallow: /

User-agent: *
Disallow: /private
Allow: /private/public
Crawl-delay: 2
`)

	if len(rules.disallow) != 1 || rules.disallow[0] != "/private" {
		t.Errorf("disallow = %v, want [/private]", rules.disallow)
	}
	if len(rules.allow) != 1 || rules.allow[0] != "/private/public" {
		t.Errorf("allow = %v, want [/private/public]", rules.allow)
	}
	if rules.crawlWait != 2*time.Second {
		t.Errorf("crawl-delay = %s, want 2s", rules.crawlWait)
	}
}

func TestRobotsPermits(t *testing.T) {
	rules := parseRobots(`
User-agent: *
Disallow: /private
Allow: /private/public
Disallow: /tmp
`)

	tests := []struct {
		path string
		want bool
	}{
		{"/", true},
		{"/about", true},
		{"/private", false},
		{"/private/secret", false},
		// A longer Allow overrides a shorter Disallow, which is how a site
		// carves an exception out of a blocked subtree.
		{"/private/public", true},
		{"/private/public/deep", true},
		{"/tmp/anything", false},
	}

	for _, tc := range tests {
		if got := rules.permits(tc.path); got != tc.want {
			t.Errorf("permits(%s) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestRobotsEmptyDisallowMeansNoRestriction(t *testing.T) {
	// "Disallow:" with no value is the conventional way to say "everything is
	// allowed". Reading it as a prefix match on "" would block the whole site.
	rules := parseRobots("User-agent: *\nDisallow:\n")
	if !rules.permits("/anything") {
		t.Error("an empty Disallow blocked the site")
	}
}

func TestParseRobotsIgnoresJunk(t *testing.T) {
	rules := parseRobots("this is not robots.txt\n<html>\n\n   \nSitemap: https://x/y.xml\n")
	if !rules.permits("/anything") {
		t.Error("unparseable robots.txt blocked the site")
	}
}

// --- the web crawler, against a local server ---

// testSite serves a small linked site plus a robots.txt.
func testSite(t *testing.T, robots string) *httptest.Server {
	t.Helper()

	pages := map[string]string{
		"/": `<html><head><title>Home</title></head><body>
			<h1>Home</h1><p>The front page.</p>
			<a href="/one">one</a> <a href="/two">two</a>
			<a href="/private/hidden">private</a>
			<a href="https://elsewhere.invalid/off-site">off site</a>
		</body></html>`,
		"/one": `<html><head><title>Page One</title></head><body>
			<p>The first page mentions goroutines.</p>
			<a href="/three">three</a> <a href="/">home</a>
		</body></html>`,
		"/two": `<html><head><title>Page Two</title></head><body>
			<p>The second page mentions channels.</p>
		</body></html>`,
		"/three": `<html><head><title>Page Three</title></head><body>
			<p>Three links deep.</p>
		</body></html>`,
		"/private/hidden": `<html><head><title>Private</title></head><body>
			<p>Should not be crawled when robots.txt says so.</p>
		</body></html>`,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		if robots == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, robots)
	})
	mux.HandleFunc("/binary", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte{0x89, 'P', 'N', 'G'})
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, body)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// fastOptions removes the politeness delay, which would otherwise make every
// crawl test take seconds. The delay itself is tested separately.
func fastOptions() WebOptions {
	opts := DefaultWebOptions()
	opts.Delay = time.Millisecond
	opts.Timeout = 5 * time.Second
	return opts
}

func crawlSite(t *testing.T, start string, opts WebOptions) map[string]Doc {
	t.Helper()
	docs := make(map[string]Doc)
	crawler := NewWebCrawler(opts)
	_, err := crawler.Crawl(context.Background(), start, func(d Doc) error {
		docs[d.URI] = d
		return nil
	})
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	return docs
}

func TestWebCrawlerFollowsLinks(t *testing.T) {
	site := testSite(t, "")
	docs := crawlSite(t, site.URL, fastOptions())

	for _, suffix := range []string{"/", "/one", "/two", "/three"} {
		if _, ok := docs[site.URL+suffix]; !ok {
			t.Errorf("did not reach %s; reached %v", suffix, uris(docs))
		}
	}
	if got := docs[site.URL+"/one"].Title; got != "Page One" {
		t.Errorf("title of /one = %q, want %q", got, "Page One")
	}
	if body := docs[site.URL+"/one"].Body; !strings.Contains(body, "goroutines") {
		t.Errorf("body of /one lost its text: %q", body)
	}
}

func TestWebCrawlerStaysOnHost(t *testing.T) {
	site := testSite(t, "")
	docs := crawlSite(t, site.URL, fastOptions())

	for uri := range docs {
		if !strings.HasPrefix(uri, site.URL) {
			t.Errorf("left the starting host: %s", uri)
		}
	}
}

func TestWebCrawlerHonoursMaxPages(t *testing.T) {
	site := testSite(t, "")
	opts := fastOptions()
	opts.MaxPages = 2

	docs := crawlSite(t, site.URL, opts)
	if len(docs) != 2 {
		t.Errorf("fetched %d pages, want 2: %v", len(docs), uris(docs))
	}
}

func TestWebCrawlerHonoursMaxDepth(t *testing.T) {
	site := testSite(t, "")
	opts := fastOptions()
	// Depth 1 means the start page and what it links to directly. /three is
	// only reachable through /one, so it is out of range.
	opts.MaxDepth = 1

	docs := crawlSite(t, site.URL, opts)
	if _, ok := docs[site.URL+"/three"]; ok {
		t.Errorf("reached a page beyond the depth limit: %v", uris(docs))
	}
	if _, ok := docs[site.URL+"/one"]; !ok {
		t.Errorf("did not reach a page within the depth limit: %v", uris(docs))
	}
}

func TestWebCrawlerHonoursRobots(t *testing.T) {
	site := testSite(t, "User-agent: *\nDisallow: /private\n")
	docs := crawlSite(t, site.URL, fastOptions())

	if _, ok := docs[site.URL+"/private/hidden"]; ok {
		t.Error("crawled a path robots.txt disallowed")
	}
	if _, ok := docs[site.URL+"/one"]; !ok {
		t.Error("robots.txt blocked an allowed path")
	}
}

func TestWebCrawlerIgnoreRobotsFlag(t *testing.T) {
	site := testSite(t, "User-agent: *\nDisallow: /\n")

	opts := fastOptions()
	docs := crawlSite(t, site.URL, opts)
	if len(docs) != 0 {
		t.Errorf("a blanket Disallow was not honoured: %v", uris(docs))
	}

	opts.IgnoreRobots = true
	docs = crawlSite(t, site.URL, opts)
	if len(docs) == 0 {
		t.Error("-ignore-robots did not override robots.txt")
	}
}

func TestWebCrawlerVisitsEachURLOnce(t *testing.T) {
	// /one links back to the front page. Without a visited set the crawl would
	// bounce between them until the page budget ran out.
	site := testSite(t, "")

	seen := make(map[string]int)
	crawler := NewWebCrawler(fastOptions())
	_, err := crawler.Crawl(context.Background(), site.URL, func(d Doc) error {
		seen[d.URI]++
		return nil
	})
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	for uri, n := range seen {
		if n != 1 {
			t.Errorf("%s was emitted %d times", uri, n)
		}
	}
}

func TestWebCrawlerSkipsNonHTML(t *testing.T) {
	site := testSite(t, "")
	docs := crawlSite(t, site.URL+"/binary", fastOptions())

	if len(docs) != 0 {
		t.Errorf("indexed a non-HTML response: %v", uris(docs))
	}
}

func TestWebCrawlerRespectsDelay(t *testing.T) {
	site := testSite(t, "")

	opts := fastOptions()
	opts.Delay = 120 * time.Millisecond
	opts.MaxPages = 3

	start := time.Now()
	crawlSite(t, site.URL, opts)
	elapsed := time.Since(start)

	// Three pages means at least two waits between them.
	if min := 2 * opts.Delay; elapsed < min {
		t.Errorf("three pages took %s, less than the %s the delay requires", elapsed, min)
	}
}

func TestWebCrawlerStopsOnContextCancel(t *testing.T) {
	site := testSite(t, "")

	ctx, cancel := context.WithCancel(context.Background())
	crawler := NewWebCrawler(fastOptions())

	count := 0
	_, err := crawler.Crawl(ctx, site.URL, func(d Doc) error {
		count++
		cancel() // stop after the very first page
		return nil
	})

	if count != 1 {
		t.Errorf("emitted %d documents after cancelling on the first", count)
	}
	if err != nil && ctx.Err() == nil {
		t.Errorf("crawl failed for a reason other than cancellation: %v", err)
	}
}

func TestWebCrawlerPropagatesEmitError(t *testing.T) {
	site := testSite(t, "")
	crawler := NewWebCrawler(fastOptions())

	want := fmt.Errorf("the index is full")
	_, err := crawler.Crawl(context.Background(), site.URL, func(d Doc) error {
		return want
	})
	if err == nil || !strings.Contains(err.Error(), "index is full") {
		t.Errorf("emit error = %v, want it to surface", err)
	}
}

func TestWebCrawlerRejectsBadStartURL(t *testing.T) {
	crawler := NewWebCrawler(fastOptions())
	if _, err := crawler.Crawl(context.Background(), "://not a url", func(Doc) error {
		return nil
	}); err == nil {
		t.Error("accepted an unparseable start URL")
	}
}

func TestWebCrawlerStats(t *testing.T) {
	site := testSite(t, "")
	crawler := NewWebCrawler(fastOptions())
	fetched, err := crawler.Crawl(context.Background(), site.URL, func(Doc) error { return nil })
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	if got := crawler.Stats().Fetched; got != fetched {
		t.Errorf("Stats().Fetched = %d, Crawl returned %d", got, fetched)
	}
}

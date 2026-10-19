package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nafiz001/lumen/internal/analysis"
	"github.com/Nafiz001/lumen/internal/crawl"
	"github.com/Nafiz001/lumen/internal/index"
	"github.com/Nafiz001/lumen/internal/search"
	"github.com/Nafiz001/lumen/internal/server"
)

// A tiny site whose pages can be changed between crawls: the home page links
// to /p1../pN, each saying something about the current "topic".
type testSite struct {
	*httptest.Server
	topic atomic.Value // string
	pages atomic.Int32
	down  atomic.Bool
}

func newTestSite(t *testing.T, topic string, pages int) *testSite {
	t.Helper()
	s := &testSite{}
	s.topic.Store(topic)
	s.pages.Store(int32(pages))
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if s.down.Load() {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		topic := s.topic.Load().(string)
		if r.URL.Path == "/" {
			var links strings.Builder
			for i := 1; i <= int(s.pages.Load()); i++ {
				fmt.Fprintf(&links, `<a href="/p%d">page %d</a> `, i, i)
			}
			fmt.Fprintf(w, `<html><head><title>Home</title></head><body><p>All about %s.</p>%s</body></html>`, topic, links.String())
			return
		}
		fmt.Fprintf(w, `<html><head><title>Page %s</title></head><body><p>This page explains %s in detail, with %s examples.</p></body></html>`,
			strings.TrimPrefix(r.URL.Path, "/p"), topic, topic)
	})
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func testCrawlOptions() crawl.WebOptions {
	opts := crawl.DefaultWebOptions()
	opts.Delay = time.Millisecond
	opts.MaxPages = 50
	return opts
}

// served builds an index of the site, saves it, and starts a server over it.
func served(t *testing.T, site *testSite) (*refresher, *server.Server, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "index")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	idx := index.New(analysis.New())
	_, err := crawl.NewWebCrawler(testCrawlOptions()).Crawl(context.Background(), site.URL+"/", func(d crawl.Doc) error {
		_, err := idx.AddDocument(d.URI, d.Title, d.Body)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := idx.Save(dir); err != nil {
		t.Fatal(err)
	}
	srv := server.New(idx, "127.0.0.1:0", log)
	srv.SetBuiltAt(indexBuiltAt(dir))
	return newRefresher(dir, site.URL+"/", testCrawlOptions(), time.Hour, srv, log), srv, dir
}

func hitsFor(t *testing.T, dir, query string) int {
	t.Helper()
	idx, err := index.Load(dir, analysis.New())
	if err != nil {
		t.Fatalf("load %s: %v", dir, err)
	}
	res, err := search.New(idx).Search(query, search.Options{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	return res.Total
}

func TestRebuildSwapsInTheNewCrawl(t *testing.T) {
	site := newTestSite(t, "goroutines", 5)
	r, srv, dir := served(t, site)
	if hitsFor(t, dir, "goroutines") == 0 {
		t.Fatal("the starting index should know about goroutines")
	}

	// The site changes. Nothing is served differently until a refresh.
	site.topic.Store("generics")
	site.pages.Store(6)
	before := srv.BuiltAt()
	time.Sleep(20 * time.Millisecond)

	docs, err := r.rebuild(context.Background())
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if docs != 7 { // the home page and six more
		t.Errorf("rebuilt index has %d documents, want 7", docs)
	}
	if !srv.BuiltAt().After(before) {
		t.Error("the server did not record the new build time")
	}
	if srv.Documents() != 7 {
		t.Errorf("the server reports %d documents, want 7", srv.Documents())
	}

	// On disk, and after a restart, the new pages are what is there.
	if hitsFor(t, dir, "generics") == 0 || hitsFor(t, dir, "goroutines") != 0 {
		t.Error("the index on disk is not the new crawl")
	}
	for _, leftover := range []string{dir + ".next", dir + ".prev"} {
		if _, err := os.Stat(leftover); err == nil {
			t.Errorf("%s was left behind", leftover)
		}
	}
}

func TestFailedCrawlLeavesTheOldIndexServing(t *testing.T) {
	site := newTestSite(t, "goroutines", 5)
	r, srv, dir := served(t, site)

	site.down.Store(true)
	if _, err := r.rebuild(context.Background()); err == nil {
		t.Fatal("a crawl of a site that is down should fail")
	}
	if srv.Documents() != 6 {
		t.Errorf("the server dropped its index: %d documents", srv.Documents())
	}
	if hitsFor(t, dir, "goroutines") == 0 {
		t.Error("the index on disk was damaged by a failed refresh")
	}
}

func TestSuspiciouslySmallCrawlIsRefused(t *testing.T) {
	site := newTestSite(t, "goroutines", 9)
	r, srv, dir := served(t, site) // 10 documents

	// The site starts serving almost nothing: a stub, not a refresh.
	site.pages.Store(1)
	if _, err := r.rebuild(context.Background()); err == nil || !strings.Contains(err.Error(), "thrown away") {
		t.Fatalf("a crawl that found 2 pages against 10 was accepted: %v", err)
	}
	if srv.Documents() != 10 || hitsFor(t, dir, "goroutines") == 0 {
		t.Error("a refused crawl must leave the served index untouched")
	}
}

func TestRecoverIndexDirUndoesAnInterruptedSwap(t *testing.T) {
	base := filepath.Join(t.TempDir(), "index")

	// Crashed after moving the old index aside, before moving the new one in.
	if err := os.MkdirAll(base+".prev", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base+".prev", "marker"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(base+".next", 0o755); err != nil {
		t.Fatal(err)
	}

	recoverIndexDir(base)
	if _, err := os.Stat(filepath.Join(base, "marker")); err != nil {
		t.Error("the old index was not put back")
	}
	for _, gone := range []string{base + ".prev", base + ".next"} {
		if _, err := os.Stat(gone); err == nil {
			t.Errorf("%s was not cleaned up", gone)
		}
	}
}

func TestPageAndStatsSayWhenTheIndexWasBuilt(t *testing.T) {
	site := newTestSite(t, "goroutines", 3)
	r, srv, _ := served(t, site)
	srv.SetRefresh(7*24*time.Hour, "https://go.dev/blog/")
	_ = r

	for _, path := range []string{"/", "/api/stats"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		body := rec.Body.String()
		want := "crawled again every week"
		if path == "/api/stats" {
			want = `"refresh_every":"168h0m0s"`
		}
		if !strings.Contains(body, want) {
			t.Errorf("GET %s does not say %q:\n%s", path, want, body)
		}
	}
}

// Searches that arrive while the index is being replaced must all succeed:
// the swap is one pointer store, and a search keeps the index it started on.
// Run with -race, which is what makes this test mean something.
func TestSearchesDuringARefreshAllSucceed(t *testing.T) {
	site := newTestSite(t, "goroutines", 20)
	r, srv, _ := served(t, site)

	stop := make(chan struct{})
	var failures, searches atomic.Int32
	done := make(chan struct{})
	for i := 0; i < 4; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for {
				select {
				case <-stop:
					return
				default:
				}
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?q=goroutines", nil))
				searches.Add(1)
				if rec.Code != http.StatusOK {
					failures.Add(1)
				}
			}
		}()
	}

	for i := 0; i < 5; i++ {
		site.topic.Store(fmt.Sprintf("goroutines round %d", i))
		if _, err := r.rebuild(context.Background()); err != nil {
			t.Errorf("rebuild %d: %v", i, err)
		}
	}
	close(stop)
	for i := 0; i < 4; i++ {
		<-done
	}
	if failures.Load() != 0 {
		t.Errorf("%d of %d searches failed during refreshes", failures.Load(), searches.Load())
	}
	if searches.Load() == 0 {
		t.Error("no searches ran")
	}
}

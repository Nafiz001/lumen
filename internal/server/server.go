// Package server exposes the index over HTTP: a JSON API and a search page.
//
// It binds to localhost by default and has no authentication, which is a
// deliberate pairing rather than an omission. This is a private search engine:
// the index is of your own files, it runs on your own machine, and nothing
// leaves it. Adding a login to a service reachable only from the machine it
// runs on would be security theatre; binding to a public interface is the thing
// that would actually need one, and that requires passing an address that says
// so.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Nafiz001/lumen/internal/index"
	"github.com/Nafiz001/lumen/internal/search"
)

// Server serves search over HTTP.
type Server struct {
	idx      *index.Index
	searcher *search.Searcher
	log      *slog.Logger
	addr     string

	mu sync.Mutex
	ln net.Listener
	// queries counts searches served, shown on the status page.
	queries int64
}

// New builds a server over an index.
func New(idx *index.Index, addr string, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	return &Server{
		idx:      idx,
		searcher: search.New(idx),
		log:      logger,
		addr:     addr,
	}
}

// Listen binds without serving, so a test can learn the port.
func (s *Server) Listen() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.addr, err)
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	return nil
}

// Addr returns the bound address.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// Handler builds the routing table.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleSearchPage)
	mux.HandleFunc("/api/search", s.handleAPISearch)
	mux.HandleFunc("/api/stats", s.handleAPIStats)
	mux.HandleFunc("/api/document/", s.handleAPIDocument)
	return s.withLogging(mux)
}

// Serve runs until the context is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	s.mu.Lock()
	bound := s.ln != nil
	s.mu.Unlock()
	if !bound {
		if err := s.Listen(); err != nil {
			return err
		}
	}

	server := &http.Server{
		Handler: s.Handler(),
		// A search request that has not finished in thirty seconds is not
		// going to; the timeouts stop a slow client from holding a connection
		// open indefinitely.
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	stats := s.idx.Stats()
	s.log.Info("lumen ready",
		"addr", s.Addr().String(),
		"documents", stats.Documents,
		"terms", stats.Terms)

	err := server.Serve(s.ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.log.Debug("request",
			"method", r.Method,
			"path", r.URL.Path,
			"query", r.URL.RawQuery,
			"took", time.Since(start).Round(time.Microsecond))
	})
}

// --- JSON API ---

func (s *Server) handleAPISearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the q parameter is required"})
		return
	}

	opts := search.DefaultOptions()
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			// Capped, so a client cannot ask for the entire corpus in one
			// response and make the server materialise it.
			opts.Limit = min(n, 100)
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			opts.Offset = n
		}
	}
	opts.Explain = r.URL.Query().Get("explain") == "true"

	start := time.Now()
	results, err := s.searcher.Search(query, opts)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	s.mu.Lock()
	s.queries++
	s.mu.Unlock()

	// Highlight markers are control characters internally; the API returns
	// them as <mark> so a browser client can render them directly.
	for i := range results.Hits {
		results.Hits[i].Snippet = search.RenderSnippet(results.Hits[i].Snippet, "<mark>", "</mark>")
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"query":   results.Query,
		"parsed":  results.Parsed,
		"total":   results.Total,
		"hits":    results.Hits,
		"took_ms": float64(time.Since(start).Microseconds()) / 1000,
	})
}

func (s *Server) handleAPIStats(w http.ResponseWriter, r *http.Request) {
	stats := s.idx.Stats()
	s.mu.Lock()
	queries := s.queries
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"documents":      stats.Documents,
		"terms":          stats.Terms,
		"postings":       stats.Postings,
		"average_length": stats.AverageLength,
		"queries_served": queries,
	})
}

func (s *Server) handleAPIDocument(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimPrefix(r.URL.Path, "/api/document/")
	id, err := strconv.ParseUint(raw, 10, 32)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid document id"})
		return
	}
	doc, ok := s.idx.Document(index.DocID(id))
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no such document"})
		return
	}
	writeJSON(w, http.StatusOK, doc)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// --- the search page ---

func (s *Server) handleSearchPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	query := r.URL.Query().Get("q")
	page := 0
	if v := r.URL.Query().Get("p"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			page = n
		}
	}

	var results *search.Results
	var elapsed time.Duration
	var searchErr error

	if query != "" {
		// Explain is on so each result can show where its score came from.
		opts := search.Options{Limit: perPage, Offset: page * perPage, SnippetLength: 240, Explain: true}
		start := time.Now()
		results, searchErr = s.searcher.Search(query, opts)
		elapsed = time.Since(start)

		s.mu.Lock()
		s.queries++
		s.mu.Unlock()
	}

	var out bytes.Buffer
	if err := pageTemplate.Execute(&out, s.buildPage(query, page, results, elapsed, searchErr)); err != nil {
		s.log.Error("render page", "err", err)
		http.Error(w, "could not render the page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(out.Bytes())
}

func displayTitle(hit search.Result) string {
	if strings.TrimSpace(hit.Title) != "" {
		return hit.Title
	}
	return hit.URI
}

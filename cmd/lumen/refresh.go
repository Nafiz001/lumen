package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/Nafiz001/lumen/internal/analysis"
	"github.com/Nafiz001/lumen/internal/crawl"
	"github.com/Nafiz001/lumen/internal/index"
	"github.com/Nafiz001/lumen/internal/server"
)

// A refresher keeps a served index from going stale. On a schedule it crawls
// the seed again into a side directory, checks the result, and only then
// replaces what is being served: the running server switches over atomically,
// so a search never sees a half-built index, and a crawl that fails or comes
// back suspiciously small leaves the old index exactly where it was.
type refresher struct {
	indexDir string
	seed     string
	opts     crawl.WebOptions
	every    time.Duration
	srv      *server.Server
	log      *slog.Logger

	// keepAtLeast is the smallest share of the current document count a new
	// crawl must reach to replace it. A site that is down, rate-limiting the
	// crawler or serving errors would otherwise swap a full index for a stub.
	keepAtLeast float64
	// retryAfter is how long to wait after a failed attempt.
	retryAfter time.Duration
}

func newRefresher(indexDir, seed string, opts crawl.WebOptions, every time.Duration, srv *server.Server, log *slog.Logger) *refresher {
	return &refresher{
		indexDir: indexDir, seed: seed, opts: opts, every: every, srv: srv, log: log,
		keepAtLeast: 0.5,
		retryAfter:  time.Hour,
	}
}

// run blocks until the context ends, rebuilding whenever the index is older
// than the refresh interval.
func (r *refresher) run(ctx context.Context) {
	wait := time.Until(r.srv.BuiltAt().Add(r.every))
	// A server that has just started, with an index already overdue, waits a
	// minute rather than crawling in the middle of its own start-up.
	if wait < time.Minute {
		wait = time.Minute
	}
	r.log.Info("index refresh scheduled", "seed", r.seed, "every", r.every.String(), "next_in", wait.Round(time.Second).String())

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		start := time.Now()
		r.log.Info("refreshing the index", "seed", r.seed)
		docs, err := r.rebuild(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			r.log.Error("refresh failed; still serving the previous index", "err", err, "retry_in", r.retryAfter.String())
			wait = r.retryAfter
		default:
			r.log.Info("index refreshed", "documents", docs, "took", time.Since(start).Round(time.Second).String())
			wait = r.every
		}
	}
}

// rebuild crawls the seed into <index>.next, checks it, then swaps it in. It
// returns the number of documents now being served.
func (r *refresher) rebuild(ctx context.Context) (int, error) {
	next := r.indexDir + ".next"
	prev := r.indexDir + ".prev"
	_ = os.RemoveAll(next)
	defer os.RemoveAll(next)

	fresh := index.New(analysis.New())
	crawler := crawl.NewWebCrawler(r.opts)
	_, err := crawler.Crawl(ctx, r.seed, func(doc crawl.Doc) error {
		if fresh.HasURI(doc.URI) {
			return nil
		}
		if _, err := fresh.AddDocument(doc.URI, doc.Title, doc.Body); err != nil {
			return fmt.Errorf("index %s: %w", doc.URI, err)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("crawl %s: %w", r.seed, err)
	}

	got, have := fresh.Stats().Documents, r.srv.Documents()
	if floor := int(float64(have) * r.keepAtLeast); got == 0 || got < floor {
		return 0, fmt.Errorf("the new crawl found %d pages against the %d being served, so it was thrown away", got, have)
	}

	if err := fresh.Save(next); err != nil {
		return 0, fmt.Errorf("save the new index: %w", err)
	}
	// Loading it back proves what is on disk is what will be served after a
	// restart, before anything is replaced.
	loaded, err := index.Load(next, analysis.New())
	if err != nil {
		return 0, fmt.Errorf("the saved index does not load: %w", err)
	}

	// Two renames, so a crash between them leaves a recoverable state:
	// recoverIndexDir puts <index>.prev back if <index> is missing.
	_ = os.RemoveAll(prev)
	if err := os.Rename(r.indexDir, prev); err != nil {
		return 0, fmt.Errorf("move the old index aside: %w", err)
	}
	if err := os.Rename(next, r.indexDir); err != nil {
		if restoreErr := os.Rename(prev, r.indexDir); restoreErr != nil {
			return 0, errors.Join(err, fmt.Errorf("and could not restore the old index: %w", restoreErr))
		}
		return 0, fmt.Errorf("move the new index into place: %w", err)
	}

	r.srv.Swap(loaded, time.Now())
	_ = os.RemoveAll(prev)
	return loaded.Stats().Documents, nil
}

// recoverIndexDir finishes or undoes a refresh that was interrupted between
// its two renames, and clears leftovers, so the server never starts without
// an index that is in fact there.
func recoverIndexDir(dir string) {
	if _, err := os.Stat(dir); errors.Is(err, os.ErrNotExist) {
		if _, err := os.Stat(dir + ".prev"); err == nil {
			_ = os.Rename(dir+".prev", dir)
		}
	}
	_ = os.RemoveAll(dir + ".next")
	_ = os.RemoveAll(dir + ".prev")
}

// indexBuiltAt is when the index on disk was last written: its newest file.
func indexBuiltAt(dir string) time.Time {
	var newest time.Time
	entries, err := os.ReadDir(dir)
	if err != nil {
		return newest
	}
	for _, e := range entries {
		if info, err := os.Stat(filepath.Join(dir, e.Name())); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
}

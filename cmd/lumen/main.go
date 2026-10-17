// Command lumen is a private search engine: it indexes your files or a website
// and answers queries locally.
//
// The subcommands split along the two things a search engine does. "index" and
// "crawl" build an index and write it to disk; "search" and "serve" read one
// back and answer questions about it. Keeping them separate means indexing a
// large tree happens once, at a time you choose, rather than on every startup.
//
//	lumen index ./notes            build an index from a directory
//	lumen crawl https://example.com    build one by crawling a site
//	lumen search "query"           query from the terminal
//	lumen serve                    query from a browser
//	lumen stats                    what the index contains
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Nafiz001/lumen/internal/analysis"
	"github.com/Nafiz001/lumen/internal/crawl"
	"github.com/Nafiz001/lumen/internal/index"
	"github.com/Nafiz001/lumen/internal/search"
	"github.com/Nafiz001/lumen/internal/server"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "index":
		err = cmdIndex(os.Args[2:])
	case "crawl":
		err = cmdCrawl(os.Args[2:])
	case "search":
		err = cmdSearch(os.Args[2:])
	case "serve":
		err = cmdServe(os.Args[2:])
	case "stats":
		err = cmdStats(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Printf("lumen %s\n", version)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "lumen: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "lumen: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `lumen - a private search engine

usage: lumen <command> [flags]

commands:
  index <dir>     build an index from a directory of files
  crawl <url>     build an index by crawling a website
  search <query>  search the index from the terminal
  serve           serve the index over HTTP with a web UI
  stats           show what the index contains
  version         print the version

Every command takes -index <dir> to choose where the index lives
(default: ./lumen-index). Run "lumen <command> -h" for its flags.

query syntax:
  go concurrency    both words, ranked by relevance
  "exact phrase"    the words adjacent and in order
  +required         the document must contain this
  -excluded         the document must not contain this
  title:golang      scoped to the title field
`)
}

// --- index ---

func cmdIndex(args []string) error {
	fs := flag.NewFlagSet("index", flag.ExitOnError)
	indexDir := fs.String("index", "lumen-index", "where to write the index")
	maxSize := fs.Int64("max-size", 10<<20, "skip files larger than this many bytes")
	extensions := fs.String("ext", "", "comma-separated extensions to index (default: a text-like set)")
	includeHidden := fs.Bool("hidden", false, "index dotfiles and dot-directories")
	appendTo := fs.Bool("append", false, "add to an existing index instead of replacing it")
	quiet := fs.Bool("quiet", false, "only print the summary")
	rest := parseFlags(fs, args)

	if len(rest) < 1 {
		return fmt.Errorf("index needs a directory: lumen index ./notes")
	}
	root := rest[0]
	if err := crawl.ValidateRoot(root); err != nil {
		return err
	}

	idx, err := openOrCreate(*indexDir, *appendTo)
	if err != nil {
		return err
	}

	opts := crawl.DefaultFileOptions()
	opts.MaxFileSize = *maxSize
	opts.SkipHidden = !*includeHidden
	if *extensions != "" {
		opts.Extensions = normalizeExtensions(*extensions)
	}

	start := time.Now()
	skipped := 0
	crawler := crawl.NewFileCrawler(opts)

	indexed, err := crawler.Crawl(root, func(doc crawl.Doc) error {
		// Re-indexing the same URI would double every term's frequency for that
		// document, which quietly corrupts the ranking rather than failing.
		if idx.HasURI(doc.URI) {
			skipped++
			return nil
		}
		if _, err := idx.AddDocument(doc.URI, doc.Title, doc.Body); err != nil {
			return fmt.Errorf("index %s: %w", doc.URI, err)
		}
		if !*quiet {
			fmt.Printf("  %s\n", doc.URI)
		}
		return nil
	})
	if err != nil {
		return err
	}

	if err := idx.Save(*indexDir); err != nil {
		return err
	}

	stats := idx.Stats()
	fmt.Printf("\nindexed %d documents (%d already present) in %s\n",
		indexed-skipped, skipped, time.Since(start).Round(time.Millisecond))
	fmt.Printf("index: %s -- %d documents, %d terms, %d postings\n",
		*indexDir, stats.Documents, stats.Terms, stats.Postings)
	return nil
}

// --- crawl ---

func cmdCrawl(args []string) error {
	fs := flag.NewFlagSet("crawl", flag.ExitOnError)
	indexDir := fs.String("index", "lumen-index", "where to write the index")
	maxPages := fs.Int("max-pages", 200, "stop after this many pages")
	maxDepth := fs.Int("max-depth", 3, "how many links deep to follow")
	delay := fs.Duration("delay", time.Second, "wait between requests to the same host")
	allHosts := fs.Bool("all-hosts", false, "follow links off the starting host")
	ignoreRobots := fs.Bool("ignore-robots", false, "do not honour robots.txt")
	appendTo := fs.Bool("append", false, "add to an existing index instead of replacing it")
	quiet := fs.Bool("quiet", false, "only print the summary")
	rest := parseFlags(fs, args)

	if len(rest) < 1 {
		return fmt.Errorf("crawl needs a URL: lumen crawl https://example.com")
	}
	seed := rest[0]

	if *ignoreRobots {
		// Said out loud, because the flag makes the tool impolite and the person
		// running it owns that choice.
		fmt.Fprintln(os.Stderr,
			"warning: ignoring robots.txt. You are responsible for what this crawler asks of a server.")
	}

	idx, err := openOrCreate(*indexDir, *appendTo)
	if err != nil {
		return err
	}

	opts := crawl.DefaultWebOptions()
	opts.MaxPages = *maxPages
	opts.MaxDepth = *maxDepth
	opts.Delay = *delay
	opts.SameHostOnly = !*allHosts
	opts.IgnoreRobots = *ignoreRobots

	crawler := crawl.NewWebCrawler(opts)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	start := time.Now()
	fetched, err := crawler.Crawl(ctx, seed, func(doc crawl.Doc) error {
		if idx.HasURI(doc.URI) {
			return nil
		}
		if _, err := idx.AddDocument(doc.URI, doc.Title, doc.Body); err != nil {
			return fmt.Errorf("index %s: %w", doc.URI, err)
		}
		if !*quiet {
			fmt.Printf("  %s\n", doc.URI)
		}
		return nil
	})
	// An interrupted crawl still has pages worth keeping, so the index is saved
	// before the error is reported rather than instead of it.
	if saveErr := idx.Save(*indexDir); saveErr != nil {
		return saveErr
	}
	if err != nil && ctx.Err() == nil {
		return err
	}

	stats := idx.Stats()
	fmt.Printf("\ncrawled %d pages in %s\n", fetched, time.Since(start).Round(time.Millisecond))
	fmt.Printf("index: %s -- %d documents, %d terms, %d postings\n",
		*indexDir, stats.Documents, stats.Terms, stats.Postings)
	return nil
}

// --- search ---

func cmdSearch(args []string) error {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	indexDir := fs.String("index", "lumen-index", "which index to search")
	limit := fs.Int("n", 10, "how many results to show")
	offset := fs.Int("offset", 0, "skip this many results")
	explain := fs.Bool("explain", false, "show how each score was reached")
	noColor := fs.Bool("no-color", false, "do not highlight matched terms")
	rest := parseFlags(fs, args)

	if len(rest) < 1 {
		return fmt.Errorf(`search needs a query: lumen search "your terms"`)
	}
	// The words are rejoined so an unquoted query still works: a shell splits
	// `lumen search go channels` into two arguments, and the user meant one
	// query.
	query := strings.Join(rest, " ")

	idx, err := index.Load(*indexDir, analysis.New())
	if err != nil {
		return fmt.Errorf("%w (build one first with: lumen index <dir>)", err)
	}

	searcher := search.New(idx)
	start := time.Now()
	results, err := searcher.Search(query, search.Options{
		Limit:   *limit,
		Offset:  *offset,
		Explain: *explain,
	})
	if err != nil {
		return err
	}
	elapsed := time.Since(start)

	if results.Total == 0 {
		fmt.Printf("no results for %q\n", query)
		return nil
	}

	fmt.Printf("%d result%s in %.2fms", results.Total, plural(results.Total),
		float64(elapsed.Microseconds())/1000)
	if results.Parsed != query {
		fmt.Printf(" (read as: %s)", results.Parsed)
	}
	fmt.Print("\n\n")

	open, close := "\x1b[1;33m", "\x1b[0m"
	if *noColor || !isTerminal() {
		open, close = "", ""
	}

	for i, hit := range results.Hits {
		title := hit.Title
		if strings.TrimSpace(title) == "" {
			title = hit.URI
		}
		fmt.Printf("%d. %s\n", *offset+i+1, title)
		fmt.Printf("   %s  (score %.4f)\n", hit.URI, hit.Score)
		if hit.Snippet != "" {
			fmt.Printf("   %s\n", search.RenderSnippet(hit.Snippet, open, close))
		}
		if *explain {
			for _, e := range hit.Explanations {
				fmt.Printf("     %-18s tf=%-4d df=%-5d idf=%.3f  ->  %.4f\n",
					e.Term, e.TermFrequency, e.DocFrequency, e.IDF, e.Contribution)
			}
		}
		fmt.Println()
	}
	return nil
}

// --- serve ---

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	indexDir := fs.String("index", "lumen-index", "which index to serve")
	addr := fs.String("addr", "127.0.0.1:8080", "address to listen on")
	verbose := fs.Bool("v", false, "log every request")
	parseFlags(fs, args)

	idx, err := index.Load(*indexDir, analysis.New())
	if err != nil {
		return fmt.Errorf("%w (build one first with: lumen index <dir>)", err)
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	// Binding to something other than a loopback address makes the index
	// reachable from the network, and the server has no authentication. Worth
	// one line of warning rather than a silent surprise.
	if !isLoopback(*addr) {
		fmt.Fprintf(os.Stderr,
			"warning: %s is not a loopback address; anyone who can reach it can search this index\n", *addr)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := server.New(idx, *addr, logger)
	if err := srv.Listen(); err != nil {
		return err
	}
	fmt.Printf("lumen serving %s at http://%s\n", *indexDir, srv.Addr())
	return srv.Serve(ctx)
}

// --- stats ---

func cmdStats(args []string) error {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	indexDir := fs.String("index", "lumen-index", "which index to describe")
	top := fs.Int("top", 15, "how many of the most common terms to list")
	parseFlags(fs, args)

	idx, err := index.Load(*indexDir, analysis.New())
	if err != nil {
		return err
	}

	stats := idx.Stats()
	size, _ := dirSize(*indexDir)

	fmt.Printf("index:            %s\n", *indexDir)
	fmt.Printf("documents:        %d\n", stats.Documents)
	fmt.Printf("distinct terms:   %d\n", stats.Terms)
	fmt.Printf("postings:         %d\n", stats.Postings)
	fmt.Printf("average length:   %.1f tokens\n", stats.AverageLength)
	fmt.Printf("on disk:          %.2f MB\n", float64(size)/(1<<20))
	if stats.Documents > 0 {
		fmt.Printf("bytes/document:   %.0f\n", float64(size)/float64(stats.Documents))
	}

	if *top > 0 {
		fmt.Printf("\nmost common terms\n")
		for _, tc := range idx.TopTerms(*top) {
			fmt.Printf("  %-22s %d documents\n", tc.Term, tc.DocFrequency)
		}
	}
	return nil
}

// --- helpers ---

// parseFlags parses a flag set and returns the positional arguments, allowing
// flags to appear after them.
//
// Go's flag package stops at the first non-flag argument, so "lumen index .
// -quiet" would parse zero flags and silently ignore -quiet -- the worst kind
// of failure, because the command still succeeds and does the wrong thing.
// Every other tool a person uses accepts flags in either position, so this
// re-parses what is left after each positional until none remain.
func parseFlags(fs *flag.FlagSet, args []string) []string {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return positional
		}
		if fs.NArg() == 0 {
			return positional
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// openOrCreate loads an existing index when appending, and starts an empty one
// otherwise.
func openOrCreate(dir string, appendTo bool) (*index.Index, error) {
	if !appendTo {
		return index.New(analysis.New()), nil
	}
	idx, err := index.Load(dir, analysis.New())
	if err != nil {
		if os.IsNotExist(err) {
			return index.New(analysis.New()), nil
		}
		return nil, err
	}
	return idx, nil
}

func normalizeExtensions(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(strings.ToLower(part))
		if part == "" {
			continue
		}
		if !strings.HasPrefix(part, ".") {
			part = "." + part
		}
		out = append(out, part)
	}
	return out
}

func dirSize(dir string) (int64, error) {
	var total int64
	err := filepath.Walk(dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return total, err
}

func isLoopback(addr string) bool {
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

// isTerminal reports whether stdout is a terminal, so that piping the output
// into a file or a pager does not fill it with escape sequences.
func isTerminal() bool {
	info, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

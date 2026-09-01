// Package crawl gathers documents for the index, from a local directory or
// from a website.
//
// The two sources answer the two senses of a private search engine: search your
// own files, or run your own index of a site rather than asking someone else's
// engine about it. Both produce the same thing -- a URI, a title and a body --
// so the index does not know or care which it came from.
package crawl

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Doc is one gathered document.
type Doc struct {
	URI   string
	Title string
	Body  string
}

// FileOptions configure a directory crawl.
type FileOptions struct {
	// Extensions to index. Empty means the default text-like set.
	Extensions []string
	// MaxFileSize skips anything larger, in bytes.
	MaxFileSize int64
	// SkipHidden ignores dotfiles and dot-directories.
	SkipHidden bool
	// SkipDirs are directory names to ignore wherever they appear.
	SkipDirs []string
}

// DefaultFileOptions returns settings that avoid the obvious traps.
func DefaultFileOptions() FileOptions {
	return FileOptions{
		Extensions: []string{
			".txt", ".md", ".markdown", ".rst", ".org",
			".go", ".py", ".js", ".ts", ".java", ".c", ".h", ".cpp", ".rs",
			".html", ".htm", ".xml", ".json", ".yaml", ".yml", ".toml",
			".css", ".sql", ".sh", ".log", ".csv",
		},
		// 10 MB. Above this a "text file" is almost always generated -- a log
		// dump or a minified bundle -- and indexing it fills the dictionary
		// with terms nobody will search for.
		MaxFileSize: 10 << 20,
		SkipHidden:  true,
		// Directories that are large, generated, and never what someone means
		// to search.
		SkipDirs: []string{
			".git", "node_modules", "vendor", "__pycache__", ".venv", "venv",
			"target", "dist", "build", ".pytest_cache", ".idea", ".vscode",
		},
	}
}

// FileCrawler walks a directory tree.
type FileCrawler struct {
	opts       FileOptions
	extensions map[string]bool
	skipDirs   map[string]bool
}

func NewFileCrawler(opts FileOptions) *FileCrawler {
	if len(opts.Extensions) == 0 {
		opts.Extensions = DefaultFileOptions().Extensions
	}
	if opts.MaxFileSize <= 0 {
		opts.MaxFileSize = DefaultFileOptions().MaxFileSize
	}

	c := &FileCrawler{
		opts:       opts,
		extensions: make(map[string]bool, len(opts.Extensions)),
		skipDirs:   make(map[string]bool, len(opts.SkipDirs)),
	}
	for _, e := range opts.Extensions {
		c.extensions[strings.ToLower(e)] = true
	}
	for _, d := range opts.SkipDirs {
		c.skipDirs[d] = true
	}
	return c
}

// Crawl walks root and calls emit for each document it finds.
//
// Documents are handed over one at a time rather than collected into a slice,
// so indexing a large tree does not require holding the whole corpus in memory
// twice.
func (c *FileCrawler) Crawl(root string, emit func(Doc) error) (int, error) {
	count := 0

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable file or directory is skipped rather than fatal: a
			// permission error deep in a tree should not abandon the crawl.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		name := d.Name()
		if d.IsDir() {
			if path == root {
				return nil
			}
			if c.skipDirs[name] || (c.opts.SkipHidden && strings.HasPrefix(name, ".")) {
				return fs.SkipDir
			}
			return nil
		}

		if c.opts.SkipHidden && strings.HasPrefix(name, ".") {
			return nil
		}
		if !c.extensions[strings.ToLower(filepath.Ext(name))] {
			return nil
		}

		info, err := d.Info()
		if err != nil || info.Size() > c.opts.MaxFileSize || info.Size() == 0 {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		// A file with an indexable extension can still be binary -- a .log that
		// is actually a core dump, say. Indexing binary produces garbage terms,
		// so it is checked rather than assumed.
		if !utf8.Valid(data) || looksBinary(data) {
			return nil
		}

		body := string(data)
		if ext := strings.ToLower(filepath.Ext(name)); ext == ".html" || ext == ".htm" {
			body = ExtractText(body)
		}

		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			relative = path
		}

		doc := Doc{
			URI:   filepath.ToSlash(relative),
			Title: titleFor(name, body),
			Body:  body,
		}
		if err := emit(doc); err != nil {
			return err
		}
		count++
		return nil
	})

	return count, err
}

// titleFor picks a display title: a Markdown heading if there is one at the
// top, otherwise the filename.
func titleFor(name, body string) string {
	lines := strings.SplitN(body, "\n", 10)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "# ") {
			return strings.TrimSpace(trimmed[2:])
		}
		if trimmed != "" {
			break // only look at the very top; a heading further down is a section
		}
	}
	return name
}

// looksBinary reports whether the content is probably not text.
//
// A NUL byte in the first few kilobytes is the cheap and reliable signal --
// text almost never contains one, and binary formats almost always do early.
func looksBinary(data []byte) bool {
	limit := len(data)
	if limit > 8000 {
		limit = 8000
	}
	for i := 0; i < limit; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

// ValidateRoot checks a directory exists before a crawl starts.
func ValidateRoot(root string) error {
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", root, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", root)
	}
	return nil
}

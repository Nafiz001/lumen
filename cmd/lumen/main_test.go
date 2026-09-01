package main

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// --- flag parsing ---

// parseFlags exists because Go's flag package stops at the first positional
// argument. These tests pin the behaviour that "lumen index . -quiet" must
// honour -quiet, since the failure mode is silent: the command succeeds while
// ignoring what it was told.
func TestParseFlagsAcceptsFlagsAfterPositionals(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		wantPositional []string
		wantIndex      string
		wantQuiet      bool
	}{
		{
			name:           "flags before the positional",
			args:           []string{"-index", "/tmp/i", "-quiet", "./notes"},
			wantPositional: []string{"./notes"},
			wantIndex:      "/tmp/i",
			wantQuiet:      true,
		},
		{
			name:           "flags after the positional",
			args:           []string{"./notes", "-index", "/tmp/i", "-quiet"},
			wantPositional: []string{"./notes"},
			wantIndex:      "/tmp/i",
			wantQuiet:      true,
		},
		{
			name:           "flags on both sides",
			args:           []string{"-quiet", "./notes", "-index", "/tmp/i"},
			wantPositional: []string{"./notes"},
			wantIndex:      "/tmp/i",
			wantQuiet:      true,
		},
		{
			name:           "no flags at all",
			args:           []string{"./notes"},
			wantPositional: []string{"./notes"},
			wantIndex:      "default",
			wantQuiet:      false,
		},
		{
			name:           "several positionals, as an unquoted query",
			args:           []string{"go", "concurrency", "-quiet"},
			wantPositional: []string{"go", "concurrency"},
			wantIndex:      "default",
			wantQuiet:      true,
		},
		{
			name:           "nothing",
			args:           []string{},
			wantPositional: nil,
			wantIndex:      "default",
			wantQuiet:      false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.SetOutput(os.NewFile(0, os.DevNull))
			indexDir := fs.String("index", "default", "")
			quiet := fs.Bool("quiet", false, "")

			got := parseFlags(fs, tc.args)

			if !reflect.DeepEqual(got, tc.wantPositional) {
				t.Errorf("positional = %v, want %v", got, tc.wantPositional)
			}
			if *indexDir != tc.wantIndex {
				t.Errorf("-index = %q, want %q", *indexDir, tc.wantIndex)
			}
			if *quiet != tc.wantQuiet {
				t.Errorf("-quiet = %v, want %v", *quiet, tc.wantQuiet)
			}
		})
	}
}

// --- helpers ---

func TestNormalizeExtensions(t *testing.T) {
	tests := []struct {
		raw  string
		want []string
	}{
		// A leading dot is added, because "-ext go,md" is what a person types.
		{"go,md", []string{".go", ".md"}},
		{".go,.md", []string{".go", ".md"}},
		{"GO, MD ", []string{".go", ".md"}},
		{"go,,md", []string{".go", ".md"}},
		{"", nil},
		{",", nil},
	}

	for _, tc := range tests {
		if got := normalizeExtensions(tc.raw); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("normalizeExtensions(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestIsLoopback(t *testing.T) {
	// This decides whether serve warns that the index is reachable from the
	// network, so a wrong answer either cries wolf or stays silent when it
	// matters.
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"localhost:8080", true},
		{"[::1]:8080", true},
		{":8080", true},
		{"127.0.0.53:8080", true},
		{"0.0.0.0:8080", false},
		{"192.168.1.5:8080", false},
		{"10.0.0.1:80", false},
	}

	for _, tc := range tests {
		if got := isLoopback(tc.addr); got != tc.want {
			t.Errorf("isLoopback(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestPlural(t *testing.T) {
	for _, tc := range []struct {
		n    int
		want string
	}{{0, "s"}, {1, ""}, {2, "s"}} {
		if got := plural(tc.n); got != tc.want {
			t.Errorf("plural(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestDirSize(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("12345"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "b"), []byte("123"), 0o644); err != nil {
		t.Fatal(err)
	}

	size, err := dirSize(dir)
	if err != nil {
		t.Fatal(err)
	}
	if size != 8 {
		t.Errorf("dirSize = %d, want 8", size)
	}
}

// --- the commands, end to end ---

// run captures stdout while a command executes, so a test can assert on what
// the user would see without the output polluting the test log.
func run(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	saved := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w

	done := make(chan string)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			sb.Write(buf[:n])
			if err != nil {
				break
			}
		}
		done <- sb.String()
	}()

	runErr := fn()

	w.Close()
	os.Stdout = saved
	return <-done, runErr
}

func TestIndexThenSearch(t *testing.T) {
	corpus := t.TempDir()
	files := map[string]string{
		"go.md":   "# Concurrency in Go\n\nGoroutines are cheap threads. Channels connect them.",
		"rust.md": "# Ownership in Rust\n\nRust manages memory without a garbage collector.",
		"bin.log": "header\x00\x00binary",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(corpus, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	indexDir := filepath.Join(t.TempDir(), "idx")

	out, err := run(t, func() error {
		return cmdIndex([]string{corpus, "-index", indexDir, "-quiet"})
	})
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if !strings.Contains(out, "indexed 2 documents") {
		t.Errorf("index summary was %q, want 2 documents (the binary one skipped)", out)
	}

	out, err = run(t, func() error {
		return cmdSearch([]string{"goroutines", "-index", indexDir, "-no-color"})
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(out, "Concurrency in Go") {
		t.Errorf("search output missing the matching document: %q", out)
	}
	if strings.Contains(out, "Ownership in Rust") {
		t.Errorf("search returned an unrelated document: %q", out)
	}

	out, err = run(t, func() error {
		return cmdStats([]string{"-index", indexDir, "-top", "3"})
	})
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	for _, want := range []string{"documents:", "distinct terms:", "most common terms"} {
		if !strings.Contains(out, want) {
			t.Errorf("stats output missing %q: %q", want, out)
		}
	}
}

func TestSearchReportsNoResults(t *testing.T) {
	corpus := t.TempDir()
	if err := os.WriteFile(filepath.Join(corpus, "a.md"), []byte("some text"), 0o644); err != nil {
		t.Fatal(err)
	}
	indexDir := filepath.Join(t.TempDir(), "idx")

	if _, err := run(t, func() error {
		return cmdIndex([]string{corpus, "-index", indexDir, "-quiet"})
	}); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, func() error {
		return cmdSearch([]string{"xyzzyplugh", "-index", indexDir, "-no-color"})
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(out, "no results") {
		t.Errorf("output = %q, want a 'no results' message", out)
	}
}

func TestAppendAddsToAnExistingIndex(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(first, "a.md"), []byte("alpha content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "b.md"), []byte("beta content"), 0o644); err != nil {
		t.Fatal(err)
	}
	indexDir := filepath.Join(t.TempDir(), "idx")

	if _, err := run(t, func() error {
		return cmdIndex([]string{first, "-index", indexDir, "-quiet"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, func() error {
		return cmdIndex([]string{second, "-index", indexDir, "-quiet", "-append"})
	}); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, func() error { return cmdStats([]string{"-index", indexDir, "-top", "0"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "documents:        2") {
		t.Errorf("appending did not preserve the first index: %q", out)
	}
}

func TestIndexWithoutAppendReplaces(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	for _, f := range []struct{ dir, name string }{{first, "a.md"}, {second, "b.md"}} {
		if err := os.WriteFile(filepath.Join(f.dir, f.name), []byte("some content"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	indexDir := filepath.Join(t.TempDir(), "idx")

	for _, dir := range []string{first, second} {
		if _, err := run(t, func() error {
			return cmdIndex([]string{dir, "-index", indexDir, "-quiet"})
		}); err != nil {
			t.Fatal(err)
		}
	}

	out, err := run(t, func() error { return cmdStats([]string{"-index", indexDir, "-top", "0"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "documents:        1") {
		t.Errorf("indexing without -append did not replace: %q", out)
	}
}

func TestExtFlagLimitsWhatIsIndexed(t *testing.T) {
	corpus := t.TempDir()
	for name, content := range map[string]string{
		"a.md":  "markdown content",
		"b.go":  "package main",
		"c.txt": "plain text",
	} {
		if err := os.WriteFile(filepath.Join(corpus, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	indexDir := filepath.Join(t.TempDir(), "idx")

	out, err := run(t, func() error {
		return cmdIndex([]string{corpus, "-index", indexDir, "-quiet", "-ext", "md"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "indexed 1 documents") {
		t.Errorf("-ext md indexed more than the markdown file: %q", out)
	}
}

func TestCommandsRejectMissingArguments(t *testing.T) {
	if err := cmdIndex(nil); err == nil {
		t.Error("index accepted no directory")
	}
	if err := cmdCrawl(nil); err == nil {
		t.Error("crawl accepted no URL")
	}
	if err := cmdSearch(nil); err == nil {
		t.Error("search accepted no query")
	}
}

func TestCommandsReportAMissingIndex(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	err := cmdSearch([]string{"anything", "-index", missing})
	if err == nil {
		t.Fatal("search accepted an index that does not exist")
	}
	// The message has to say what to do next; "no such file" alone leaves the
	// user guessing that an index is something they were meant to build.
	if !strings.Contains(err.Error(), "lumen index") {
		t.Errorf("error = %q, want it to point at the index command", err)
	}

	if err := cmdIndex([]string{missing, "-index", t.TempDir()}); err == nil {
		t.Error("index accepted a directory that does not exist")
	}
}

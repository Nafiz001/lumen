package index

import (
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nafizahmed/lumen/internal/analysis"
)

// --- postings encoding ---

func TestPostingsRoundTrip(t *testing.T) {
	original := &PostingsList{
		Term: "goroutine",
		Postings: []Posting{
			{DocID: 1, Frequency: 3, Positions: []uint32{0, 7, 42}},
			{DocID: 2, Frequency: 1, Positions: []uint32{15}},
			{DocID: 900, Frequency: 2, Positions: []uint32{3, 3000}},
		},
	}

	decoded, err := DecodePostings(original.Term, original.Encode())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(original, decoded) {
		t.Errorf("round trip changed the list:\n got %+v\nwant %+v", decoded, original)
	}
}

func TestPostingsRoundTripRandom(t *testing.T) {
	// The encoding stores gaps rather than values, so a bug in the delta
	// arithmetic shows up as a corrupted tail rather than a decode error. Only
	// a wide range of inputs catches that reliably.
	rng := rand.New(rand.NewSource(7))

	for trial := 0; trial < 200; trial++ {
		list := &PostingsList{Term: "t"}
		var docID DocID
		for i := 0; i < rng.Intn(40)+1; i++ {
			docID += DocID(rng.Intn(5000) + 1)

			var positions []uint32
			var pos uint32
			for j := 0; j < rng.Intn(12)+1; j++ {
				pos += uint32(rng.Intn(2000) + 1)
				positions = append(positions, pos)
			}
			list.Postings = append(list.Postings, Posting{
				DocID:     docID,
				Frequency: uint32(len(positions)),
				Positions: positions,
			})
		}

		decoded, err := DecodePostings("t", list.Encode())
		if err != nil {
			t.Fatalf("trial %d: decode: %v", trial, err)
		}
		if !reflect.DeepEqual(list, decoded) {
			t.Fatalf("trial %d: round trip changed the list", trial)
		}
	}
}

func TestPostingsEncodingIsSmallerThanFixedWidth(t *testing.T) {
	// The point of delta plus varint is that consecutive document ids differ
	// by small numbers, which fit in one byte. If this ever stops holding, the
	// encoding is costing complexity for nothing.
	list := &PostingsList{Term: "the"}
	for id := DocID(1); id <= 1000; id++ {
		list.Postings = append(list.Postings, Posting{DocID: id, Frequency: 1, Positions: []uint32{uint32(id)}})
	}

	encoded := len(list.Encode())
	// Fixed width would be 4 bytes of id + 4 of frequency + 4 per position.
	fixed := 1000 * 12

	if encoded >= fixed {
		t.Errorf("encoded to %d bytes, no better than %d fixed-width", encoded, fixed)
	}
	t.Logf("1000 postings: %d bytes encoded vs %d fixed-width (%.0f%%)",
		encoded, fixed, 100*float64(encoded)/float64(fixed))
}

func TestDecodePostingsRejectsTruncatedData(t *testing.T) {
	list := &PostingsList{
		Term:     "x",
		Postings: []Posting{{DocID: 1, Frequency: 2, Positions: []uint32{4, 9}}},
	}
	encoded := list.Encode()

	// Truncation must be an error rather than a silently short list; a
	// half-read index that looks valid is worse than one that refuses to load.
	if _, err := DecodePostings("x", encoded[:len(encoded)-1]); err == nil {
		t.Error("decoded a truncated postings list without error")
	}
}

func TestDecodeEmptyPostings(t *testing.T) {
	list := &PostingsList{Term: "unused"}
	decoded, err := DecodePostings("unused", list.Encode())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Postings) != 0 {
		t.Errorf("empty list decoded to %d postings", len(decoded.Postings))
	}
}

// --- intersection and union ---

func list(term string, postings ...Posting) *PostingsList {
	return &PostingsList{Term: term, Postings: postings}
}

func p(id DocID, positions ...uint32) Posting {
	return Posting{DocID: id, Frequency: uint32(len(positions)), Positions: positions}
}

func TestIntersect(t *testing.T) {
	a := list("a", p(1, 0), p(3, 0), p(5, 0), p(7, 0))
	b := list("b", p(3, 1), p(5, 1), p(9, 1))
	c := list("c", p(3, 2), p(5, 2), p(11, 2))

	got := Intersect(a, b, c)
	if len(got) != 2 || got[0].DocID != 3 || got[1].DocID != 5 {
		t.Errorf("Intersect = %v, want documents 3 and 5", got)
	}
}

func TestIntersectEdgeCases(t *testing.T) {
	a := list("a", p(1, 0), p(2, 0))

	if got := Intersect(); len(got) != 0 {
		t.Errorf("Intersect of nothing = %v, want empty", got)
	}
	if got := Intersect(a); len(got) != 2 {
		t.Errorf("Intersect of one list = %v, want it unchanged", got)
	}
	if got := Intersect(a, list("empty")); len(got) != 0 {
		t.Errorf("Intersect with an empty list = %v, want empty", got)
	}
	if got := Intersect(a, list("b", p(9, 0))); len(got) != 0 {
		t.Errorf("Intersect with no overlap = %v, want empty", got)
	}
}

func TestUnion(t *testing.T) {
	a := list("a", p(1, 0), p(4, 0))
	b := list("b", p(2, 0), p(4, 0))

	got := Union(a, b)
	want := []DocID{1, 2, 4}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("Union = %v, want %v (sorted, no duplicates)", got, want)
	}
}

// --- phrase matching ---

func TestPhraseMatch(t *testing.T) {
	tests := []struct {
		name     string
		postings []Posting
		want     bool
	}{
		{
			name:     "adjacent in order",
			postings: []Posting{p(1, 5), p(1, 6)},
			want:     true,
		},
		{
			name: "present but not adjacent",
			// "search ... engine" with a word between is not the phrase.
			postings: []Posting{p(1, 5), p(1, 8)},
			want:     false,
		},
		{
			name:     "adjacent but reversed",
			postings: []Posting{p(1, 6), p(1, 5)},
			want:     false,
		},
		{
			name:     "one occurrence among several matches",
			postings: []Posting{p(1, 2, 9, 40), p(1, 7, 41, 90)},
			want:     true,
		},
		{
			name:     "three word phrase",
			postings: []Posting{p(1, 1), p(1, 2), p(1, 3)},
			want:     true,
		},
		{
			name:     "three word phrase with a gap",
			postings: []Posting{p(1, 1), p(1, 2), p(1, 4)},
			want:     false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := PhraseMatch(tc.postings); got != tc.want {
				t.Errorf("PhraseMatch = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPhrasePositions(t *testing.T) {
	// Two occurrences of the phrase, at 1 and at 40.
	got := PhrasePositions([]Posting{p(1, 1, 20, 40), p(1, 2, 25, 41)})
	want := []uint32{1, 40}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("PhrasePositions = %v, want %v", got, want)
	}
}

// --- the index itself ---

func newTestIndex(t *testing.T) *Index {
	t.Helper()
	idx := New(analysis.New())

	docs := []struct{ uri, title, body string }{
		{"a.md", "Concurrency in Go", "Goroutines are cheap. Channels connect goroutines."},
		{"b.md", "Channels", "A channel carries values between goroutines."},
		{"c.md", "Ownership in Rust", "Rust manages memory without a garbage collector."},
	}
	for _, d := range docs {
		if _, err := idx.AddDocument(d.uri, d.title, d.body); err != nil {
			t.Fatalf("add %s: %v", d.uri, err)
		}
	}
	return idx
}

func TestAddDocument(t *testing.T) {
	idx := newTestIndex(t)

	if got := idx.DocCount(); got != 3 {
		t.Errorf("DocCount = %d, want 3", got)
	}
	if idx.TermCount() == 0 {
		t.Error("the dictionary is empty")
	}
	if idx.AverageLength() <= 0 {
		t.Errorf("AverageLength = %v, want a positive token count", idx.AverageLength())
	}
}

func TestAddDocumentRequiresAURI(t *testing.T) {
	idx := New(analysis.New())
	if _, err := idx.AddDocument("", "title", "body"); err == nil {
		t.Error("accepted a document without a URI")
	}
}

func TestAddDocumentAssignsIncreasingIDs(t *testing.T) {
	idx := New(analysis.New())

	first, _ := idx.AddDocument("a", "", "one")
	second, _ := idx.AddDocument("b", "", "two")

	if first == 0 {
		t.Error("document id 0 was assigned; 0 should stay available as a zero value")
	}
	if second <= first {
		t.Errorf("ids did not increase: %d then %d", first, second)
	}
}

func TestPostingsArePerDocumentAndSorted(t *testing.T) {
	idx := newTestIndex(t)

	postings := idx.Postings("goroutin") // the stem of "goroutines"
	if postings == nil {
		t.Fatal("no postings for a term appearing in two documents")
	}
	if len(postings.Postings) != 2 {
		t.Fatalf("term appears in %d documents, want 2", len(postings.Postings))
	}
	for i := 1; i < len(postings.Postings); i++ {
		if postings.Postings[i].DocID <= postings.Postings[i-1].DocID {
			t.Errorf("postings are not sorted by document id: %v", postings.Postings)
		}
	}
	// "Goroutines" occurs twice in document a.
	if got := postings.Postings[0].Frequency; got != 2 {
		t.Errorf("frequency in the first document = %d, want 2", got)
	}
}

func TestFieldScopedTerms(t *testing.T) {
	idx := newTestIndex(t)

	// "ownership" is in a title, so both the bare and the scoped term exist.
	if idx.Postings("ownership") == nil {
		t.Error("no bare posting for a title term")
	}
	if idx.Postings("title:ownership") == nil {
		t.Error("no field-scoped posting for a title term")
	}
	// "collector" is only in a body, so there is no title-scoped entry.
	if idx.Postings("title:collector") != nil {
		t.Error("a body-only term got a title-scoped posting")
	}
}

func TestPositionsContinueFromTitleIntoBody(t *testing.T) {
	// Title and body share one position space. If the body restarted at zero,
	// a phrase could appear to straddle the two fields.
	idx := New(analysis.New())
	if _, err := idx.AddDocument("x", "alpha beta", "gamma delta"); err != nil {
		t.Fatal(err)
	}

	gamma := idx.Postings("gamma")
	if gamma == nil || len(gamma.Postings) == 0 {
		t.Fatal("no posting for the first body term")
	}
	if pos := gamma.Postings[0].Positions[0]; pos < 2 {
		t.Errorf("the first body term is at position %d, want it after the two title terms", pos)
	}
}

func TestDocumentLookup(t *testing.T) {
	idx := newTestIndex(t)

	doc, ok := idx.Document(1)
	if !ok {
		t.Fatal("document 1 not found")
	}
	if doc.URI != "a.md" {
		t.Errorf("URI = %q, want a.md", doc.URI)
	}
	if doc.Length <= 0 {
		t.Errorf("Length = %d, want a positive token count", doc.Length)
	}

	if _, ok := idx.Document(9999); ok {
		t.Error("found a document that was never added")
	}
}

func TestHasURI(t *testing.T) {
	idx := newTestIndex(t)

	if !idx.HasURI("a.md") {
		t.Error("HasURI missed an indexed document")
	}
	if idx.HasURI("never-added.md") {
		t.Error("HasURI reported a document that was never added")
	}
}

func TestTermsExcludesFieldScoped(t *testing.T) {
	idx := newTestIndex(t)

	for _, term := range idx.Terms() {
		if strings.Contains(term, ":") {
			t.Errorf("Terms() included the field-scoped entry %q", term)
		}
	}
	if !sortedAscending(idx.Terms()) {
		t.Error("Terms() is not sorted")
	}
}

func sortedAscending(terms []string) bool {
	for i := 1; i < len(terms); i++ {
		if terms[i] < terms[i-1] {
			return false
		}
	}
	return true
}

func TestStatsIgnoresFieldScopedDuplicates(t *testing.T) {
	idx := newTestIndex(t)
	stats := idx.Stats()

	// TermCount counts the raw map, Stats deliberately does not.
	if stats.Terms >= idx.TermCount() {
		t.Errorf("Stats().Terms = %d and TermCount() = %d; the scoped copies were not excluded",
			stats.Terms, idx.TermCount())
	}
	if stats.Documents != 3 {
		t.Errorf("Stats().Documents = %d, want 3", stats.Documents)
	}
	if stats.Postings <= 0 {
		t.Errorf("Stats().Postings = %d, want a positive count", stats.Postings)
	}
}

func TestTopTerms(t *testing.T) {
	idx := newTestIndex(t)

	top := idx.TopTerms(5)
	if len(top) == 0 {
		t.Fatal("TopTerms returned nothing")
	}
	if len(top) > 5 {
		t.Errorf("TopTerms(5) returned %d entries", len(top))
	}
	for i := 1; i < len(top); i++ {
		if top[i].DocFrequency > top[i-1].DocFrequency {
			t.Errorf("TopTerms is not sorted by frequency: %v", top)
		}
	}
	for _, tc := range top {
		if strings.Contains(tc.Term, ":") {
			t.Errorf("TopTerms included the field-scoped entry %q", tc.Term)
		}
	}
}

func TestTopTermsIsStable(t *testing.T) {
	// Map iteration order changes between runs; without the tie-break, equally
	// common terms would shuffle and the diagnostic would be unreadable.
	idx := newTestIndex(t)

	first := idx.TopTerms(10)
	for i := 0; i < 20; i++ {
		if !reflect.DeepEqual(idx.TopTerms(10), first) {
			t.Fatal("TopTerms returned a different order on a repeated call")
		}
	}
}

// --- persistence ---

func TestSaveAndLoad(t *testing.T) {
	original := newTestIndex(t)
	dir := filepath.Join(t.TempDir(), "index")

	if err := original.Save(dir); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := Load(dir, analysis.New())
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if loaded.DocCount() != original.DocCount() {
		t.Errorf("documents: %d loaded, %d saved", loaded.DocCount(), original.DocCount())
	}
	if loaded.TermCount() != original.TermCount() {
		t.Errorf("terms: %d loaded, %d saved", loaded.TermCount(), original.TermCount())
	}
	if loaded.AverageLength() != original.AverageLength() {
		t.Errorf("average length: %v loaded, %v saved",
			loaded.AverageLength(), original.AverageLength())
	}

	// Every posting must survive, positions included -- phrase search depends
	// on them and a lost position fails silently as a missing result.
	for _, term := range original.Terms() {
		before, after := original.Postings(term), loaded.Postings(term)
		if after == nil {
			t.Fatalf("term %q was lost", term)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("postings for %q changed across save and load", term)
		}
	}

	// Bodies must survive too, since snippets are built from them.
	for id := DocID(1); id <= 3; id++ {
		want, _ := original.Document(id)
		got, ok := loaded.Document(id)
		if !ok {
			t.Fatalf("document %d was lost", id)
		}
		if !reflect.DeepEqual(want, got) {
			t.Errorf("document %d changed:\n got %+v\nwant %+v", id, got, want)
		}
	}
}

func TestLoadContinuesAssigningIDs(t *testing.T) {
	// If nextID reset on load, appending to a saved index would overwrite an
	// existing document rather than adding one.
	original := newTestIndex(t)
	dir := filepath.Join(t.TempDir(), "index")
	if err := original.Save(dir); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(dir, analysis.New())
	if err != nil {
		t.Fatal(err)
	}

	id, err := loaded.AddDocument("d.md", "New", "added after loading")
	if err != nil {
		t.Fatal(err)
	}
	if id <= 3 {
		t.Errorf("the appended document got id %d, colliding with an existing one", id)
	}
	if loaded.DocCount() != 4 {
		t.Errorf("DocCount = %d after appending, want 4", loaded.DocCount())
	}
}

func TestSaveIsAtomic(t *testing.T) {
	// Save writes to a temporary file and renames, so an interrupted save
	// cannot leave a half-written index behind. What is observable from here
	// is that no temporary files survive a successful one.
	idx := newTestIndex(t)
	dir := filepath.Join(t.TempDir(), "index")
	if err := idx.Save(dir); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

func TestSaveOverwrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "index")

	first := newTestIndex(t)
	if err := first.Save(dir); err != nil {
		t.Fatal(err)
	}

	second := New(analysis.New())
	if _, err := second.AddDocument("only.md", "Only", "one document now"); err != nil {
		t.Fatal(err)
	}
	if err := second.Save(dir); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(dir, analysis.New())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DocCount() != 1 {
		t.Errorf("DocCount = %d after overwriting, want 1", loaded.DocCount())
	}
}

func TestLoadMissingDirectory(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nothing-here"), analysis.New()); err == nil {
		t.Error("loaded an index that does not exist")
	}
}

func TestLoadRejectsAForeignFile(t *testing.T) {
	// The magic header is what stops a truncated or unrelated file from being
	// read as postings and producing nonsense results.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "postings.lum"),
		[]byte("this is not a lumen index"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "documents.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(dir, analysis.New()); err == nil {
		t.Error("loaded a file that is not an index")
	}
}

func TestEmptyIndexSaveAndLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "index")
	if err := New(analysis.New()).Save(dir); err != nil {
		t.Fatalf("saving an empty index: %v", err)
	}

	loaded, err := Load(dir, analysis.New())
	if err != nil {
		t.Fatalf("loading an empty index: %v", err)
	}
	if loaded.DocCount() != 0 {
		t.Errorf("DocCount = %d, want 0", loaded.DocCount())
	}
	if loaded.AverageLength() != 0 {
		t.Errorf("AverageLength = %v on an empty index, want 0 rather than a division by zero",
			loaded.AverageLength())
	}
}

// --- concurrency ---

func TestConcurrentReads(t *testing.T) {
	// The server answers queries from many goroutines against one index. Run
	// with -race, this is the test that says that is safe.
	idx := newTestIndex(t)

	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 200; j++ {
				idx.Postings("goroutin")
				idx.Document(1)
				idx.Stats()
				idx.DocCount()
			}
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

func TestConcurrentWritesAndReads(t *testing.T) {
	idx := New(analysis.New())

	done := make(chan struct{})
	go func() {
		defer func() { done <- struct{}{} }()
		for i := 0; i < 200; i++ {
			_, _ = idx.AddDocument(strings.Repeat("a", i%20+1), "title", "body text here")
		}
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		for i := 0; i < 200; i++ {
			idx.Stats()
			idx.Terms()
		}
	}()

	<-done
	<-done
}

// --- a term appearing in more than one field ---

func TestTermInTitleAndBodyGetsOnePosting(t *testing.T) {
	// The title and the body are indexed by separate calls. If each appends
	// its own posting, a word in both fields is recorded as two documents.
	// Nothing errors: document frequency quietly inflates, IDF drops for that
	// term across the whole corpus, and the sorted-unique order that Intersect
	// walks no longer holds.
	idx := New(analysis.New())
	if _, err := idx.AddDocument("a.md", "Ownership in Rust", "Rust manages memory."); err != nil {
		t.Fatal(err)
	}

	postings := idx.Postings("rust")
	if postings == nil {
		t.Fatal("no postings for a term in both the title and the body")
	}
	if len(postings.Postings) != 1 {
		t.Fatalf("one document produced %d postings for %q", len(postings.Postings), "rust")
	}
	if got := postings.Postings[0].Frequency; got != 2 {
		t.Errorf("frequency = %d, want 2 (once in the title, once in the body)", got)
	}
	if got := len(postings.Postings[0].Positions); got != 2 {
		t.Errorf("recorded %d positions, want 2", got)
	}
}

func TestPostingDocIDsAreUnique(t *testing.T) {
	// The invariant Intersect and the delta encoding both depend on.
	idx := New(analysis.New())
	docs := []struct{ uri, title, body string }{
		{"a", "search engine", "a search engine indexes documents"},
		{"b", "index", "the index maps terms to documents"},
		{"c", "documents", "documents are indexed by an engine"},
	}
	for _, d := range docs {
		if _, err := idx.AddDocument(d.uri, d.title, d.body); err != nil {
			t.Fatal(err)
		}
	}

	for _, term := range idx.Terms() {
		postings := idx.Postings(term).Postings
		for i := 1; i < len(postings); i++ {
			if postings[i].DocID <= postings[i-1].DocID {
				t.Errorf("term %q has out-of-order or duplicate document ids: %v",
					term, postings)
				break
			}
		}
	}
}

func TestPositionsAreAscendingAcrossFields(t *testing.T) {
	// The delta encoding stores gaps, so a position that goes backwards
	// underflows rather than erroring.
	idx := New(analysis.New())
	if _, err := idx.AddDocument("a", "rust", "rust rust"); err != nil {
		t.Fatal(err)
	}

	positions := idx.Postings("rust").Postings[0].Positions
	for i := 1; i < len(positions); i++ {
		if positions[i] <= positions[i-1] {
			t.Errorf("positions are not ascending: %v", positions)
			break
		}
	}
}

func TestDocFrequencyNeverExceedsDocumentCount(t *testing.T) {
	idx := newTestIndex(t)

	for _, tc := range idx.TopTerms(0) {
		if tc.DocFrequency > idx.DocCount() {
			t.Errorf("term %q is in %d documents but only %d exist",
				tc.Term, tc.DocFrequency, idx.DocCount())
		}
	}
}

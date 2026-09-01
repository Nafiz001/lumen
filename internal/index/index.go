package index

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/nafizahmed/lumen/internal/analysis"
)

// Document is one indexed item and the metadata a result page needs.
type Document struct {
	ID    DocID  `json:"id"`
	URI   string `json:"uri"`
	Title string `json:"title"`
	// Body is kept so snippets can be generated at query time. That costs
	// space -- the index stores the corpus twice over -- and buys the ability
	// to show why a document matched, which a search engine without is close
	// to unusable.
	Body string `json:"body"`
	// Length is the document's token count, which BM25 needs to normalise by.
	Length int `json:"length"`
	// Fields holds the per-field token counts, so a field-scoped query can be
	// answered and a title match can be weighted above a body match.
	Fields map[string]int `json:"fields,omitempty"`
}

// Index is an in-memory inverted index with a durable on-disk form.
//
// It is held in memory in full and written out as one file. That is a real
// limit -- the corpus has to fit in RAM -- and it is the right trade at this
// scale: a segmented, memory-mapped design like Lucene's is what you build when
// the corpus does not fit, and it is noted as future work rather than
// half-implemented here.
type Index struct {
	mu sync.RWMutex

	analyzer *analysis.Analyzer

	// terms maps each term to its postings. The map is the dictionary; a
	// production engine would use a finite-state transducer or a sorted block
	// index to keep it off the heap.
	terms map[string]*PostingsList
	docs  map[DocID]*Document

	nextID DocID
	// totalLength backs the average document length in BM25.
	totalLength int64
}

// New creates an empty index.
func New(analyzer *analysis.Analyzer) *Index {
	if analyzer == nil {
		analyzer = analysis.New()
	}
	return &Index{
		analyzer: analyzer,
		terms:    make(map[string]*PostingsList),
		docs:     make(map[DocID]*Document),
		nextID:   1,
	}
}

// Analyzer returns the analyzer, so a query can be processed identically.
//
// Exposing it is deliberate: a query analysed differently from the documents
// will not match them, so the searcher takes the analyzer from the index it is
// searching rather than constructing its own.
func (idx *Index) Analyzer() *analysis.Analyzer { return idx.analyzer }

// DocCount returns how many documents are indexed.
func (idx *Index) DocCount() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.docs)
}

// TermCount returns the size of the dictionary.
func (idx *Index) TermCount() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.terms)
}

// AverageLength is the mean document length, which BM25 normalises against.
func (idx *Index) AverageLength() float64 {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if len(idx.docs) == 0 {
		return 0
	}
	return float64(idx.totalLength) / float64(len(idx.docs))
}

// AddDocument indexes a document and returns its assigned id.
//
// Title and body are analysed together but the title's terms are recorded
// separately, so a field-scoped query can be answered later.
func (idx *Index) AddDocument(uri, title, body string) (DocID, error) {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if uri == "" {
		return 0, fmt.Errorf("a document needs a URI")
	}

	id := idx.nextID
	idx.nextID++

	doc := &Document{
		ID:     id,
		URI:    uri,
		Title:  title,
		Body:   body,
		Fields: make(map[string]int, 2),
	}

	// The title is indexed first and the body's positions continue from it, so
	// a phrase can span the boundary and title terms occupy the low positions
	// -- which is also how a proximity-to-start heuristic would find them.
	position := 0
	position = idx.indexField(id, "title", title, position)
	doc.Fields["title"] = position
	bodyStart := position
	position = idx.indexField(id, "body", body, position)
	doc.Fields["body"] = position - bodyStart

	doc.Length = position
	idx.docs[id] = doc
	idx.totalLength += int64(position)
	return id, nil
}

// indexField analyses one field and merges its tokens into the postings.
func (idx *Index) indexField(id DocID, field, text string, startPosition int) int {
	tokens := idx.analyzer.Analyze(text)
	if len(tokens) == 0 {
		return startPosition
	}

	// Group by term first, so each term is touched once per document rather
	// than once per occurrence. On a long document that is the difference
	// between one map lookup and thousands.
	grouped := make(map[string][]uint32)
	maxPosition := startPosition
	for _, tok := range tokens {
		position := startPosition + tok.Position
		grouped[tok.Term] = append(grouped[tok.Term], uint32(position))
		if position+1 > maxPosition {
			maxPosition = position + 1
		}
		// A field-scoped term, so `title:golang` can be answered without a
		// second index.
		fieldTerm := field + ":" + tok.Term
		grouped[fieldTerm] = append(grouped[fieldTerm], uint32(position))
	}

	for term, positions := range grouped {
		list, ok := idx.terms[term]
		if !ok {
			list = &PostingsList{Term: term}
			idx.terms[term] = list
		}
		// Documents arrive in increasing id order, so appending keeps the list
		// sorted without a sort -- which is what the intersection depends on.
		list.Postings = append(list.Postings, Posting{
			DocID:     id,
			Frequency: uint32(len(positions)),
			Positions: positions,
		})
	}
	return maxPosition
}

// Postings returns the list for a term, or nil.
func (idx *Index) Postings(term string) *PostingsList {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.terms[term]
}

// Document returns a document by id.
func (idx *Index) Document(id DocID) (*Document, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	doc, ok := idx.docs[id]
	return doc, ok
}

// Documents returns every document, in id order.
func (idx *Index) Documents() []*Document {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	out := make([]*Document, 0, len(idx.docs))
	for _, doc := range idx.docs {
		out = append(out, doc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// HasURI reports whether a URI is already indexed, so a crawler can skip it.
func (idx *Index) HasURI(uri string) bool {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	for _, doc := range idx.docs {
		if doc.URI == uri {
			return true
		}
	}
	return false
}

// Terms returns the dictionary, sorted. Used by diagnostics and prefix search.
func (idx *Index) Terms() []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	out := make([]string, 0, len(idx.terms))
	for term := range idx.terms {
		if !strings.Contains(term, ":") {
			out = append(out, term)
		}
	}
	sort.Strings(out)
	return out
}

// TermCount pairs a term with the number of documents containing it.
type TermCount struct {
	Term         string `json:"term"`
	DocFrequency int    `json:"doc_frequency"`
}

// TopTerms returns the n terms appearing in the most documents.
//
// This is a diagnostic rather than a search feature, and it is a revealing one:
// if the top of the list is full of markup fragments or minified identifiers,
// the analysis pipeline is indexing something it should be discarding, and
// every document's length -- and so every BM25 score -- is skewed by it.
func (idx *Index) TopTerms(n int) []TermCount {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	counts := make([]TermCount, 0, len(idx.terms))
	for term, list := range idx.terms {
		if strings.Contains(term, ":") {
			continue // field-scoped copies would appear twice
		}
		counts = append(counts, TermCount{Term: term, DocFrequency: len(list.Postings)})
	}
	// Ties break on the term so the output is stable between runs; map order
	// alone would shuffle equally common terms on every call.
	sort.Slice(counts, func(i, j int) bool {
		if counts[i].DocFrequency != counts[j].DocFrequency {
			return counts[i].DocFrequency > counts[j].DocFrequency
		}
		return counts[i].Term < counts[j].Term
	})
	if n > 0 && len(counts) > n {
		counts = counts[:n]
	}
	return counts
}

// Stats describes the index, for the CLI and the status endpoint.
type Stats struct {
	Documents     int     `json:"documents"`
	Terms         int     `json:"terms"`
	Postings      int     `json:"postings"`
	AverageLength float64 `json:"average_length"`
}

func (idx *Index) Stats() Stats {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	postings := 0
	terms := 0
	for term, list := range idx.terms {
		if strings.Contains(term, ":") {
			continue // field-scoped duplicates would double the count
		}
		terms++
		postings += len(list.Postings)
	}

	avg := 0.0
	if len(idx.docs) > 0 {
		avg = float64(idx.totalLength) / float64(len(idx.docs))
	}
	return Stats{
		Documents:     len(idx.docs),
		Terms:         terms,
		Postings:      postings,
		AverageLength: avg,
	}
}

// --- persistence ---

const (
	fileMagic   = "LUMEN001"
	fileVersion = 1
)

// Save writes the index to a directory.
//
// The dictionary and postings go into one binary file; the documents go into a
// JSON file beside it. Splitting them is a practical choice rather than a
// principled one: the postings are the part worth compressing carefully, and
// the documents are the part worth being able to read with a text editor when
// something looks wrong.
func (idx *Index) Save(dir string) error {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	// Written to a temporary path and renamed, so an interrupted save leaves
	// the previous index intact rather than a half-written one.
	postingsPath := filepath.Join(dir, "postings.lum")
	tempPath := postingsPath + ".tmp"

	f, err := os.Create(tempPath)
	if err != nil {
		return err
	}

	buf := make([]byte, 0, 1<<20)
	buf = append(buf, fileMagic...)
	buf = binary.LittleEndian.AppendUint32(buf, fileVersion)
	buf = binary.AppendUvarint(buf, uint64(len(idx.terms)))

	// Terms are written in sorted order so the file is deterministic: the same
	// corpus produces byte-identical output, which makes it diffable and makes
	// a corrupted write obvious.
	termNames := make([]string, 0, len(idx.terms))
	for term := range idx.terms {
		termNames = append(termNames, term)
	}
	sort.Strings(termNames)

	for _, term := range termNames {
		encoded := idx.terms[term].Encode()
		buf = binary.AppendUvarint(buf, uint64(len(term)))
		buf = append(buf, term...)
		buf = binary.AppendUvarint(buf, uint64(len(encoded)))
		buf = append(buf, encoded...)
	}

	if _, err := f.Write(buf); err != nil {
		f.Close()
		os.Remove(tempPath)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tempPath)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, postingsPath); err != nil {
		os.Remove(tempPath)
		return err
	}

	// Documents, as JSON.
	docs := make([]*Document, 0, len(idx.docs))
	for _, doc := range idx.docs {
		docs = append(docs, doc)
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].ID < docs[j].ID })

	data, err := json.Marshal(struct {
		NextID DocID       `json:"next_id"`
		Docs   []*Document `json:"documents"`
	}{NextID: idx.nextID, Docs: docs})
	if err != nil {
		return err
	}

	docsPath := filepath.Join(dir, "documents.json")
	docsTemp := docsPath + ".tmp"
	if err := os.WriteFile(docsTemp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(docsTemp, docsPath)
}

// Load reads an index written by Save.
func Load(dir string, analyzer *analysis.Analyzer) (*Index, error) {
	idx := New(analyzer)

	data, err := os.ReadFile(filepath.Join(dir, "postings.lum"))
	if err != nil {
		return nil, fmt.Errorf("read postings: %w", err)
	}
	if len(data) < len(fileMagic)+4 {
		return nil, fmt.Errorf("postings file is truncated")
	}
	if string(data[:len(fileMagic)]) != fileMagic {
		return nil, fmt.Errorf("not a lumen index")
	}
	if v := binary.LittleEndian.Uint32(data[len(fileMagic):]); v != fileVersion {
		return nil, fmt.Errorf("unsupported index version %d", v)
	}

	pos := len(fileMagic) + 4
	termCount, n := binary.Uvarint(data[pos:])
	if n <= 0 {
		return nil, fmt.Errorf("corrupt index: bad term count")
	}
	pos += n

	for i := uint64(0); i < termCount; i++ {
		termLen, n := binary.Uvarint(data[pos:])
		if n <= 0 || pos+n+int(termLen) > len(data) {
			return nil, fmt.Errorf("corrupt index: bad term length")
		}
		pos += n
		term := string(data[pos : pos+int(termLen)])
		pos += int(termLen)

		blobLen, n := binary.Uvarint(data[pos:])
		if n <= 0 || pos+n+int(blobLen) > len(data) {
			return nil, fmt.Errorf("corrupt index: bad postings length for %q", term)
		}
		pos += n

		list, err := DecodePostings(term, data[pos:pos+int(blobLen)])
		if err != nil {
			return nil, err
		}
		pos += int(blobLen)
		idx.terms[term] = list
	}

	docData, err := os.ReadFile(filepath.Join(dir, "documents.json"))
	if err != nil {
		return nil, fmt.Errorf("read documents: %w", err)
	}
	var stored struct {
		NextID DocID       `json:"next_id"`
		Docs   []*Document `json:"documents"`
	}
	if err := json.Unmarshal(docData, &stored); err != nil {
		return nil, fmt.Errorf("parse documents: %w", err)
	}

	idx.nextID = stored.NextID
	for _, doc := range stored.Docs {
		idx.docs[doc.ID] = doc
		idx.totalLength += int64(doc.Length)
	}
	if idx.nextID == 0 {
		idx.nextID = 1
	}
	return idx, nil
}

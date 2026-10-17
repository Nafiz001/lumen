package search

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/Nafiz001/lumen/internal/analysis"
	"github.com/Nafiz001/lumen/internal/index"
)

// The benchmarks build a synthetic corpus with a Zipf-like term distribution,
// because that is what makes the numbers mean anything. A corpus of uniformly
// random terms gives every postings list the same length, which is the case an
// inverted index handles best and real text never produces. Real vocabulary is
// heavily skewed -- a few terms in almost every document, a long tail in one or
// two -- and it is the common terms whose postings lists dominate query cost.

var vocabulary = buildVocabulary(20000)

func buildVocabulary(n int) []string {
	words := make([]string, n)
	for i := range words {
		words[i] = fmt.Sprintf("term%d", i)
	}
	return words
}

// zipfWord picks a term with a skew resembling natural language.
func zipfWord(rng *rand.Rand, zipf *rand.Zipf) string {
	return vocabulary[zipf.Uint64()%uint64(len(vocabulary))]
}

func buildCorpus(b *testing.B, documents, wordsPer int) *index.Index {
	b.Helper()

	idx := index.New(analysis.New())
	rng := rand.New(rand.NewSource(42))
	zipf := rand.NewZipf(rng, 1.2, 1, uint64(len(vocabulary)-1))

	for d := 0; d < documents; d++ {
		body := make([]byte, 0, wordsPer*8)
		for w := 0; w < wordsPer; w++ {
			body = append(body, zipfWord(rng, zipf)...)
			body = append(body, ' ')
		}
		title := fmt.Sprintf("document %d %s", d, zipfWord(rng, zipf))
		if _, err := idx.AddDocument(fmt.Sprintf("doc/%d", d), title, string(body)); err != nil {
			b.Fatal(err)
		}
	}
	return idx
}

func BenchmarkIndexDocuments(b *testing.B) {
	rng := rand.New(rand.NewSource(1))
	zipf := rand.NewZipf(rng, 1.2, 1, uint64(len(vocabulary)-1))

	// One document of roughly 500 words, which is a typical article or source
	// file, so the number reads as "documents per second".
	body := make([]byte, 0, 4000)
	for w := 0; w < 500; w++ {
		body = append(body, zipfWord(rng, zipf)...)
		body = append(body, ' ')
	}
	text := string(body)

	b.ReportAllocs()
	b.ResetTimer()

	idx := index.New(analysis.New())
	for i := 0; i < b.N; i++ {
		if _, err := idx.AddDocument(fmt.Sprintf("doc/%d", i), "a title", text); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkSearch(b *testing.B, documents int, query string) {
	idx := buildCorpus(b, documents, 300)
	searcher := New(idx)
	opts := DefaultOptions()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := searcher.Search(query, opts); err != nil {
			b.Fatal(err)
		}
	}
}

// The queried terms are drawn from the head of the Zipf distribution, so the
// postings lists involved are the long ones -- the expensive case, not the
// flattering one.
func BenchmarkSearchSingleTerm(b *testing.B) { benchmarkSearch(b, 10000, "term1") }
func BenchmarkSearchTwoTerms(b *testing.B)   { benchmarkSearch(b, 10000, "term1 term2") }
func BenchmarkSearchFiveTerms(b *testing.B) {
	benchmarkSearch(b, 10000, "term1 term2 term3 term4 term5")
}
func BenchmarkSearchRequired(b *testing.B)    { benchmarkSearch(b, 10000, "+term1 +term2 term3") }
func BenchmarkSearchPhrase(b *testing.B)      { benchmarkSearch(b, 10000, `"term1 term2"`) }
func BenchmarkSearchFieldScoped(b *testing.B) { benchmarkSearch(b, 10000, "title:term1") }
func BenchmarkSearchExclusion(b *testing.B)   { benchmarkSearch(b, 10000, "term1 -term2") }

// A term matching nothing should cost nothing; if it does not, the searcher is
// doing work before checking whether it has anything to work on.
func BenchmarkSearchNoMatches(b *testing.B) { benchmarkSearch(b, 10000, "termnothingmatchesthis") }

func BenchmarkSearchScaling(b *testing.B) {
	for _, documents := range []int{1000, 10000, 50000} {
		b.Run(fmt.Sprintf("%ddocs", documents), func(b *testing.B) {
			benchmarkSearch(b, documents, "term1 term2")
		})
	}
}

func BenchmarkSnippetGeneration(b *testing.B) {
	// Snippets re-tokenise the matched document at query time, which is the
	// deliberate trade for not storing character offsets in the index. This is
	// the number that says whether the trade holds.
	idx := buildCorpus(b, 1000, 800)
	searcher := New(idx)

	withSnippets := DefaultOptions()
	withSnippets.SnippetLength = 240

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if _, err := searcher.Search("term1 term2", withSnippets); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAnalyze(b *testing.B) {
	analyzer := analysis.New()
	text := "The Porter stemming algorithm reduces running and runs to a common root, " +
		"which is what makes a search for one find a document containing the other."

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		analyzer.AnalyzeTerms(text)
	}
}

// Package search turns a query into ranked results.
//
// Two things happen here: deciding *which* documents match, and deciding what
// order to show them in. The second is the harder problem and the one users
// judge an engine on -- a search that returns the right thousand documents with
// the best one at rank 400 is a search that failed.
package search

import (
	"math"

	"github.com/Nafiz001/lumen/internal/index"
)

// BM25 parameters.
//
// These are the values from the original TREC experiments and they are what
// almost every engine still ships, Lucene and Elasticsearch included. They are
// defaults rather than laws -- a corpus of tweets and a corpus of legal
// judgements want different length normalisation -- but they are a well-tested
// starting point.
const (
	// DefaultK1 controls how quickly term frequency saturates.
	DefaultK1 = 1.2
	// DefaultB controls how strongly document length is normalised.
	DefaultB = 0.75
)

// Scorer ranks documents with Okapi BM25.
//
// BM25 is what replaced TF-IDF, and it is worth being precise about why,
// because the two look similar and behave very differently on real corpora.
//
// *Term frequency saturates.* Under TF-IDF a document containing a term fifty
// times scores fifty times higher than one containing it once. That is wrong:
// the fiftieth occurrence tells you almost nothing the second did not, and the
// effect is that keyword-stuffed pages beat good ones. BM25 runs frequency
// through f/(f+k1), which rises steeply at first and then flattens -- so the
// difference between one and two occurrences is large, and between forty and
// fifty is nearly nil.
//
// *Length is normalised, adjustably.* A long document contains more of every
// word, so raw frequency favours length. BM25 divides by length relative to the
// corpus average, with b controlling how hard: b=1 normalises fully, b=0 not at
// all, and 0.75 is the usual compromise. TF-IDF has no principled equivalent.
//
// *Inverse document frequency is smoothed.* A term in nearly every document
// carries almost no information, and the log form gives it a weight near zero.
type Scorer struct {
	K1 float64
	B  float64

	docCount      int
	averageLength float64
}

// NewScorer builds a scorer for one index.
func NewScorer(idx *index.Index) *Scorer {
	return &Scorer{
		K1:            DefaultK1,
		B:             DefaultB,
		docCount:      idx.DocCount(),
		averageLength: idx.AverageLength(),
	}
}

// IDF is the inverse document frequency of a term.
//
// The formula is the BM25 variant with the +0.5 smoothing terms:
//
//	idf = ln( (N - df + 0.5) / (df + 0.5) + 1 )
//
// The trailing +1 matters. Without it, a term appearing in more than half the
// documents gets a *negative* idf, and a document scores worse for containing
// a word the user searched for -- which is indefensible however common the word
// is. The +1 floors it at zero instead.
func (s *Scorer) IDF(docFrequency int) float64 {
	if s.docCount == 0 || docFrequency == 0 {
		return 0
	}
	n := float64(s.docCount)
	df := float64(docFrequency)
	return math.Log((n-df+0.5)/(df+0.5) + 1)
}

// Score computes one term's contribution to one document.
func (s *Scorer) Score(termFrequency uint32, docLength int, docFrequency int) float64 {
	if termFrequency == 0 || s.averageLength == 0 {
		return 0
	}

	tf := float64(termFrequency)
	length := float64(docLength)

	// The saturating numerator and the length-normalised denominator. As tf
	// grows, the ratio approaches k1+1 rather than growing without bound.
	numerator := tf * (s.K1 + 1)
	denominator := tf + s.K1*(1-s.B+s.B*(length/s.averageLength))

	return s.IDF(docFrequency) * (numerator / denominator)
}

// Explanation records how a score was arrived at, for the `explain` output.
//
// Ranking is the part of a search engine most likely to be quietly wrong, and
// the only way to tell a good ranker from a bad one is to be able to ask why a
// document scored what it did.
type Explanation struct {
	Term          string  `json:"term"`
	TermFrequency uint32  `json:"term_frequency"`
	DocFrequency  int     `json:"doc_frequency"`
	IDF           float64 `json:"idf"`
	Contribution  float64 `json:"contribution"`
	FieldBoost    float64 `json:"field_boost,omitempty"`
}

// ScoreWithExplanation returns the score and how it was reached.
func (s *Scorer) ScoreWithExplanation(term string, termFrequency uint32, docLength, docFrequency int) Explanation {
	return Explanation{
		Term:          term,
		TermFrequency: termFrequency,
		DocFrequency:  docFrequency,
		IDF:           s.IDF(docFrequency),
		Contribution:  s.Score(termFrequency, docLength, docFrequency),
	}
}

// DocCount and AverageLength expose the corpus statistics a caller may want to
// display alongside an explanation.
func (s *Scorer) DocCount() int          { return s.docCount }
func (s *Scorer) AverageLength() float64 { return s.averageLength }

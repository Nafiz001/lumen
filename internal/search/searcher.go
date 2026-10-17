package search

import (
	"sort"
	"strings"

	"github.com/Nafiz001/lumen/internal/index"
)

// Result is one ranked hit.
type Result struct {
	DocID   index.DocID `json:"id"`
	URI     string      `json:"uri"`
	Title   string      `json:"title"`
	Score   float64     `json:"score"`
	Snippet string      `json:"snippet"`
	// Explanations show how the score was reached, when asked for.
	Explanations []Explanation `json:"explanations,omitempty"`
}

// Results is a ranked page plus what it took to produce it.
type Results struct {
	Query string   `json:"query"`
	Total int      `json:"total"`
	Hits  []Result `json:"hits"`
	// Parsed is the query as understood, which is worth showing: a user who
	// typed something the parser read differently should be able to see that.
	Parsed string `json:"parsed"`
}

// Searcher runs queries against an index.
type Searcher struct {
	idx    *index.Index
	scorer *Scorer

	// titleBoost multiplies a title match.
	//
	// A term in the title is much stronger evidence of what a document is
	// about than the same term buried in its body, and no amount of BM25
	// tuning captures that -- it is a statement about document structure, not
	// about term statistics. 2.0 is a judgement call, and it is the single
	// knob here most worth arguing about.
	titleBoost float64
}

// New creates a searcher.
func New(idx *index.Index) *Searcher {
	return &Searcher{
		idx:        idx,
		scorer:     NewScorer(idx),
		titleBoost: 2.0,
	}
}

// SetTitleBoost overrides the title weighting.
func (s *Searcher) SetTitleBoost(boost float64) { s.titleBoost = boost }

// Options control one search.
type Options struct {
	Limit   int
	Offset  int
	Explain bool
	// SnippetLength is how many characters of context to show.
	SnippetLength int
}

// DefaultOptions returns a sensible page.
func DefaultOptions() Options {
	return Options{Limit: 10, SnippetLength: 200}
}

// Search parses and runs a query.
func (s *Searcher) Search(raw string, opts Options) (*Results, error) {
	if opts.Limit <= 0 {
		opts.Limit = 10
	}
	if opts.SnippetLength <= 0 {
		opts.SnippetLength = 200
	}

	analyzer := s.idx.Analyzer()
	query, err := ParseQuery(raw, analyzer.AnalyzeTerms)
	if err != nil {
		return nil, err
	}

	results := &Results{Query: raw, Parsed: query.String()}
	if query.IsEmpty() {
		return results, nil
	}

	scored := s.score(query, opts.Explain)
	results.Total = len(scored)

	// Only the requested page is ranked. Fully sorting every candidate to show
	// ten of them is the sort dominating the query on a large corpus; topK
	// selects with a bounded heap instead and falls back to a full sort when
	// the page is most of what matched.
	ranked := topK(scored, opts.Offset+opts.Limit)

	start := opts.Offset
	if start > len(ranked) {
		start = len(ranked)
	}
	end := start + opts.Limit
	if end > len(ranked) {
		end = len(ranked)
	}

	page := ranked[start:end]
	terms := query.TermsFor()
	for i := range page {
		doc, ok := s.idx.Document(page[i].DocID)
		if !ok {
			continue
		}
		page[i].URI = doc.URI
		page[i].Title = doc.Title
		page[i].Snippet = s.snippet(doc, terms, opts.SnippetLength)
	}
	results.Hits = page
	return results, nil
}

// candidate accumulates one document's score while clauses are applied.
type candidate struct {
	docID        index.DocID
	score        float64
	matched      int
	explanations []Explanation
}

// score evaluates every clause and combines the results.
//
// The order is deliberate. Positive clauses build the candidate set, then Must
// clauses filter it, then MustNot removes. Doing exclusions last means a
// document is never scored and then discarded, and doing Must as a filter
// rather than a scorer keeps required terms from dominating the ranking.
func (s *Searcher) score(query *Query, explain bool) []Result {
	candidates := make(map[index.DocID]*candidate)
	positiveClauses := 0

	for _, clause := range query.Clauses {
		if clause.Occur == MustNot {
			continue
		}
		positiveClauses++
		s.applyClause(clause, candidates, explain)
	}

	// Must clauses: drop anything that did not match them.
	for _, clause := range query.Clauses {
		if clause.Occur != Must {
			continue
		}
		matching := s.matchingDocs(clause)
		for id := range candidates {
			if _, ok := matching[id]; !ok {
				delete(candidates, id)
			}
		}
	}

	// MustNot clauses: remove what they match.
	for _, clause := range query.Clauses {
		if clause.Occur != MustNot {
			continue
		}
		for id := range s.matchingDocs(clause) {
			delete(candidates, id)
		}
	}

	out := make([]Result, 0, len(candidates))
	for _, c := range candidates {
		// A document matching more of the query's terms is more likely to be
		// what was meant, and BM25 alone does not say so -- it sums each term
		// independently, so one very rare term can outweigh three common ones
		// that all matched. This nudges toward coverage without overriding the
		// scores.
		coverage := 1.0
		if positiveClauses > 1 {
			coverage = 1.0 + 0.15*float64(c.matched-1)
		}
		out = append(out, Result{
			DocID:        c.docID,
			Score:        c.score * coverage,
			Explanations: c.explanations,
		})
	}
	return out
}

// applyClause scores one positive clause into the candidate set.
func (s *Searcher) applyClause(clause Clause, candidates map[index.DocID]*candidate, explain bool) {
	if clause.Phrase {
		s.applyPhrase(clause, candidates, explain)
		return
	}

	for _, term := range clause.Terms {
		indexed := clause.indexTerm(term)
		list := s.idx.Postings(indexed)
		if list == nil {
			continue
		}

		boost := 1.0
		if clause.Field == "title" {
			boost = s.titleBoost
		}

		docFrequency := list.DocFrequency()
		for _, posting := range list.Postings {
			doc, ok := s.idx.Document(posting.DocID)
			if !ok {
				continue
			}

			contribution := s.scorer.Score(posting.Frequency, doc.Length, docFrequency) * boost

			// An unscoped term still gets a title boost if it happens to occur
			// in the title, which is checked with the field-scoped postings.
			if clause.Field == "" {
				if titleList := s.idx.Postings("title:" + term); titleList != nil {
					if tf := frequencyIn(titleList, posting.DocID); tf > 0 {
						contribution += s.scorer.Score(tf, doc.Length, titleList.DocFrequency()) *
							(s.titleBoost - 1.0)
					}
				}
			}

			c := candidates[posting.DocID]
			if c == nil {
				c = &candidate{docID: posting.DocID}
				candidates[posting.DocID] = c
			}
			c.score += contribution
			c.matched++

			if explain {
				e := s.scorer.ScoreWithExplanation(indexed, posting.Frequency, doc.Length, docFrequency)
				e.Contribution = contribution
				e.FieldBoost = boost
				c.explanations = append(c.explanations, e)
			}
		}
	}
}

// applyPhrase scores a phrase clause, which only matches adjacent terms.
func (s *Searcher) applyPhrase(clause Clause, candidates map[index.DocID]*candidate, explain bool) {
	lists := make([]*index.PostingsList, 0, len(clause.Terms))
	for _, term := range clause.Terms {
		list := s.idx.Postings(clause.indexTerm(term))
		if list == nil {
			return // one missing term means the phrase cannot occur
		}
		lists = append(lists, list)
	}

	// Only documents containing every term can contain the phrase, so the
	// intersection prunes before the more expensive positional check.
	common := index.Intersect(lists...)
	if len(common) == 0 {
		return
	}

	boost := 1.0
	if clause.Field == "title" {
		boost = s.titleBoost
	}

	for _, posting := range common {
		grouped := make([]index.Posting, 0, len(lists))
		for _, list := range lists {
			p := postingFor(list, posting.DocID)
			if p == nil {
				break
			}
			grouped = append(grouped, *p)
		}
		if len(grouped) != len(lists) || !index.PhraseMatch(grouped) {
			continue
		}

		doc, ok := s.idx.Document(posting.DocID)
		if !ok {
			continue
		}

		// A phrase is much stronger evidence than the same words scattered, so
		// it is scored as the sum of its terms and then boosted.
		total := 0.0
		for i, list := range lists {
			total += s.scorer.Score(grouped[i].Frequency, doc.Length, list.DocFrequency())
		}
		// The multiplier is a judgement call: high enough that an exact phrase
		// outranks a loose bag of the same words, low enough that a much more
		// relevant loose match can still win.
		total *= 1.5 * boost

		c := candidates[posting.DocID]
		if c == nil {
			c = &candidate{docID: posting.DocID}
			candidates[posting.DocID] = c
		}
		c.score += total
		c.matched++

		if explain {
			c.explanations = append(c.explanations, Explanation{
				Term:         "\"" + strings.Join(clause.Terms, " ") + "\"",
				DocFrequency: len(common),
				Contribution: total,
				FieldBoost:   boost * 1.5,
			})
		}
	}
}

// matchingDocs returns the documents a clause matches, ignoring score.
func (s *Searcher) matchingDocs(clause Clause) map[index.DocID]struct{} {
	out := make(map[index.DocID]struct{})

	if clause.Phrase {
		lists := make([]*index.PostingsList, 0, len(clause.Terms))
		for _, term := range clause.Terms {
			list := s.idx.Postings(clause.indexTerm(term))
			if list == nil {
				return out
			}
			lists = append(lists, list)
		}
		for _, posting := range index.Intersect(lists...) {
			grouped := make([]index.Posting, 0, len(lists))
			for _, list := range lists {
				if p := postingFor(list, posting.DocID); p != nil {
					grouped = append(grouped, *p)
				}
			}
			if len(grouped) == len(lists) && index.PhraseMatch(grouped) {
				out[posting.DocID] = struct{}{}
			}
		}
		return out
	}

	// A multi-term clause requires all of its terms, which is what makes
	// `+golang +rust` behave the way it reads.
	lists := make([]*index.PostingsList, 0, len(clause.Terms))
	for _, term := range clause.Terms {
		list := s.idx.Postings(clause.indexTerm(term))
		if list == nil {
			return out
		}
		lists = append(lists, list)
	}
	for _, posting := range index.Intersect(lists...) {
		out[posting.DocID] = struct{}{}
	}
	return out
}

// postingFor finds a document's posting in a list by binary search.
func postingFor(list *index.PostingsList, id index.DocID) *index.Posting {
	i := sort.Search(len(list.Postings), func(i int) bool {
		return list.Postings[i].DocID >= id
	})
	if i < len(list.Postings) && list.Postings[i].DocID == id {
		return &list.Postings[i]
	}
	return nil
}

func frequencyIn(list *index.PostingsList, id index.DocID) uint32 {
	if p := postingFor(list, id); p != nil {
		return p.Frequency
	}
	return 0
}

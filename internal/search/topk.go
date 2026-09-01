package search

import (
	"container/heap"
	"sort"
)

// Selecting the results to show.
//
// A query for a common term can match most of the corpus, and the page being
// rendered is ten results. Fully sorting every candidate to show ten of them is
// O(n log n) work for an O(n log k) answer, and on a large corpus the sort
// dominates the query.
//
// The fix is the standard one -- a bounded min-heap holding the best k seen so
// far, where k is the offset plus the page size. Every candidate is compared
// against the worst element currently held, which is one comparison for the
// overwhelming majority that do not make the page.
//
// It is worth being precise about what this does not do. It does not make the
// query cheaper: every matching document is still scored, because BM25 needs
// each document's own term frequency and length. Skipping that requires
// upper-bounding a document's possible score before computing it, which is what
// WAND and block-max WAND do, and is noted as future work rather than claimed
// here.

// ranks reports whether a sorts before b: higher score first, and the document
// id breaking ties so that equal-scoring results come back in a stable order
// rather than whatever the map iteration produced.
func ranks(a, b Result) bool {
	if a.Score != b.Score {
		return a.Score > b.Score
	}
	return a.DocID < b.DocID
}

// worstFirst is a min-heap under `ranks`: its root is the element that would be
// dropped first, so a new candidate need only be compared against the root.
type worstFirst []Result

func (h worstFirst) Len() int           { return len(h) }
func (h worstFirst) Less(i, j int) bool { return ranks(h[j], h[i]) }
func (h worstFirst) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *worstFirst) Push(x any)        { *h = append(*h, x.(Result)) }
func (h *worstFirst) Pop() any {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// topK returns the k best results, sorted.
//
// It falls back to a full sort when k is close to the number of candidates,
// because the heap only wins when most candidates are discarded; at k == n it
// is the same work with worse constants.
func topK(results []Result, k int) []Result {
	if k <= 0 {
		return nil
	}
	if k >= len(results) {
		sort.Slice(results, func(i, j int) bool { return ranks(results[i], results[j]) })
		return results
	}

	h := make(worstFirst, 0, k+1)
	for _, r := range results {
		if len(h) < k {
			heap.Push(&h, r)
			continue
		}
		// h[0] is the worst of the k held. Anything not better than it cannot
		// make the page, which is the comparison that most candidates fail.
		if ranks(r, h[0]) {
			h[0] = r
			heap.Fix(&h, 0)
		}
	}

	out := []Result(h)
	sort.Slice(out, func(i, j int) bool { return ranks(out[i], out[j]) })
	return out
}

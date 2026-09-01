package search

import (
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/nafizahmed/lumen/internal/index"
)

// fullSort is the obvious implementation topK replaces; it is the oracle the
// heap is checked against.
func fullSort(results []Result, k int) []Result {
	out := append([]Result(nil), results...)
	sort.Slice(out, func(i, j int) bool { return ranks(out[i], out[j]) })
	if k < len(out) {
		out = out[:k]
	}
	return out
}

func TestTopKMatchesAFullSort(t *testing.T) {
	rng := rand.New(rand.NewSource(3))

	for trial := 0; trial < 300; trial++ {
		n := rng.Intn(200)
		results := make([]Result, n)
		for i := range results {
			results[i] = Result{
				DocID: index.DocID(rng.Intn(1000)),
				// A small score range so ties are common; the tie-break is the
				// part most likely to differ between the two implementations.
				Score: float64(rng.Intn(8)),
			}
		}

		for _, k := range []int{1, 3, 10, 50, n, n + 5} {
			if k <= 0 {
				continue
			}
			want := fullSort(results, k)
			got := topK(append([]Result(nil), results...), k)

			if !reflect.DeepEqual(got, want) {
				t.Fatalf("trial %d, k=%d, n=%d:\n got %v\nwant %v", trial, k, n, got, want)
			}
		}
	}
}

func TestTopKOrdering(t *testing.T) {
	results := []Result{
		{DocID: 5, Score: 1.0},
		{DocID: 2, Score: 3.0},
		{DocID: 9, Score: 2.0},
		{DocID: 1, Score: 3.0}, // ties with document 2; the lower id wins
	}

	got := topK(results, 3)
	want := []Result{
		{DocID: 1, Score: 3.0},
		{DocID: 2, Score: 3.0},
		{DocID: 9, Score: 2.0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("topK = %v, want %v", got, want)
	}
}

func TestTopKEdgeCases(t *testing.T) {
	results := []Result{{DocID: 1, Score: 1}, {DocID: 2, Score: 2}}

	if got := topK(results, 0); got != nil {
		t.Errorf("topK(_, 0) = %v, want nil", got)
	}
	if got := topK(nil, 5); len(got) != 0 {
		t.Errorf("topK(nil, 5) = %v, want empty", got)
	}
	if got := topK(results, 99); len(got) != 2 {
		t.Errorf("topK with k past the end returned %d, want 2", len(got))
	}
}

func BenchmarkTopKvsFullSort(b *testing.B) {
	rng := rand.New(rand.NewSource(1))
	results := make([]Result, 50000)
	for i := range results {
		results[i] = Result{DocID: index.DocID(i), Score: rng.Float64()}
	}

	b.Run("topK-10", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			topK(append([]Result(nil), results...), 10)
		}
	})
	b.Run("fullSort-10", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			fullSort(results, 10)
		}
	})
}

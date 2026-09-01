// Package index is the inverted index: the structure that makes search fast.
//
// A forward index maps documents to the words they contain, which is how a
// document is stored and is useless for searching -- answering "which documents
// contain 'quicksort'" would mean reading every document. An inverted index maps
// the other way, from each term to the list of documents containing it, so the
// same question is one lookup.
//
// That inversion is the whole idea, and everything else here is a consequence
// of it: postings lists are long, so they are compressed; they must be
// intersected quickly, so they are sorted by document id; and phrase search
// needs to know *where* in each document a term appeared, so positions are
// stored alongside.
package index

import (
	"encoding/binary"
	"fmt"
	"sort"
)

// DocID identifies a document within an index.
type DocID uint32

// Posting is one term's appearance in one document.
type Posting struct {
	DocID DocID
	// Frequency is how many times the term occurs, which BM25 needs.
	Frequency uint32
	// Positions are the token offsets where it occurs, in ascending order.
	// They cost real space -- often more than the rest of the posting -- and
	// buy phrase search, which is not optional for a usable engine.
	Positions []uint32
}

// PostingsList is every posting for one term, sorted by document id.
//
// The sort order is not cosmetic. Intersecting two sorted lists is a linear
// merge with no allocation and no hashing, which is what makes a multi-term
// query fast; unsorted lists would force a set intersection and lose the
// ability to skip ahead.
type PostingsList struct {
	Term     string
	Postings []Posting
}

// DocFrequency is how many documents contain the term. This is the number
// inverse document frequency is computed from.
func (p *PostingsList) DocFrequency() int { return len(p.Postings) }

// Encode serialises a postings list.
//
// Two compressions matter here and both are standard practice.
//
// *Delta encoding*: document ids are stored as gaps rather than absolutes.
// A term appearing in documents 1000, 1005 and 1100 is stored as 1000, 5, 95.
// Gaps are far smaller than the ids themselves, which matters because a common
// term's postings list is the largest single object in the index.
//
// *Varint encoding*: small numbers take fewer bytes. A gap of 5 costs one byte
// rather than the four a fixed uint32 would. Together these routinely cut a
// postings list to a fraction of its naive size, and the index is almost
// entirely postings lists.
func (p *PostingsList) Encode() []byte {
	buf := make([]byte, 0, len(p.Postings)*8)
	buf = binary.AppendUvarint(buf, uint64(len(p.Postings)))

	var prevDoc DocID
	for _, posting := range p.Postings {
		// The gap from the previous document, not the id itself.
		buf = binary.AppendUvarint(buf, uint64(posting.DocID-prevDoc))
		prevDoc = posting.DocID

		buf = binary.AppendUvarint(buf, uint64(posting.Frequency))
		buf = binary.AppendUvarint(buf, uint64(len(posting.Positions)))

		// Positions get the same treatment, and benefit even more: within one
		// document they are close together, so the gaps are tiny.
		var prevPos uint32
		for _, pos := range posting.Positions {
			buf = binary.AppendUvarint(buf, uint64(pos-prevPos))
			prevPos = pos
		}
	}
	return buf
}

// DecodePostings reverses Encode.
func DecodePostings(term string, data []byte) (*PostingsList, error) {
	count, n := binary.Uvarint(data)
	if n <= 0 {
		return nil, fmt.Errorf("corrupt postings list for %q: bad length", term)
	}
	pos := n

	list := &PostingsList{Term: term, Postings: make([]Posting, 0, count)}
	var docID DocID

	for i := uint64(0); i < count; i++ {
		gap, n := binary.Uvarint(data[pos:])
		if n <= 0 {
			return nil, fmt.Errorf("corrupt postings list for %q: bad document gap", term)
		}
		pos += n
		docID += DocID(gap)

		frequency, n := binary.Uvarint(data[pos:])
		if n <= 0 {
			return nil, fmt.Errorf("corrupt postings list for %q: bad frequency", term)
		}
		pos += n

		positionCount, n := binary.Uvarint(data[pos:])
		if n <= 0 {
			return nil, fmt.Errorf("corrupt postings list for %q: bad position count", term)
		}
		pos += n

		positions := make([]uint32, 0, positionCount)
		var position uint32
		for j := uint64(0); j < positionCount; j++ {
			gap, n := binary.Uvarint(data[pos:])
			if n <= 0 {
				return nil, fmt.Errorf("corrupt postings list for %q: bad position", term)
			}
			pos += n
			position += uint32(gap)
			positions = append(positions, position)
		}

		list.Postings = append(list.Postings, Posting{
			DocID:     docID,
			Frequency: uint32(frequency),
			Positions: positions,
		})
	}
	return list, nil
}

// Intersect returns the documents present in every list.
//
// This is the operation an AND query performs, and it is why postings are kept
// sorted. The lists are advanced together, always moving the one that is
// behind, so the cost is the total length rather than the product -- and the
// shortest list is processed first, since the result cannot be larger than it.
func Intersect(lists ...*PostingsList) []Posting {
	if len(lists) == 0 {
		return nil
	}
	for _, l := range lists {
		if l == nil || len(l.Postings) == 0 {
			return nil // an empty list makes the whole intersection empty
		}
	}
	if len(lists) == 1 {
		return lists[0].Postings
	}

	// Starting from the shortest bounds the work: the result cannot contain
	// more documents than the smallest input.
	ordered := make([]*PostingsList, len(lists))
	copy(ordered, lists)
	sort.Slice(ordered, func(i, j int) bool {
		return len(ordered[i].Postings) < len(ordered[j].Postings)
	})

	result := ordered[0].Postings
	for _, list := range ordered[1:] {
		result = intersectTwo(result, list.Postings)
		if len(result) == 0 {
			return nil
		}
	}
	return result
}

// intersectTwo merges two sorted posting slices.
func intersectTwo(a, b []Posting) []Posting {
	out := make([]Posting, 0, min(len(a), len(b)))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i].DocID == b[j].DocID:
			out = append(out, a[i])
			i++
			j++
		case a[i].DocID < b[j].DocID:
			i++
		default:
			j++
		}
	}
	return out
}

// Union returns every document appearing in any list, merged and deduplicated.
//
// This is what an OR query needs. Frequencies are not summed: a document
// matching two different terms has a separate posting for each, and the scorer
// wants them separately -- adding them would conflate "contains 'go' twice"
// with "contains 'go' and 'rust' once each", which BM25 scores very
// differently.
func Union(lists ...*PostingsList) []DocID {
	seen := make(map[DocID]struct{})
	for _, list := range lists {
		if list == nil {
			continue
		}
		for _, posting := range list.Postings {
			seen[posting.DocID] = struct{}{}
		}
	}

	out := make([]DocID, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// PhraseMatch reports whether the terms appear consecutively in a document.
//
// The positions of each term are walked together, looking for a set that is
// exactly one apart in sequence. This is why positions are stored at all: with
// only frequencies, a search for "New York" would match a document containing
// "York" and "New" in unrelated sentences.
func PhraseMatch(postings []Posting) bool {
	if len(postings) == 0 {
		return false
	}
	if len(postings) == 1 {
		return len(postings[0].Positions) > 0
	}

	// Candidate start positions come from the first term; each is then checked
	// against every later term at the offset it should occupy.
	for _, start := range postings[0].Positions {
		matched := true
		for offset := 1; offset < len(postings); offset++ {
			want := start + uint32(offset)
			if !containsPosition(postings[offset].Positions, want) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// containsPosition binary-searches a sorted position list.
func containsPosition(positions []uint32, want uint32) bool {
	i := sort.Search(len(positions), func(i int) bool { return positions[i] >= want })
	return i < len(positions) && positions[i] == want
}

// PhrasePositions returns where a phrase begins in a document, for snippets.
func PhrasePositions(postings []Posting) []uint32 {
	if len(postings) == 0 {
		return nil
	}
	if len(postings) == 1 {
		return postings[0].Positions
	}

	var out []uint32
	for _, start := range postings[0].Positions {
		matched := true
		for offset := 1; offset < len(postings); offset++ {
			if !containsPosition(postings[offset].Positions, start+uint32(offset)) {
				matched = false
				break
			}
		}
		if matched {
			out = append(out, start)
		}
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

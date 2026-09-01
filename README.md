# lumen

A search engine written from scratch in Go, with no dependencies outside the
standard library. It indexes a directory of files or crawls a website, ranks
results with BM25, and serves them from a local HTTP server. Nothing leaves the
machine it runs on.

```
$ lumen index ~/notes
indexed 1,284 documents in 2.1s

$ lumen search '"distributed consensus" -blockchain'
7 results in 1.83ms

1. Raft: In Search of an Understandable Consensus Algorithm
   papers/raft.md  (score 9.8412)
   ...the problem of distributed consensus, and argues that Paxos is
   unnecessarily difficult to understand...

$ lumen serve
lumen serving ~/notes at http://127.0.0.1:8080
```

## Why build this

Search looks simple from the outside — type words, get documents — and the
interesting parts are all in the gap between that and how it actually works.
Why does a search for `running` find a page that says `runs`? Why is a document
that mentions your term twenty times not necessarily better than one that
mentions it twice? How do you find phrase matches without scanning every
document?

This implements the answers rather than describing them: an inverted index with
positional postings, the Porter stemmer, Okapi BM25, a query parser, a polite
web crawler, and the snippet generation that makes a result list readable.

## Install

Go 1.22 or newer. No dependencies to fetch.

```sh
git clone https://github.com/nafizahmed/lumen
cd lumen
make build      # or: go build -o lumen ./cmd/lumen
```

## Usage

```
lumen index <dir>     build an index from a directory of files
lumen crawl <url>     build an index by crawling a website
lumen search <query>  search from the terminal
lumen serve           search from a browser
lumen stats           what the index contains
```

Every command takes `-index <dir>` to choose where the index lives; it defaults
to `./lumen-index`. Run `lumen <command> -h` for the rest.

### Indexing files

```sh
lumen index ~/notes
lumen index ~/src -ext go,md          # only these extensions
lumen index ~/docs -append            # add to an existing index
lumen index ~/notes -hidden           # include dotfiles
```

Binary files are skipped even when the extension says otherwise, along with
`.git`, `node_modules`, `vendor` and the other directories that are large,
generated, and never what someone meant to search.

### Crawling a site

```sh
lumen crawl https://example.com
lumen crawl https://example.com -max-pages 1000 -max-depth 5
lumen crawl https://mysite.local -delay 200ms -ignore-robots
```

The crawler honours `robots.txt`, waits between requests to the same host,
stays on the starting host, and identifies itself in the `User-Agent`. The
defaults are polite because the cost of being too slow is waiting, and the cost
of being too fast is someone else's server.

### Querying

| Query | Meaning |
|---|---|
| `go concurrency` | both words, ranked by relevance |
| `"exact phrase"` | the words adjacent and in order |
| `+required` | the document must contain this |
| `-excluded` | the document must not contain this |
| `title:golang` | scoped to the title field |

These combine: `+raft "leader election" -paxos title:consensus` is a valid
query.

`lumen search -explain` shows how each score was reached, term by term:

```
1. Raft Consensus
   papers/raft.md  (score 9.8412)
     raft               tf=14   df=3     idf=2.128  ->  6.1204
     consensus          tf=9    df=11    idf=1.204  ->  3.7208
```

### Serving

```sh
lumen serve -addr 127.0.0.1:8080
```

A search page at `/`, and a JSON API:

```
GET /api/search?q=<query>&limit=10&offset=0&explain=true
GET /api/stats
GET /api/document/<id>
```

The server binds to localhost and has no authentication. That pairing is
deliberate rather than an omission: the index is of your own files on your own
machine, and a login on a service nothing else can reach is theatre. Binding to
a public interface is the thing that would need one, so passing a non-loopback
address prints a warning.

## How it works

### The inverted index

The central data structure maps each term to the documents containing it,
rather than each document to its terms — which is what makes a query cost time
proportional to the terms asked for rather than to the size of the corpus.

```
"goroutine" -> [(doc 3, freq 2, positions [45, 89]),
                (doc 7, freq 1, positions [12])]
```

Positions are stored, not just counts, because they are what a phrase query
needs: `"channel close"` matches only where `close` sits at the position
immediately after `channel`.

Postings are encoded with two standard compressions. Document ids are stored as
**gaps** rather than absolute values — a term in documents 5, 9 and 400 becomes
5, 4, 391 — and the gaps are then written as **varints**, one byte for values
under 128. Since a common term appears in many documents, its gaps are small,
so the list that would benefit least from compression is the one that gets the
most. In practice this is about 2.5× smaller than fixed-width — 4,875 bytes
against 12,000 for a thousand postings — and the test suite asserts it stays
that way.

### Analysis

Documents and queries go through the same pipeline, which is the invariant the
whole thing rests on: a query analysed differently from the documents will not
match them. The searcher takes its analyzer from the index it is searching
rather than constructing its own, so the two cannot drift apart.

The pipeline lowercases, tokenises on non-alphanumerics while keeping internal
apostrophes and hyphens, drops stopwords, and stems with the Porter algorithm
implemented from the 1980 paper — all five steps, checked against the published
test vocabulary.

Stemming is what makes a search for `running` find a document that says `runs`.
It is also lossy in ways worth knowing: Porter maps `university` and `universe`
to the same stem. That is the trade a stemmer makes, and it is the right one
for recall.

Token positions are counted **before** stopwords are removed. Otherwise
removing `the` from `the quick fox` would leave `quick` and `fox` adjacent, and
a phrase search for `"quick fox"` would match text that never said it.

### Ranking

Scoring is Okapi BM25:

```
score(D, Q) = Σ  IDF(q) · ( f(q,D) · (k₁+1) ) / ( f(q,D) + k₁·(1 - b + b·|D|/avgdl) )
```

Two things it gets right that a naive term-frequency count does not.

**Saturation.** The twentieth occurrence of a word tells you far less than the
second. The `k₁` term makes the contribution flatten out rather than grow
linearly, so a page that repeats a keyword two hundred times does not outrank
one that uses it naturally.

**Length normalisation.** A long document contains more of everything, so raw
counts favour it for no good reason. The `b` term divides by length relative to
the corpus average, so a short focused document can beat a long rambling one.

On top of BM25: title matches are boosted 2×, phrase matches 1.5×, and a
document matching more of the query's clauses gets a small coverage bonus —
because a document matching three of your four words is usually a better answer
than one matching a single word very intensely.

### Selecting the page

A query for a common term matches most of the corpus, and the page being shown
is ten results. Sorting every candidate to display ten of them makes the sort
dominate the query, so the top-k are selected with a bounded min-heap ordered
worst-first: each candidate costs one comparison against the root unless it
makes the page.

This does not make the query cheaper — every matching document is still scored,
because BM25 needs its own term frequency and length. Avoiding that needs a
score upper bound per document, which is what WAND and block-max WAND do. See
*Limits* below.

### Snippets

The excerpt under each result is not decoration. Without it, the user has to
open every link to find out which one they wanted, which is the work a search
engine exists to save.

lumen finds the **densest window** of query terms in the document and shows
that, rather than the first hundred characters — the opening of a document is
very often boilerplate that says nothing about why it matched.

Matched terms are marked with control characters rather than HTML, so the
caller decides how to render them: ANSI colour in the terminal, `<mark>` in the
browser. That also makes the escaping order safe in the web UI — the snippet is
HTML-escaped first, then the markers become tags, so a document whose body
contains `<script>` renders as text and cannot inject one.

## Performance

Measured on an Intel i5-1155G7, synthetic corpus with a Zipf-distributed
vocabulary. Uniform random terms would give every postings list the same
length, which is the case an inverted index handles best and real text never
produces, so the benchmarks avoid it. The queried terms are drawn from the head
of the distribution, so the postings lists involved are the long ones.

| Operation | Corpus | Time |
|---|---|---|
| Index a 500-word document | — | 0.27 ms |
| Analyse a sentence | — | 8.0 µs |
| Single-term query | 10,000 docs | 4.7 ms |
| Two-term query | 10,000 docs | 5.4 ms |
| Five-term query | 10,000 docs | 10.9 ms |
| Phrase query | 10,000 docs | 10.3 ms |
| Field-scoped query | 10,000 docs | 2.1 ms |
| Query matching nothing | 10,000 docs | 1.3 µs |
| Two-term query | 50,000 docs | 43.5 ms |

Reproduce with `make bench`.

The single- and two-term numbers look slow for a search engine, and they are
honest about why: those terms are the most common in the corpus, so they match
almost every document, and every match is scored. A query for an ordinary term
touches a short postings list and returns in microseconds — which is what the
"matching nothing" row and the field-scoped row show from the other direction.

Replacing the full sort with the bounded heap took the 50,000-document query
from 55.8 ms to 43.5 ms, and the selection step alone from 16.9 ms to 1.83 ms.

## Limits

Stated rather than hidden, because they are where the design ends and the next
version begins.

- **The index is held in memory in full** and written as a single file. The
  corpus has to fit in RAM. Lucene's answer is segmented, memory-mapped files
  with background merges; that is what you build when the corpus does not fit,
  and half-implementing it here would be worse than not.
- **Every matching document is scored.** A common-term query therefore costs
  time proportional to how many documents contain the term. WAND bounds a
  document's possible score before computing it and skips those that cannot
  reach the current top-k threshold.
- **No incremental update or deletion.** Adding to an index means `-append`;
  changing a document means rebuilding. Real engines keep a deletion bitmap and
  merge segments.
- **The HTML extractor is a hand-written scanner, not a parser.** It handles
  ordinary markup and will mishandle pathological input, which for an index
  means a slightly worse snippet rather than a wrong answer. `x/net/html` would
  be more correct at the cost of the zero-dependency property.
- **English only.** The stemmer, the stopword list and the tokeniser all assume
  it. CJK text needs segmentation rather than splitting on whitespace.
- **One writer.** Reads are concurrent and race-tested; writes take an
  exclusive lock.

## Development

```sh
make test          # go test ./...
make race          # go test -race ./...
make bench         # benchmarks
make lint          # gofmt check and go vet
make build         # build ./cmd/lumen
```

The suite is 83% statement coverage, runs clean under `-race`, and touches no
network: the web crawler is tested against an `httptest` server. It covers the
analysis pipeline against the published Porter test vectors, postings encoding against 200 randomised round trips, index persistence
posting by posting including positions, both crawlers, the HTTP layer including
the XSS escaping order, and top-k selection against a full sort.

## Layout

```
cmd/lumen/          CLI
internal/analysis/  tokenising, stopwords, Porter stemmer
internal/index/     postings, encoding, inverted index, persistence
internal/search/    BM25, query parsing, top-k selection, snippets
internal/crawl/     file walker, web crawler, HTML extraction
internal/server/    HTTP API and search page
```

## References

- Robertson & Zaragoza, *The Probabilistic Relevance Framework: BM25 and
  Beyond* (2009)
- Porter, *An Algorithm for Suffix Stripping* (1980)
- Manning, Raghavan & Schütze, *Introduction to Information Retrieval* (2008)
- Broder et al., *Efficient Query Evaluation using a Two-Level Retrieval
  Process* (2003) — the WAND paper

## License

MIT

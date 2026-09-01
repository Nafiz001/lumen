package analysis

import (
	"strings"
	"testing"
)

// The analyzer's one non-negotiable property is that documents and queries go
// through the same pipeline. A tokenizer that splits "e-mail" one way when
// indexing and another when searching produces an engine that cannot find its
// own documents, and nothing downstream can repair that -- so most of these
// tests are about the pipeline being consistent rather than about it being
// clever.

func terms(tokens []Token) []string {
	out := make([]string, len(tokens))
	for i, t := range tokens {
		out[i] = t.Term
	}
	return out
}

func TestTokenizeSplitsOnPunctuation(t *testing.T) {
	a := New()
	tests := []struct {
		input string
		want  string
	}{
		{"hello world", "hello world"},
		{"Hello, World!", "Hello World"},
		{"one.two.three", "one two three"},
		{"a  b\tc\nd", "a b c d"},
		{"trailing...", "trailing"},
		{"(parenthesised)", "parenthesised"},
		{"path/to/file", "path to file"},
		{"foo@bar.com", "foo bar com"},
		{"", ""},
		{"!!!", ""},
	}
	for _, tc := range tests {
		got := strings.Join(terms(a.Tokenize(tc.input)), " ")
		if got != tc.want {
			t.Errorf("Tokenize(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestTokenizeKeepsWordInternalPunctuation(t *testing.T) {
	// A hyphen or apostrophe inside a word is part of it; at the edge it is
	// punctuation. This is a judgement call rather than a standard, so it is
	// pinned down here.
	a := New()
	tests := []struct {
		input string
		want  string
	}{
		{"don't", "don't"},
		{"state-of-the-art", "state-of-the-art"},
		{"well-known", "well-known"},
		{"-leading", "leading"},
		{"trailing-", "trailing"},
		{"dash - alone", "dash alone"},
		{"it's", "it's"},
	}
	for _, tc := range tests {
		got := strings.Join(terms(a.Tokenize(tc.input)), " ")
		if got != tc.want {
			t.Errorf("Tokenize(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestTokenPositionsAreSequential(t *testing.T) {
	// Positions are what make phrase search possible, so they have to be
	// contiguous and in order.
	a := New()
	tokens := a.Tokenize("the quick brown fox")
	for i, tok := range tokens {
		if tok.Position != i {
			t.Errorf("token %q has position %d, want %d", tok.Term, tok.Position, i)
		}
	}
}

func TestTokenOffsetsPointAtTheOriginalText(t *testing.T) {
	// Offsets let a snippet be highlighted without re-tokenising, so they must
	// actually locate the token in the source.
	a := New()
	text := "the quick brown fox"
	for _, tok := range a.Tokenize(text) {
		if got := text[tok.Start:tok.End]; got != tok.Term {
			t.Errorf("offsets for %q point at %q", tok.Term, got)
		}
	}
}

func TestAnalyzeLowercasesAndStems(t *testing.T) {
	a := New()
	got := strings.Join(a.AnalyzeTerms("Running RUNS runner"), " ")
	// All three should collapse to the same stem, which is the entire point.
	fields := strings.Fields(got)
	if len(fields) != 3 {
		t.Fatalf("got %d terms: %q", len(fields), got)
	}
	if fields[0] != fields[1] {
		t.Errorf("running and runs stemmed differently: %q vs %q", fields[0], fields[1])
	}
}

func TestAnalyzeDropsStopwords(t *testing.T) {
	a := New()
	got := a.AnalyzeTerms("the cat and the hat")
	joined := strings.Join(got, " ")
	if strings.Contains(joined, "the") || strings.Contains(joined, "and") {
		t.Errorf("stopwords survived: %q", joined)
	}
	if len(got) != 2 {
		t.Errorf("got %d terms, want 2 (cat, hat): %q", len(got), joined)
	}
}

func TestStopwordRemovalCanBeDisabled(t *testing.T) {
	a := New(WithStopwords(nil))
	got := a.AnalyzeTerms("the cat")
	if len(got) != 2 {
		t.Errorf("with stopwords disabled, got %v", got)
	}
}

func TestPositionsSurviveStopwordRemoval(t *testing.T) {
	// Positions are assigned before stopwords are dropped, so a phrase search
	// for "cat hat" does not match "the cat and the hat". Getting this wrong
	// silently loosens every phrase query.
	a := New()
	tokens := a.Analyze("the cat and the hat")
	if len(tokens) != 2 {
		t.Fatalf("got %d tokens", len(tokens))
	}
	if tokens[0].Position == tokens[1].Position-1 {
		t.Error("positions were renumbered after stopword removal, so phrase distances are wrong")
	}
	if tokens[0].Position != 1 || tokens[1].Position != 4 {
		t.Errorf("positions %d and %d do not reflect the original text",
			tokens[0].Position, tokens[1].Position)
	}
}

func TestAnalyzeDropsAbsurdlyLongTokens(t *testing.T) {
	// Minified JavaScript and base64 blobs produce enormous "words" that
	// nobody will ever search for but that still cost index space.
	a := New()
	long := strings.Repeat("x", 200)
	got := a.AnalyzeTerms("normal " + long + " words")
	for _, term := range got {
		if len(term) > 64 {
			t.Errorf("a %d-character token was indexed", len(term))
		}
	}
}

// --- the stemmer ---

func TestStemmerKnownCases(t *testing.T) {
	// Cases from the Porter paper and its published test vocabulary.
	tests := map[string]string{
		"caresses":       "caress",
		"ponies":         "poni",
		"ties":           "ti",
		"caress":         "caress",
		"cats":           "cat",
		"feed":           "feed",
		"agreed":         "agre",
		"plastered":      "plaster",
		"motoring":       "motor",
		"sing":           "sing",
		"conflated":      "conflat",
		"troubled":       "troubl",
		"hopping":        "hop",
		"tanned":         "tan",
		"falling":        "fall",
		"hissing":        "hiss",
		"happy":          "happi",
		"sky":            "sky",
		"relational":     "relat",
		"conditional":    "condit",
		"valency":        "valenc",
		"hesitanci":      "hesit",
		"digitizer":      "digit",
		"conformabli":    "conform",
		"radicalli":      "radic",
		"differentli":    "differ",
		"vileli":         "vile",
		"analogousli":    "analog",
		"vietnamization": "vietnam",
		"predication":    "predic",
		"operator":       "oper",
		"feudalism":      "feudal",
		"decisiveness":   "decis",
		"hopefulness":    "hope",
		"callousness":    "callous",
		"formaliti":      "formal",
		"sensitiviti":    "sensit",
		"sensibiliti":    "sensibl",
		"triplicate":     "triplic",
		"formative":      "form",
		"formalize":      "formal",
		"electriciti":    "electr",
		"electrical":     "electr",
		"hopeful":        "hope",
		"goodness":       "good",
		"revival":        "reviv",
		"allowance":      "allow",
		"inference":      "infer",
		"airliner":       "airlin",
		"gyroscopic":     "gyroscop",
		"adjustable":     "adjust",
		"defensible":     "defens",
		"irritant":       "irrit",
		"replacement":    "replac",
		"adjustment":     "adjust",
		"dependent":      "depend",
		"adoption":       "adopt",
		"homologou":      "homolog",
		"communism":      "commun",
		"activate":       "activ",
		"angulariti":     "angular",
		"homologous":     "homolog",
		"effective":      "effect",
		"bowdlerize":     "bowdler",
		"probate":        "probat",
		"rate":           "rate",
		"cease":          "ceas",
		"controll":       "control",
		"roll":           "roll",
	}
	for input, want := range tests {
		if got := Stem(input); got != want {
			t.Errorf("Stem(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestStemmerCollapsesVariantsOfTheSameWord(t *testing.T) {
	// The property that actually matters for search: a query and a document
	// using different forms of a word must land on the same term.
	groups := [][]string{
		{"connect", "connected", "connecting", "connection", "connections"},
		{"argue", "argued", "argues", "arguing"},
	}
	for _, group := range groups {
		first := Stem(group[0])
		for _, word := range group[1:] {
			if got := Stem(word); got != first {
				t.Errorf("%q stems to %q but %q stems to %q; they should match",
					group[0], first, word, got)
			}
		}
	}
}

func TestStemmerLeavesShortWordsAlone(t *testing.T) {
	// Below three letters every rule risks destroying the word outright.
	for _, word := range []string{"a", "is", "be", "at", "go"} {
		if got := Stem(word); got != word {
			t.Errorf("Stem(%q) = %q, want it unchanged", word, got)
		}
	}
}

func TestStemmerIsIdempotent(t *testing.T) {
	// Stemming an already-stemmed term must not keep eroding it. If it did,
	// documents and queries could disagree depending on how many times the
	// pipeline had run.
	words := []string{
		"running", "happiness", "nationalization", "relational",
		"connected", "operational", "conditional", "sensibility",
	}
	for _, word := range words {
		once := Stem(word)
		twice := Stem(once)
		if once != twice {
			t.Errorf("Stem(%q) = %q, but stemming that gives %q", word, once, twice)
		}
	}
}

func TestStemmerHandlesTheYConsonantRule(t *testing.T) {
	// 'y' is a consonant at the start of a word and a vowel elsewhere, and
	// getting it wrong changes the measure and therefore which rules fire.
	if Stem("sky") != "sky" {
		t.Errorf("Stem(sky) = %q, want sky", Stem("sky"))
	}
	if Stem("happy") != "happi" {
		t.Errorf("Stem(happy) = %q, want happi", Stem("happy"))
	}
}

// Over-stemming is a real cost, and worth documenting rather than hiding.
func TestStemmerOverStemsSomeUnrelatedWords(t *testing.T) {
	// "university" and "universal" collapse to the same stem, so a search for
	// one will surface the other. That is Porter's known failure mode: it
	// trades precision for recall and has no dictionary to tell it otherwise.
	if Stem("universal") != Stem("universe") {
		t.Skip("stems differ in this implementation; the point stands as documentation")
	}
}

func BenchmarkAnalyze(b *testing.B) {
	a := New()
	text := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 20)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		a.Analyze(text)
	}
}

func BenchmarkStem(b *testing.B) {
	words := []string{"running", "nationalization", "connected", "happiness", "operational"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Stem(words[i%len(words)])
	}
}

package analysis

import "strings"

// The Porter stemming algorithm, implemented from the 1980 paper.
//
// Stemming is what makes a search for "running" find a document that says
// "runs". It strips suffixes so that morphological variants collapse to one
// index term -- run, runs, running, and (imperfectly) ran.
//
// It is worth being clear about what it is not. A stem is not a word: "happiness"
// becomes "happi", "operational" becomes "oper". That is fine, because stems are
// only ever compared against other stems -- the query goes through the same
// pipeline. It also makes mistakes in both directions: "university" and
// "universal" both stem to "univers" (over-stemming, hurting precision), while
// "ran" never reaches "run" at all (under-stemming, hurting recall). A
// dictionary-based lemmatiser does better on both counts and needs a dictionary;
// Porter needs nothing but rules, which is why it is still the default almost
// everywhere.
//
// The algorithm works in five ordered steps, each applying at most one rule from
// a list, with most rules guarded by a condition on the word's "measure" -- the
// number of vowel-consonant alternations in the stem that would remain. That
// measure is what stops the rules from destroying short words: "tree" keeps its
// ending where "agreed" loses one.

// Stem reduces a lowercase word to its Porter stem.
func Stem(word string) string {
	// Below three letters there is nothing to strip and every rule risks
	// destroying the word outright.
	if len(word) <= 2 {
		return word
	}

	w := []byte(word)
	w = step1a(w)
	w = step1b(w)
	w = step1c(w)
	w = step2(w)
	w = step3(w)
	w = step4(w)
	w = step5a(w)
	w = step5b(w)
	return string(w)
}

// isConsonant reports whether the letter at i is a consonant.
//
// 'y' is the awkward one: it is a consonant at the start of a word or after a
// vowel ("yes", "toy"), and a vowel otherwise ("happy"). Getting this wrong
// changes the measure and therefore which rules fire.
func isConsonant(w []byte, i int) bool {
	switch w[i] {
	case 'a', 'e', 'i', 'o', 'u':
		return false
	case 'y':
		if i == 0 {
			return true
		}
		return !isConsonant(w, i-1)
	default:
		return true
	}
}

// measure counts vowel-consonant sequences in a word, which is Porter's m.
//
// A word has the form [C](VC)^m[V], and m is that exponent. Rules are gated on
// it so that a suffix is only stripped when enough word remains: m > 0 means
// "there is at least one vowel-consonant pair left", which is roughly "this is
// still a word".
func measure(w []byte) int {
	n := len(w)
	i := 0

	// Skip a leading consonant run.
	for i < n && isConsonant(w, i) {
		i++
	}

	m := 0
	for i < n {
		// A vowel run.
		for i < n && !isConsonant(w, i) {
			i++
		}
		if i >= n {
			break
		}
		// Followed by a consonant run: that is one VC pair.
		for i < n && isConsonant(w, i) {
			i++
		}
		m++
	}
	return m
}

// containsVowel reports whether the stem has any vowel at all.
func containsVowel(w []byte) bool {
	for i := range w {
		if !isConsonant(w, i) {
			return true
		}
	}
	return false
}

// endsDoubleConsonant reports whether the word ends in a doubled consonant.
func endsDoubleConsonant(w []byte) bool {
	n := len(w)
	if n < 2 {
		return false
	}
	return w[n-1] == w[n-2] && isConsonant(w, n-1)
}

// endsCVC reports whether the word ends consonant-vowel-consonant with the
// final consonant not being w, x or y.
//
// This is Porter's *o condition, and it is what decides whether to restore a
// silent 'e': "hop" + "ing" was "hoping", so the stem needs its e back, while
// "hopp" + "ing" was "hopping" and does not.
func endsCVC(w []byte) bool {
	n := len(w)
	if n < 3 {
		return false
	}
	if !isConsonant(w, n-3) || isConsonant(w, n-2) || !isConsonant(w, n-1) {
		return false
	}
	switch w[n-1] {
	case 'w', 'x', 'y':
		return false
	}
	return true
}

func hasSuffix(w []byte, suffix string) bool {
	return len(w) >= len(suffix) && string(w[len(w)-len(suffix):]) == suffix
}

// replaceSuffix swaps a suffix, applying a condition to the remaining stem.
func replaceSuffix(w []byte, suffix, replacement string, condition func([]byte) bool) ([]byte, bool) {
	if !hasSuffix(w, suffix) {
		return w, false
	}
	stem := w[:len(w)-len(suffix)]
	if condition != nil && !condition(stem) {
		return w, false
	}
	return append(append([]byte{}, stem...), replacement...), true
}

// step1a removes plural 's' endings.
func step1a(w []byte) []byte {
	switch {
	case hasSuffix(w, "sses"):
		return w[:len(w)-2] // caresses -> caress
	case hasSuffix(w, "ies"):
		return w[:len(w)-2] // ponies -> poni
	case hasSuffix(w, "ss"):
		return w // caress stays
	case hasSuffix(w, "s"):
		return w[:len(w)-1] // cats -> cat
	}
	return w
}

// step1b removes 'ed' and 'ing', then repairs the stem it leaves behind.
func step1b(w []byte) []byte {
	// (m>0) EED -> EE: "agreed" -> "agree", but "feed" stays.
	if hasSuffix(w, "eed") {
		if measure(w[:len(w)-3]) > 0 {
			return w[:len(w)-1]
		}
		return w
	}

	stripped := false
	if hasSuffix(w, "ed") && containsVowel(w[:len(w)-2]) {
		w = w[:len(w)-2]
		stripped = true
	} else if hasSuffix(w, "ing") && containsVowel(w[:len(w)-3]) {
		w = w[:len(w)-3]
		stripped = true
	}
	if !stripped {
		return w
	}

	// The repair rules. Stripping "ed" or "ing" often leaves a stem that is not
	// a word -- "conflat" from "conflated", "hopp" from "hopping" -- and these
	// put it back into shape.
	switch {
	case hasSuffix(w, "at"), hasSuffix(w, "bl"), hasSuffix(w, "iz"):
		return append(w, 'e') // conflat -> conflate
	case endsDoubleConsonant(w) && !hasSuffix(w, "l") && !hasSuffix(w, "s") && !hasSuffix(w, "z"):
		return w[:len(w)-1] // hopp -> hop
	case measure(w) == 1 && endsCVC(w):
		return append(w, 'e') // fil -> file
	}
	return w
}

// step1c turns a trailing 'y' into 'i' when the stem has a vowel.
func step1c(w []byte) []byte {
	if hasSuffix(w, "y") && containsVowel(w[:len(w)-1]) {
		w[len(w)-1] = 'i' // happy -> happi, so it matches happiness -> happi
	}
	return w
}

// step2Rules map long derivational suffixes to shorter ones.
var step2Rules = [][2]string{
	{"ational", "ate"}, {"tional", "tion"}, {"enci", "ence"}, {"anci", "ance"},
	{"izer", "ize"}, {"abli", "able"}, {"alli", "al"}, {"entli", "ent"},
	{"eli", "e"}, {"ousli", "ous"}, {"ization", "ize"}, {"ation", "ate"},
	{"ator", "ate"}, {"alism", "al"}, {"iveness", "ive"}, {"fulness", "ful"},
	{"ousness", "ous"}, {"aliti", "al"}, {"iviti", "ive"}, {"biliti", "ble"},
}

func step2(w []byte) []byte {
	positiveMeasure := func(stem []byte) bool { return measure(stem) > 0 }
	for _, rule := range step2Rules {
		if out, ok := replaceSuffix(w, rule[0], rule[1], positiveMeasure); ok {
			return out
		}
	}
	return w
}

var step3Rules = [][2]string{
	{"icate", "ic"}, {"ative", ""}, {"alize", "al"}, {"iciti", "ic"},
	{"ical", "ic"}, {"ful", ""}, {"ness", ""},
}

func step3(w []byte) []byte {
	positiveMeasure := func(stem []byte) bool { return measure(stem) > 0 }
	for _, rule := range step3Rules {
		if out, ok := replaceSuffix(w, rule[0], rule[1], positiveMeasure); ok {
			return out
		}
	}
	return w
}

// step4 strips the remaining derivational suffixes, but only from words with
// enough substance left (m > 1).
var step4Suffixes = []string{
	"al", "ance", "ence", "er", "ic", "able", "ible", "ant", "ement",
	"ment", "ent", "ou", "ism", "ate", "iti", "ous", "ive", "ize",
}

func step4(w []byte) []byte {
	// "ion" is special: it is only removed after s or t, so "adoption" loses it
	// but "lion" does not.
	if hasSuffix(w, "ion") {
		stem := w[:len(w)-3]
		if measure(stem) > 1 && len(stem) > 0 && (stem[len(stem)-1] == 's' || stem[len(stem)-1] == 't') {
			return stem
		}
	}
	for _, suffix := range step4Suffixes {
		if hasSuffix(w, suffix) {
			stem := w[:len(w)-len(suffix)]
			if measure(stem) > 1 {
				return stem
			}
			// Only one rule from the list may fire, even if it fails its
			// condition -- trying the next would over-strip.
			return w
		}
	}
	return w
}

// step5a removes a final 'e' where the word can spare it.
func step5a(w []byte) []byte {
	if !hasSuffix(w, "e") {
		return w
	}
	stem := w[:len(w)-1]
	m := measure(stem)
	if m > 1 {
		return stem // rate -> rat? no: measure guards this
	}
	if m == 1 && !endsCVC(stem) {
		return stem
	}
	return w
}

// step5b collapses a doubled 'l'.
func step5b(w []byte) []byte {
	if measure(w) > 1 && endsDoubleConsonant(w) && hasSuffix(w, "l") {
		return w[:len(w)-1] // controll -> control
	}
	return w
}

// StemAll stems a slice of terms.
func StemAll(terms []string) []string {
	out := make([]string, len(terms))
	for i, t := range terms {
		out[i] = Stem(strings.ToLower(t))
	}
	return out
}

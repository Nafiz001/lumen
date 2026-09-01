package crawl

import (
	"net/url"
	"strings"
)

// HTML extraction, done with a small hand-written scanner rather than a parser.
//
// A real parser (golang.org/x/net/html) would be more correct, and this project
// deliberately has no dependencies outside the standard library. The scanner
// below is enough for the job because the job is narrow: strip tags, drop the
// contents of script and style, decode the handful of entities that appear in
// prose, and collect href attributes. It will mishandle pathological markup,
// which for a search index means a slightly worse snippet rather than a wrong
// answer.
//
// The one thing it must get right is removing script and style *contents*.
// Indexing minified JavaScript fills the dictionary with thousands of terms
// that match nothing a person would ever type, and inflates every document's
// length, which throws off BM25's normalisation for the whole corpus.

// ExtractText strips markup and returns readable text.
func ExtractText(html string) string {
	_, text := ExtractTitleAndText(html)
	return text
}

// ExtractTitleAndText pulls the <title> and the body text in one pass.
func ExtractTitleAndText(html string) (string, string) {
	var title strings.Builder
	var text strings.Builder

	runes := []rune(html)
	i := 0
	// skipUntil holds a closing tag name whose contents are being discarded.
	skipUntil := ""
	inTitle := false

	for i < len(runes) {
		if runes[i] != '<' {
			if skipUntil != "" {
				i++
				continue
			}
			if inTitle {
				title.WriteRune(runes[i])
			} else {
				text.WriteRune(runes[i])
			}
			i++
			continue
		}

		// A comment runs to -->.
		if strings.HasPrefix(string(runes[i:min(i+4, len(runes))]), "<!--") {
			end := indexFrom(runes, i, "-->")
			if end < 0 {
				break
			}
			i = end + 3
			continue
		}

		tagEnd := indexRuneFrom(runes, i, '>')
		if tagEnd < 0 {
			break // unterminated tag: treat the rest as gone
		}
		tag := strings.ToLower(strings.TrimSpace(string(runes[i+1 : tagEnd])))
		i = tagEnd + 1

		switch {
		case skipUntil != "":
			if strings.HasPrefix(tag, "/"+skipUntil) {
				skipUntil = ""
			}
		case strings.HasPrefix(tag, "script"):
			skipUntil = "script"
		case strings.HasPrefix(tag, "style"):
			skipUntil = "style"
		case strings.HasPrefix(tag, "noscript"):
			skipUntil = "noscript"
		case strings.HasPrefix(tag, "title"):
			inTitle = true
		case strings.HasPrefix(tag, "/title"):
			inTitle = false
		default:
			// A block-level tag is a word boundary. Without this, "<p>one</p>
			// <p>two</p>" becomes "onetwo" and both words are lost.
			if isBlockTag(tag) {
				text.WriteRune('\n')
			} else {
				text.WriteRune(' ')
			}
		}
	}

	return strings.TrimSpace(decodeEntities(collapse(title.String()))),
		strings.TrimSpace(decodeEntities(collapse(text.String())))
}

// ExtractLinks returns absolute URLs from the href attributes in a document.
func ExtractLinks(html string, base *url.URL) []*url.URL {
	var out []*url.URL
	seen := make(map[string]bool)

	lower := strings.ToLower(html)
	cursor := 0

	for {
		idx := strings.Index(lower[cursor:], "href")
		if idx < 0 {
			break
		}
		pos := cursor + idx + 4

		// Skip whitespace and the '='.
		for pos < len(html) && (html[pos] == ' ' || html[pos] == '\t' || html[pos] == '\n') {
			pos++
		}
		if pos >= len(html) || html[pos] != '=' {
			cursor = pos
			continue
		}
		pos++
		for pos < len(html) && (html[pos] == ' ' || html[pos] == '\t' || html[pos] == '\n') {
			pos++
		}
		if pos >= len(html) {
			break
		}

		var raw string
		if html[pos] == '"' || html[pos] == '\'' {
			quote := html[pos]
			pos++
			end := strings.IndexByte(html[pos:], quote)
			if end < 0 {
				break
			}
			raw = html[pos : pos+end]
			cursor = pos + end + 1
		} else {
			end := pos
			for end < len(html) && html[end] != ' ' && html[end] != '>' && html[end] != '\t' {
				end++
			}
			raw = html[pos:end]
			cursor = end
		}

		raw = strings.TrimSpace(decodeEntities(raw))
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		// Only http(s). A mailto:, javascript: or data: href is not a page.
		lowerRaw := strings.ToLower(raw)
		if strings.HasPrefix(lowerRaw, "javascript:") ||
			strings.HasPrefix(lowerRaw, "mailto:") ||
			strings.HasPrefix(lowerRaw, "tel:") ||
			strings.HasPrefix(lowerRaw, "data:") {
			continue
		}

		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		// Relative links are resolved against the page they came from.
		resolved := base.ResolveReference(parsed)
		if resolved.Scheme != "http" && resolved.Scheme != "https" {
			continue
		}
		if seen[resolved.String()] {
			continue
		}
		seen[resolved.String()] = true
		out = append(out, resolved)
	}
	return out
}

func isBlockTag(tag string) bool {
	name := tag
	if i := strings.IndexAny(name, " \t\n/"); i >= 0 {
		name = name[:i]
	}
	name = strings.TrimPrefix(name, "/")

	switch name {
	case "p", "div", "br", "li", "tr", "td", "th", "h1", "h2", "h3", "h4",
		"h5", "h6", "section", "article", "header", "footer", "nav", "aside",
		"blockquote", "pre", "ul", "ol", "table", "hr", "main", "figure":
		return true
	}
	return false
}

// collapse reduces runs of whitespace to single spaces.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// decodeEntities handles the entities that actually occur in prose.
//
// A full table has well over a thousand entries and buys almost nothing for an
// index: an undecoded &hellip; costs one garbage term in one document.
func decodeEntities(s string) string {
	if !strings.Contains(s, "&") {
		return s
	}
	replacer := strings.NewReplacer(
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&#39;", "'",
		"&apos;", "'",
		"&nbsp;", " ",
		"&mdash;", "-",
		"&ndash;", "-",
		"&hellip;", "...",
		"&rsquo;", "'",
		"&lsquo;", "'",
		"&ldquo;", `"`,
		"&rdquo;", `"`,
	)
	return replacer.Replace(s)
}

func indexFrom(runes []rune, start int, needle string) int {
	s := string(runes[start:])
	i := strings.Index(s, needle)
	if i < 0 {
		return -1
	}
	return start + len([]rune(s[:i]))
}

func indexRuneFrom(runes []rune, start int, target rune) int {
	for i := start; i < len(runes); i++ {
		if runes[i] == target {
			return i
		}
	}
	return -1
}

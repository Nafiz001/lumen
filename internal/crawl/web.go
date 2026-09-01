package crawl

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// The web crawler, and the rules it follows.
//
// A crawler is a program that makes requests to a server you do not own, in a
// loop, as fast as it can. Written carelessly it is indistinguishable from a
// denial of service, and it will get the operator blocked -- deservedly. So the
// politeness here is not decoration:
//
//   - robots.txt is fetched and obeyed before anything else is requested.
//   - Requests to one host are spaced by a delay, serialised per host.
//   - A real User-Agent identifies the crawler, so an operator can see who it
//     is and block it if they want to.
//   - The crawl stays on the host it started from, unless told otherwise.
//   - Depth and page count are bounded, so a mistake stops on its own.
//
// The last one matters more than it sounds: the web is an infinite graph. A
// calendar with a "next month" link generates pages forever, and a crawler
// without a bound will happily follow it until the disk fills.

// WebOptions configure a site crawl.
type WebOptions struct {
	// MaxPages stops the crawl after this many documents.
	MaxPages int
	// MaxDepth limits how far from the start URL to follow links.
	MaxDepth int
	// Delay is the minimum gap between requests to the same host.
	Delay time.Duration
	// Timeout bounds a single request.
	Timeout time.Duration
	// UserAgent identifies the crawler to servers.
	UserAgent string
	// SameHostOnly keeps the crawl on the starting host.
	SameHostOnly bool
	// IgnoreRobots disables robots.txt. It exists because crawling your own
	// site is a legitimate reason to skip it, and it is off by default.
	IgnoreRobots bool
	// MaxBodySize caps a single response.
	MaxBodySize int64
}

// DefaultWebOptions returns polite defaults.
func DefaultWebOptions() WebOptions {
	return WebOptions{
		MaxPages: 500,
		MaxDepth: 3,
		// One request per second per host. Slower than necessary for a small
		// site and the right default: the cost of being too slow is waiting,
		// and the cost of being too fast is someone else's server.
		Delay:        1 * time.Second,
		Timeout:      15 * time.Second,
		UserAgent:    "lumen/0.1 (private search engine; +https://github.com/nafizahmed/lumen)",
		SameHostOnly: true,
		MaxBodySize:  5 << 20,
	}
}

// WebCrawler fetches and follows links from a starting URL.
type WebCrawler struct {
	opts   WebOptions
	client *http.Client

	mu      sync.Mutex
	visited map[string]bool
	lastHit map[string]time.Time
	robots  map[string]*robotsRules
	fetched int
	skipped int
}

func NewWebCrawler(opts WebOptions) *WebCrawler {
	defaults := DefaultWebOptions()
	if opts.MaxPages <= 0 {
		opts.MaxPages = defaults.MaxPages
	}
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = defaults.MaxDepth
	}
	if opts.Delay <= 0 {
		opts.Delay = defaults.Delay
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaults.Timeout
	}
	if opts.UserAgent == "" {
		opts.UserAgent = defaults.UserAgent
	}
	if opts.MaxBodySize <= 0 {
		opts.MaxBodySize = defaults.MaxBodySize
	}

	return &WebCrawler{
		opts:    opts,
		client:  &http.Client{Timeout: opts.Timeout},
		visited: make(map[string]bool),
		lastHit: make(map[string]time.Time),
		robots:  make(map[string]*robotsRules),
	}
}

// Stats reports what the crawl did.
type Stats struct {
	Fetched int
	Skipped int
}

func (c *WebCrawler) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{Fetched: c.fetched, Skipped: c.skipped}
}

// queued is one URL waiting to be crawled.
type queued struct {
	url   string
	depth int
}

// Crawl walks a site breadth-first from start.
//
// Breadth-first rather than depth-first, deliberately: pages near the entry
// point are usually the important ones, so if the page budget runs out the
// crawl has spent it on the parts of the site that matter most. Depth-first
// would burrow into one corner and never see the rest.
func (c *WebCrawler) Crawl(ctx context.Context, start string, emit func(Doc) error) (int, error) {
	startURL, err := url.Parse(start)
	if err != nil {
		return 0, fmt.Errorf("invalid start URL %q: %w", start, err)
	}
	if startURL.Scheme != "http" && startURL.Scheme != "https" {
		return 0, fmt.Errorf("unsupported scheme %q; use http or https", startURL.Scheme)
	}

	queue := []queued{{url: normalizeURL(startURL), depth: 0}}
	indexed := 0

	for len(queue) > 0 && indexed < c.opts.MaxPages {
		select {
		case <-ctx.Done():
			return indexed, ctx.Err()
		default:
		}

		item := queue[0]
		queue = queue[1:]

		c.mu.Lock()
		if c.visited[item.url] {
			c.mu.Unlock()
			continue
		}
		c.visited[item.url] = true
		c.mu.Unlock()

		parsed, err := url.Parse(item.url)
		if err != nil {
			continue
		}

		if !c.opts.IgnoreRobots && !c.allowed(ctx, parsed) {
			c.mu.Lock()
			c.skipped++
			c.mu.Unlock()
			continue
		}

		c.waitForHost(parsed.Host)

		body, contentType, err := c.fetch(ctx, item.url)
		if err != nil {
			c.mu.Lock()
			c.skipped++
			c.mu.Unlock()
			continue
		}

		c.mu.Lock()
		c.fetched++
		c.mu.Unlock()

		// Only HTML is followed for links; anything else is indexed as text if
		// it looks like text and skipped otherwise.
		if !strings.Contains(contentType, "html") {
			if strings.HasPrefix(contentType, "text/") {
				if err := emit(Doc{URI: item.url, Title: item.url, Body: body}); err != nil {
					return indexed, err
				}
				indexed++
			}
			continue
		}

		title, text := ExtractTitleAndText(body)
		if title == "" {
			title = item.url
		}
		if err := emit(Doc{URI: item.url, Title: title, Body: text}); err != nil {
			return indexed, err
		}
		indexed++

		if item.depth >= c.opts.MaxDepth {
			continue
		}
		for _, link := range ExtractLinks(body, parsed) {
			if c.opts.SameHostOnly && link.Host != startURL.Host {
				continue
			}
			normalized := normalizeURL(link)
			c.mu.Lock()
			seen := c.visited[normalized]
			c.mu.Unlock()
			if !seen {
				queue = append(queue, queued{url: normalized, depth: item.depth + 1})
			}
		}
	}

	return indexed, nil
}

// waitForHost enforces the per-host delay.
//
// Serialising by host rather than globally is what lets a crawl of several
// hosts proceed at a reasonable overall rate while still being gentle with
// each one.
func (c *WebCrawler) waitForHost(host string) {
	c.mu.Lock()
	last, seen := c.lastHit[host]
	c.mu.Unlock()

	if seen {
		if elapsed := time.Since(last); elapsed < c.opts.Delay {
			time.Sleep(c.opts.Delay - elapsed)
		}
	}

	c.mu.Lock()
	c.lastHit[host] = time.Now()
	c.mu.Unlock()
}

func (c *WebCrawler) fetch(ctx context.Context, rawURL string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", c.opts.UserAgent)
	req.Header.Set("Accept", "text/html,text/plain;q=0.9,*/*;q=0.1")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("status %d", resp.StatusCode)
	}

	// The body is capped even though the server declared a length, because a
	// server can declare anything.
	limited := io.LimitReader(resp.Body, c.opts.MaxBodySize)
	data, err := io.ReadAll(limited)
	if err != nil {
		return "", "", err
	}
	return string(data), strings.ToLower(resp.Header.Get("Content-Type")), nil
}

// normalizeURL reduces a URL to a canonical form for deduplication.
//
// Without this, /page, /page#section and /page?utm_source=x are three
// different crawl targets holding identical content -- and a crawler that
// treats them separately will index the same page many times and exhaust its
// budget on duplicates.
func normalizeURL(u *url.URL) string {
	clean := *u
	// A fragment never changes what the server returns.
	clean.Fragment = ""
	// Tracking parameters change the URL and not the content.
	if clean.RawQuery != "" {
		values := clean.Query()
		for key := range values {
			lower := strings.ToLower(key)
			if strings.HasPrefix(lower, "utm_") || lower == "fbclid" || lower == "gclid" {
				values.Del(key)
			}
		}
		clean.RawQuery = values.Encode()
	}
	// A trailing slash on a path is almost always the same resource.
	if clean.Path != "/" {
		clean.Path = strings.TrimSuffix(clean.Path, "/")
	}
	if clean.Path == "" {
		clean.Path = "/"
	}
	clean.Host = strings.ToLower(clean.Host)
	return clean.String()
}

// --- robots.txt ---

// robotsRules is the subset of robots.txt that matters here.
type robotsRules struct {
	disallow  []string
	allow     []string
	crawlWait time.Duration
}

// allowed reports whether robots.txt permits fetching a URL.
//
// The file is fetched once per host and cached. A host whose robots.txt cannot
// be fetched is treated as permitting everything, which is the conventional
// reading: a missing file means no restrictions, and a server that is down
// cannot be asked.
func (c *WebCrawler) allowed(ctx context.Context, u *url.URL) bool {
	c.mu.Lock()
	rules, cached := c.robots[u.Host]
	c.mu.Unlock()

	if !cached {
		rules = c.fetchRobots(ctx, u)
		c.mu.Lock()
		c.robots[u.Host] = rules
		c.mu.Unlock()
	}
	if rules == nil {
		return true
	}
	return rules.permits(u.Path)
}

func (c *WebCrawler) fetchRobots(ctx context.Context, u *url.URL) *robotsRules {
	robotsURL := u.Scheme + "://" + u.Host + "/robots.txt"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, robotsURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", c.opts.UserAgent)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return nil
	}
	return parseRobots(string(data))
}

// parseRobots reads the directives that apply to every crawler.
//
// Only the `User-agent: *` group is honoured, which is the right reading for a
// crawler that does not claim a registered name. A group naming a specific
// agent is not addressed to this one.
func parseRobots(text string) *robotsRules {
	rules := &robotsRules{}
	applies := false

	for _, line := range strings.Split(text, "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		directive := strings.ToLower(strings.TrimSpace(parts[0]))
		value := strings.TrimSpace(parts[1])

		switch directive {
		case "user-agent":
			applies = value == "*"
		case "disallow":
			if applies && value != "" {
				rules.disallow = append(rules.disallow, value)
			}
		case "allow":
			if applies && value != "" {
				rules.allow = append(rules.allow, value)
			}
		case "crawl-delay":
			if applies {
				var seconds float64
				if _, err := fmt.Sscanf(value, "%f", &seconds); err == nil && seconds > 0 {
					rules.crawlWait = time.Duration(seconds * float64(time.Second))
				}
			}
		}
	}
	return rules
}

// permits applies the rules to a path.
//
// A longer match wins, and Allow beats Disallow at equal length -- which is how
// a site says "stay out of /private except /private/public".
func (r *robotsRules) permits(path string) bool {
	if path == "" {
		path = "/"
	}

	longestDisallow := -1
	for _, rule := range r.disallow {
		if strings.HasPrefix(path, rule) && len(rule) > longestDisallow {
			longestDisallow = len(rule)
		}
	}
	if longestDisallow < 0 {
		return true
	}

	longestAllow := -1
	for _, rule := range r.allow {
		if strings.HasPrefix(path, rule) && len(rule) > longestAllow {
			longestAllow = len(rule)
		}
	}
	return longestAllow >= longestDisallow
}

// CrawlDelayFor returns the delay a host's robots.txt asked for, if any.
func (c *WebCrawler) CrawlDelayFor(host string) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if rules, ok := c.robots[host]; ok && rules != nil {
		return rules.crawlWait
	}
	return 0
}

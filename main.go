package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

type PageWordCounts struct {
	URL        string         `json:"url"`
	WordCounts map[string]int `json:"word_counts"`
	TotalWords int            `json:"total_words"`
}

type SiteReport struct {
	SeedURL       string           `json:"seed_url"`
	PagesVisited  int              `json:"pages_visited"`
	UniqueWords   int              `json:"unique_words"`
	TotalWords    int              `json:"total_words"`
	AggregatedTop []WordCountEntry `json:"aggregated_top_50"`
	Pages         []PageWordCounts `json:"pages"`
}

type WordCountEntry struct {
	Word  string `json:"word"`
	Count int    `json:"count"`
}

type GlobalVisited struct {
	mu      sync.Mutex
	visited map[string]bool
}

func NewGlobalVisited() *GlobalVisited {
	return &GlobalVisited{visited: make(map[string]bool)}
}

func (g *GlobalVisited) MarkVisited(url string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.visited[url] {
		return true
	}
	g.visited[url] = true
	return false
}

func (g *GlobalVisited) Count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.visited)
}

type crawlJob struct {
	url   string
	depth int
}

type Crawler struct {
	baseURL    *url.URL
	global     *GlobalVisited
	mu         sync.Mutex
	wordCounts map[string]int
	pageCounts []PageWordCounts
	maxDepth   int
	workers    int
	queue      chan crawlJob
	wg         sync.WaitGroup
	client     *http.Client
}

func NewCrawler(rawURL string, maxDepth int, global *GlobalVisited) (*Crawler, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}
	return &Crawler{
		baseURL:    parsed,
		global:     global,
		wordCounts: make(map[string]int),
		pageCounts: []PageWordCounts{},
		maxDepth:   maxDepth,
		workers:    5,
		queue:      make(chan crawlJob, 1000),
		client:     &http.Client{Timeout: 15 * time.Second},
	}, nil
}

func (c *Crawler) Run(seedURL string) {
	for i := 0; i < c.workers; i++ {
		go c.worker(i)
	}

	c.enqueue(seedURL, 0)
	c.wg.Wait()
}

func (c *Crawler) enqueue(rawURL string, depth int) {
	if depth > c.maxDepth {
		return
	}

	normalized := c.normalizeURL(rawURL)
	if normalized == "" || !c.isSameHost(normalized) {
		return
	}

	if isFileURL(normalized) {
		log.Printf("[enqueue] SKIP file URL %s", normalized)
		return
	}

	if alreadyVisited := c.global.MarkVisited(normalized); alreadyVisited {
		return
	}

	c.wg.Add(1)
	c.queue <- crawlJob{url: normalized, depth: depth}
}

func (c *Crawler) worker(id int) {
	for job := range c.queue {
		c.processPage(id, job)
		c.wg.Done()
	}
}

func (c *Crawler) processPage(workerID int, job crawlJob) {
	visited := c.global.Count()
	log.Printf("[worker %d] #%d depth=%d %s", workerID, visited, job.depth, job.url)

	text, links, err := c.fetchPage(job.url)
	if err != nil {
		log.Printf("[worker %d] ERROR %s: %v", workerID, job.url, err)
		return
	}

	words := tokenize(text)
	pageWords := make(map[string]int)
	for _, w := range words {
		pageWords[w]++
	}
	c.mu.Lock()
	for w, count := range pageWords {
		c.wordCounts[w] += count
	}
	c.pageCounts = append(c.pageCounts, PageWordCounts{
		URL:        job.url,
		WordCounts: pageWords,
		TotalWords: len(words),
	})
	c.mu.Unlock()

	var queued int
	for _, link := range links {
		resolved := c.normalizeURL(link)
		if resolved == "" || !c.isSameHost(resolved) {
			continue
		}
		c.enqueue(resolved, job.depth+1)
		queued++
	}
	log.Printf("[worker %d] DONE %s — %d words, %d links, %d queued", workerID, job.url, len(words), len(links), queued)
}

func (c *Crawler) fetchPage(pageURL string) (text string, links []string, err error) {
	start := time.Now()
	log.Printf("[fetch] START %s", pageURL)

	resp, err := c.client.Get(pageURL)
	if err != nil {
		return "", nil, fmt.Errorf("GET: %w", err)
	}
	defer resp.Body.Close()
	log.Printf("[fetch] got response %d in %v", resp.StatusCode, time.Since(start))

	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "text/html") {
		return "", nil, fmt.Errorf("not HTML: %s", contentType)
	}

	doc, err := html.Parse(resp.Body)
	if err != nil {
		return "", nil, fmt.Errorf("parse: %w", err)
	}

	text = extractText(doc)
	links = extractLinks(doc, pageURL)

	log.Printf("[fetch] DONE %s — %d chars, %d links in %v", pageURL, len(text), len(links), time.Since(start))
	return text, links, nil
}

func extractText(n *html.Node) string {
	if n.Type == html.ElementNode {
		switch n.Data {
		case "script", "style", "noscript", "head":
			return ""
		}
	}
	if n.Type == html.TextNode {
		return n.Data
	}
	var sb strings.Builder
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		sb.WriteString(extractText(child))
		sb.WriteString(" ")
	}
	return sb.String()
}

func extractLinks(n *html.Node, baseURL string) []string {
	var links []string
	base, _ := url.Parse(baseURL)

	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, attr := range n.Attr {
				if attr.Key == "href" {
					ref, err := url.Parse(attr.Val)
					if err != nil {
						continue
					}
					resolved := base.ResolveReference(ref)
					resolved.Fragment = ""
					links = append(links, resolved.String())
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			visit(child)
		}
	}
	visit(n)
	return links
}

func (c *Crawler) normalizeURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	parsed = c.baseURL.ResolveReference(parsed)
	parsed.Fragment = ""
	return parsed.String()
}

func (c *Crawler) isSameHost(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return parsed.Host == c.baseURL.Host
}

var skipExtensions = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true, ".ico": true, ".webp": true,
	".pdf": true, ".doc": true, ".docx": true, ".xls": true, ".xlsx": true, ".ppt": true, ".pptx": true,
	".zip": true, ".tar": true, ".gz": true, ".bz2": true, ".7z": true, ".rar": true,
	".mp3": true, ".mp4": true, ".wav": true, ".avi": true, ".mov": true, ".webm": true, ".ogg": true,
	".woff": true, ".woff2": true, ".ttf": true, ".eot": true, ".otf": true,
	".css": true, ".js": true, ".map": true, ".mjs": true,
	".exe": true, ".bin": true, ".dmg": true, ".iso": true, ".apk": true,
	".xml": true, ".json": true, ".yaml": true, ".yml": true, ".toml": true,
	".csv": true, ".tsv": true,
}

func isFileURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	path := strings.ToLower(parsed.Path)
	lastDot := strings.LastIndex(path, ".")
	if lastDot == -1 {
		return false
	}
	ext := path[lastDot:]
	return skipExtensions[ext]
}

var wordRegex = regexp.MustCompile(`[a-zA-Z]+`)

func tokenize(text string) []string {
	matches := wordRegex.FindAllString(text, -1)
	var words []string
	for _, m := range matches {
		words = append(words, strings.ToLower(m))
	}
	return words
}

func (c *Crawler) sortedCounts() []WordCountEntry {
	var results []WordCountEntry
	for w, count := range c.wordCounts {
		results = append(results, WordCountEntry{w, count})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].Count > results[j].Count
	})
	return results
}

func (c *Crawler) PrintResults() {
	results := c.sortedCounts()

	fmt.Printf("\n=== Word Counts (%d unique words, %d pages crawled) ===\n\n",
		len(results), len(c.pageCounts))

	for i, wc := range results {
		fmt.Printf("%-30s %d\n", wc.Word, wc.Count)
		if i >= 49 {
			fmt.Printf("\n... and %d more words\n", len(results)-50)
			break
		}
	}
}

func (c *Crawler) WriteJSON(filename string) error {
	results := c.sortedCounts()
	top50 := results
	if len(top50) > 50 {
		top50 = top50[:50]
	}

	totalWords := 0
	for _, count := range c.wordCounts {
		totalWords += count
	}

	report := SiteReport{
		SeedURL:       c.baseURL.String(),
		PagesVisited:  len(c.pageCounts),
		UniqueWords:   len(c.wordCounts),
		TotalWords:    totalWords,
		AggregatedTop: top50,
		Pages:         c.pageCounts,
	}

	f, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("creating %s: %w", filename, err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return fmt.Errorf("writing JSON: %w", err)
	}

	fmt.Printf("JSON report written to %s\n", filename)
	return nil
}

func sanitizeFilename(rawURL string) string {
	parsed, _ := url.Parse(rawURL)
	name := parsed.Host + strings.ReplaceAll(parsed.Path, "/", "_")
	name = regexp.MustCompile(`[^a-zA-Z0-9._-]`).ReplaceAllString(name, "_")
	name = strings.Trim(name, "_")
	if name == "" {
		name = "site"
	}
	return name + ".json"
}

func writeResults(filename string, pages []PageWordCounts) error {
	f, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("creating %s: %w", filename, err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(pages); err != nil {
		return fmt.Errorf("writing JSON: %w", err)
	}

	log.Printf("[results] wrote %d pages to %s", len(pages), filename)
	return nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <urls-file> [max-depth]\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  urls-file: text file with one URL per line\n")
		fmt.Fprintf(os.Stderr, "  max-depth: how many link levels to follow (default: 2)\n")
		os.Exit(1)
	}

	urlsFile := os.Args[1]
	maxDepth := 2
	if len(os.Args) >= 3 {
		fmt.Sscanf(os.Args[2], "%d", &maxDepth)
	}

	f, err := os.Open(urlsFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error opening %s: %v\n", urlsFile, err)
		os.Exit(1)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var urls []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			urls = append(urls, line)
		}
	}

	if len(urls) == 0 {
		fmt.Fprintln(os.Stderr, "No URLs found in file")
		os.Exit(1)
	}

	globalVisited := NewGlobalVisited()
	var allPages []PageWordCounts

	for _, rawURL := range urls {
		fmt.Printf("\n--- Starting crawl from: %s ---\n", rawURL)
		crawler, err := NewCrawler(rawURL, maxDepth, globalVisited)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Skipping %s: %v\n", rawURL, err)
			continue
		}
		crawler.Run(rawURL)
		crawler.PrintResults()

		jsonFile := sanitizeFilename(rawURL)
		if err := crawler.WriteJSON(jsonFile); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing JSON: %v\n", err)
		}

		allPages = append(allPages, crawler.pageCounts...)
	}

	if err := writeResults("results.json", allPages); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing results.json: %v\n", err)
	}
}

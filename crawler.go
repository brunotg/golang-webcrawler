package main

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/net/html"
	"golang.org/x/net/publicsuffix"
)

const maxConcurrentPerSite = 5

var httpClient = &http.Client{
	Timeout: 30 * time.Second,
}

type PageResult struct {
	URL      string
	WordFreq map[string]int
	Err      string
}

func (p PageResult) WordCount() int {
	total := 0
	for _, c := range p.WordFreq {
		total += c
	}
	return total
}

type CrawlResult struct {
	StartURL   string
	Pages      []PageResult
	TotalWords int
}

func crawlSite(startURL string) CrawlResult {
	base, err := url.Parse(startURL)
	if err != nil {
		return CrawlResult{
			StartURL: startURL,
			Pages:    []PageResult{{URL: startURL, Err: err.Error()}},
		}
	}

	var (
		mu      sync.Mutex
		visited = map[string]bool{normalizeURL(startURL): true}
		pages   []PageResult
		wg      sync.WaitGroup
		sem     = make(chan struct{}, maxConcurrentPerSite)
	)

	var crawlPage func(pageURL string)
	crawlPage = func(pageURL string) {
		defer wg.Done()

		sem <- struct{}{}
		links, freq, finalURL, fetchErr := fetchAndParse(pageURL)
		<-sem

		result := PageResult{URL: pageURL, WordFreq: freq}
		if fetchErr != nil {
			result.Err = fetchErr.Error()
		}

		mu.Lock()
		if finalURL != "" {
			visited[normalizeURL(finalURL)] = true
		}
		pages = append(pages, result)
		for _, link := range links {
			norm := normalizeURL(link)
			if !visited[norm] && isSameDomain(base, link) {
				visited[norm] = true
				wg.Add(1)
				go crawlPage(link)
			}
		}
		mu.Unlock()
	}

	wg.Add(1)
	go crawlPage(startURL)
	wg.Wait()

	var total int
	for _, p := range pages {
		total += p.WordCount()
	}

	return CrawlResult{StartURL: startURL, Pages: pages, TotalWords: total}
}

func fetchAndParse(pageURL string) (links []string, wordCount map[string]int, finalURL string, err error) {
	req, err := http.NewRequest(http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, nil, "", err
	}
	req.Header.Set("User-Agent", "webcrawler/1.0")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, nil, "", err
	}
	defer resp.Body.Close()

	finalURL = resp.Request.URL.String()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, finalURL, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		return nil, nil, finalURL, nil
	}

	doc, err := html.Parse(resp.Body)
	if err != nil {
		return nil, nil, finalURL, err
	}

	links = extractLinks(doc, finalURL)
	wordCount = countWordFreq(doc)
	return links, wordCount, finalURL, nil
}

func extractLinks(doc *html.Node, baseURL string) []string {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil
	}

	var links []string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, attr := range n.Attr {
				if attr.Key == "href" {
					ref, err := url.Parse(strings.TrimSpace(attr.Val))
					if err != nil {
						continue
					}
					resolved := base.ResolveReference(ref)
					resolved.Fragment = ""
					if resolved.Scheme == "http" || resolved.Scheme == "https" {
						links = append(links, resolved.String())
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return links
}

func countWordFreq(doc *html.Node) map[string]int {
	freq := map[string]int{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "noscript", "head":
				return
			}
		}
		if n.Type == html.TextNode {
			for _, raw := range strings.Fields(n.Data) {
				if w := cleanWord(raw); w != "" {
					freq[w]++
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return freq
}

func cleanWord(w string) string {
	w = strings.ToLower(w)
	w = strings.TrimFunc(w, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	return w
}

func normalizeURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.Fragment = ""
	return strings.ToLower(u.String())
}

func isSameDomain(base *url.URL, rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	baseDomain, err := publicsuffix.EffectiveTLDPlusOne(base.Hostname())
	if err != nil {
		return strings.EqualFold(u.Hostname(), base.Hostname())
	}
	targetDomain, err := publicsuffix.EffectiveTLDPlusOne(u.Hostname())
	if err != nil {
		return false
	}
	return strings.EqualFold(baseDomain, targetDomain)
}

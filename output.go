package main

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type detailJSON struct {
	StartURL     string            `json:"start_url"`
	PagesVisited int               `json:"pages_visited"`
	TotalWords   int               `json:"total_words"`
	TopWords     []wordEntry       `json:"top_50_words"`
	Subdomains   []subdomainResult `json:"subdomains"`
}

type subdomainResult struct {
	Host         string      `json:"host"`
	PagesVisited int         `json:"pages_visited"`
	TotalWords   int         `json:"total_words"`
	TopWords     []wordEntry `json:"top_50_words"`
	Pages        []pageJSON  `json:"pages"`
}

type wordEntry struct {
	Word  string `json:"word"`
	Count int    `json:"count"`
}

type pageJSON struct {
	URL       string `json:"url"`
	WordCount int    `json:"word_count"`
	Error     string `json:"error,omitempty"`
}

type summaryJSON struct {
	SitesCrawled    int           `json:"sites_crawled"`
	GrandTotalWords int           `json:"grand_total_words"`
	Results         []summaryItem `json:"results"`
}

type summaryItem struct {
	StartURL     string `json:"start_url"`
	PagesVisited int    `json:"pages_visited"`
	TotalWords   int    `json:"total_words"`
}

func writeDetailFile(result CrawlResult) error {
	grouped := map[string][]PageResult{}
	for _, p := range result.Pages {
		host := hostOf(p.URL)
		grouped[host] = append(grouped[host], p)
	}

	hosts := make([]string, 0, len(grouped))
	for h := range grouped {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)

	allFreq := map[string]int{}
	var subdomains []subdomainResult

	for _, host := range hosts {
		pages := grouped[host]
		sort.Slice(pages, func(i, j int) bool {
			return pages[i].URL < pages[j].URL
		})

		hostFreq := map[string]int{}
		var pjs []pageJSON
		for _, p := range pages {
			wc := 0
			for word, count := range p.WordFreq {
				hostFreq[word] += count
				allFreq[word] += count
				wc += count
			}
			pjs = append(pjs, pageJSON{URL: p.URL, WordCount: wc, Error: p.Err})
		}

		subdomains = append(subdomains, subdomainResult{
			Host:         host,
			PagesVisited: len(pages),
			TotalWords:   freqTotal(hostFreq),
			TopWords:     topWords(hostFreq, 50),
			Pages:        pjs,
		})
	}

	out := detailJSON{
		StartURL:     result.StartURL,
		PagesVisited: len(result.Pages),
		TotalWords:   result.TotalWords,
		TopWords:     topWords(allFreq, 50),
		Subdomains:   subdomains,
	}

	return writeJSON(filepath.Join("output", sanitizeFilename(result.StartURL)+".json"), out)
}

func writeSummaryFile(results []CrawlResult) error {
	var grandTotal int
	for _, r := range results {
		grandTotal += r.TotalWords
	}

	out := summaryJSON{
		SitesCrawled:    len(results),
		GrandTotalWords: grandTotal,
	}
	for _, r := range results {
		out.Results = append(out.Results, summaryItem{
			StartURL:     r.StartURL,
			PagesVisited: len(r.Pages),
			TotalWords:   r.TotalWords,
		})
	}

	return writeJSON(filepath.Join("output", "summary.json"), out)
}

func topWords(freq map[string]int, n int) []wordEntry {
	entries := make([]wordEntry, 0, len(freq))
	for w, c := range freq {
		entries = append(entries, wordEntry{Word: w, Count: c})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Count != entries[j].Count {
			return entries[i].Count > entries[j].Count
		}
		return entries[i].Word < entries[j].Word
	})
	if len(entries) > n {
		entries = entries[:n]
	}
	return entries
}

func freqTotal(freq map[string]int) int {
	total := 0
	for _, c := range freq {
		total += c
	}
	return total
}

func writeJSON(path string, v any) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	return strings.ToLower(u.Hostname())
}

var nonAlphanumeric = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func sanitizeFilename(rawURL string) string {
	s := strings.TrimPrefix(rawURL, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimSuffix(s, "/")
	s = nonAlphanumeric.ReplaceAllString(s, "_")
	if len(s) > 100 {
		s = s[:100]
	}
	return s
}

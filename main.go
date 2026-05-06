package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
)

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: webcrawler <urls-file>")
	}

	urls, err := readURLsFromFile(os.Args[1])
	if err != nil {
		log.Fatalf("reading URLs: %v", err)
	}
	if len(urls) == 0 {
		log.Fatal("no URLs found in file")
	}

	if err := os.MkdirAll("output", 0755); err != nil {
		log.Fatalf("creating output directory: %v", err)
	}

	fmt.Printf("Crawling %d site(s)...\n", len(urls))

	results := make([]CrawlResult, len(urls))
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func(idx int, startURL string) {
			defer wg.Done()
			fmt.Printf("  starting: %s\n", startURL)
			results[idx] = crawlSite(startURL)
			r := results[idx]
			fmt.Printf("  finished: %s — %d pages, %d words\n", startURL, len(r.Pages), r.TotalWords)
		}(i, u)
	}
	wg.Wait()

	for _, r := range results {
		if err := writeDetailFile(r); err != nil {
			log.Printf("writing detail for %s: %v", r.StartURL, err)
		}
	}

	if err := writeSummaryFile(results); err != nil {
		log.Fatalf("writing summary: %v", err)
	}

	fmt.Println("Done. Output written to output/")
}

func readURLsFromFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var urls []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			urls = append(urls, line)
		}
	}
	return urls, scanner.Err()
}

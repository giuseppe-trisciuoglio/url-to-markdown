package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"

	md "github.com/JohannesKaufmann/html-to-markdown"
)

func main() {
	var verbose bool
	var outputFile string
	var doCrawl bool
	var maxPages int
	flag.BoolVar(&verbose, "v", false, "enable verbose logging")
	flag.StringVar(&outputFile, "o", "", "output filename (default: auto-generated from URL)")
	flag.BoolVar(&doCrawl, "crawl", false, "crawl entire domain and save all pages to a folder")
	flag.IntVar(&maxPages, "max", 100, "maximum pages to crawl (used with -crawl)")
	flag.Parse()

	args := flag.Args()
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "usage: %s [-v] [-o <file>] [-crawl] [-max N] <url>\n", filepath.Base(os.Args[0]))
		os.Exit(2)
	}
	rawURL := args[0]

	logger := func(string, ...interface{}) {}
	if verbose {
		logger = func(format string, values ...interface{}) {
			fmt.Fprintf(os.Stderr, format+"\n", values...)
		}
	}

	parsed, err := parseURL(rawURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid url: %v\n", err)
		os.Exit(2)
	}

	if doCrawl {
		if outputFile != "" {
			logger("warning: -o flag is ignored in crawl mode")
		}
		outDir := crawlDirName(parsed)
		logger("Crawling %s → ./%s/", parsed.String(), outDir)
		if err := os.MkdirAll(outDir, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "failed to create output dir: %v\n", err)
			os.Exit(1)
		}
		if err := crawlSite(parsed, outDir, maxPages, logger); err != nil {
			fmt.Fprintf(os.Stderr, "crawl failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	logger("Fetching %s …", parsed.String())
	body, isHTML, err := fetchHTML(ctx, parsed, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to download %s: %v\n", parsed, err)
		os.Exit(1)
	}

	var markdown string
	if isHTML {
		logger("Converting HTML to Markdown")
		markdown, err = convertToMarkdown(parsed, body)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to convert markup: %v\n", err)
			os.Exit(1)
		}
	} else {
		logger("Using preformatted Markdown response")
		markdown = string(body)
	}

	var filename string
	if outputFile != "" {
		filename = outputFile
	} else {
		filename = outputFilename(parsed)
	}
	logger("Saving to %s", filename)

	if err := os.WriteFile(filename, []byte(markdown), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write file: %v\n", err)
		os.Exit(1)
	}

	if verbose {
		fmt.Fprintf(os.Stderr, "Done. Wrote %s\n", filename)
	}
}

func parseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == "" {
		parsed.Scheme = "https"
	}

	if parsed.Host == "" {
		guessed, guessErr := url.Parse(parsed.Scheme + "://" + raw)
		if guessErr == nil && guessed.Host != "" {
			return guessed, nil
		}
		return nil, errors.New("missing host")
	}
	return parsed, nil
}

func fetchHTML(ctx context.Context, target *url.URL, logf func(string, ...interface{})) ([]byte, bool, error) {
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}

	hostBase := target.Scheme + "://" + target.Host

	// Warm-up request to capture any cookies/challenges that are required for the main document.
	if warmupReq, err := http.NewRequestWithContext(ctx, http.MethodGet, hostBase+"/", nil); err == nil {
		applyBrowserHeaders(warmupReq, target, false)
		if resp, err := client.Do(warmupReq); err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, false, err
	}
	applyBrowserHeaders(req, target, true)

	resp, err := client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	isCloudflare := strings.Contains(strings.ToLower(resp.Header.Get("Server")), "cloudflare")
	switch resp.StatusCode {
	case http.StatusForbidden, http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusServiceUnavailable:
		reason := fmt.Sprintf("Received %d from origin", resp.StatusCode)
		if isCloudflare {
			reason = "Hit Cloudflare challenge"
		}
		if fallback, err := fetchViaProxy(ctx, target); err == nil {
			logf("%s, fetched content via proxy", reason)
			return fallback, false, nil
		} else {
			logf("%s, proxy fallback failed: %v", reason, err)
			return nil, false, fmt.Errorf("%s and proxy fallback failed: %w", resp.Status, err)
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false, fmt.Errorf("HTTP status %s", resp.Status)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, err
	}

	// Check if the content is already Markdown (skip HTML conversion)
	contentType := resp.Header.Get("Content-Type")
	isMarkdown := strings.HasSuffix(strings.ToLower(target.Path), ".md") ||
		strings.Contains(contentType, "text/markdown") ||
		strings.Contains(contentType, "text/x-markdown")

	return data, !isMarkdown, nil
}

func convertToMarkdown(base *url.URL, html []byte) (string, error) {
	converter := md.NewConverter(base.String(), true, nil)
	return converter.ConvertString(string(html))
}

func outputFilename(u *url.URL) string {
	base := u.Host + u.Path
	base = strings.Trim(base, "/")

	if base == "" {
		base = u.Host
	}

	re := regexp.MustCompile(`[^A-Za-z0-9]+`)
	base = re.ReplaceAllString(base, "_")
	base = strings.Trim(base, "_")

	if base == "" {
		base = "output"
	}

	return base + ".md"
}

func crawlDirName(u *url.URL) string {
	re := regexp.MustCompile(`[^A-Za-z0-9]+`)
	name := re.ReplaceAllString(u.Host, "_")
	return strings.Trim(name, "_")
}

func crawlFilePath(outDir string, pageURL *url.URL) string {
	p := strings.Trim(pageURL.Path, "/")
	if unescaped, err := url.PathUnescape(p); err == nil {
		p = unescaped
	}
	if p == "" {
		return filepath.Join(outDir, "index.md")
	}
	p = strings.ReplaceAll(p, "/", string(filepath.Separator))
	if strings.HasSuffix(pageURL.Path, "/") {
		p = filepath.Join(outDir, p, "index.md")
	} else {
		// Strip common extensions before adding .md
		for _, ext := range []string{".html", ".htm", ".php", ".asp", ".aspx"} {
			if strings.HasSuffix(strings.ToLower(p), ext) {
				p = p[:len(p)-len(ext)]
				break
			}
		}
		p = filepath.Join(outDir, p+".md")
	}
	// Prevent path traversal: ensure result stays within outDir
	absOut, err1 := filepath.Abs(outDir)
	absFile, err2 := filepath.Abs(p)
	if err1 != nil || err2 != nil || !strings.HasPrefix(absFile, absOut+string(filepath.Separator)) {
		return ""
	}
	return p
}

func extractLinks(base *url.URL, htmlBytes []byte) []string {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(htmlBytes))
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var links []string
	doc.Find("a[href]").Each(func(_ int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		href = strings.TrimSpace(href)
		if href == "" || strings.HasPrefix(href, "#") ||
			strings.HasPrefix(href, "mailto:") ||
			strings.HasPrefix(href, "javascript:") {
			return
		}
		ref, err := url.Parse(href)
		if err != nil {
			return
		}
		resolved := base.ResolveReference(ref)
		resolved.Fragment = ""
		// Strip query strings to avoid duplicate pages on doc sites
		resolved.RawQuery = ""
		key := resolved.String()
		if !seen[key] {
			seen[key] = true
			links = append(links, key)
		}
	})
	return links
}

type crawlResult struct {
	pageURL  *url.URL
	links    []string
	markdown string
	err      error
}

func crawlSite(startURL *url.URL, outDir string, maxPages int, logf func(string, ...interface{})) error {
	const numWorkers = 3

	visited := make(map[string]bool)
	queue := []string{startURL.String()}
	count := 0

	// Channel for dispatching URLs to workers
	jobs := make(chan string, numWorkers)
	results := make(chan crawlResult, numWorkers)

	var wg sync.WaitGroup
	for i := 0; i < numWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rawURL := range jobs {
				parsed, err := url.Parse(rawURL)
				if err != nil {
					results <- crawlResult{err: fmt.Errorf("invalid url %s: %w", rawURL, err)}
					continue
				}

				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				body, isHTML, err := fetchHTML(ctx, parsed, logf)
				cancel()
				if err != nil {
					results <- crawlResult{pageURL: parsed, err: err}
					continue
				}

				var links []string
				if isHTML {
					links = extractLinks(parsed, body)
				}

				var markdown string
				if isHTML {
					markdown, err = convertToMarkdown(parsed, body)
					if err != nil {
						results <- crawlResult{pageURL: parsed, err: fmt.Errorf("conversion failed: %w", err)}
						continue
					}
				} else {
					markdown = string(body)
				}

				results <- crawlResult{
					pageURL:  parsed,
					links:    links,
					markdown: markdown,
				}
			}
		}()
	}

	// Dispatch work in batches
	saved := 0
	pending := 0
	for count < maxPages {
		// Fill worker slots from queue
		for pending < numWorkers && len(queue) > 0 && count < maxPages {
			current := queue[0]
			queue = queue[1:]
			if visited[current] {
				continue
			}
			visited[current] = true
			count++
			logf("[%d/%d] Fetching %s", count, maxPages, current)
			jobs <- current
			pending++
		}

		if pending == 0 {
			break
		}

		// Collect one result
		res := <-results
		pending--

		if res.err != nil {
			if res.pageURL != nil {
				logf("  skipping %s: %v", res.pageURL.String(), res.err)
			} else {
				logf("  skipping: %v", res.err)
			}
			continue
		}

		// Save file
		filePath := crawlFilePath(outDir, res.pageURL)
		if filePath == "" {
			logf("  skipping %s: unsafe path", res.pageURL.String())
			continue
		}
		if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
			logf("  skipping %s: mkdir failed: %v", res.pageURL.String(), err)
			continue
		}
		if err := os.WriteFile(filePath, []byte(res.markdown), 0644); err != nil {
			logf("  skipping %s: write failed: %v", res.pageURL.String(), err)
			continue
		}
		saved++

		// Enqueue new links
		for _, link := range res.links {
			parsed, err := url.Parse(link)
			if err != nil || parsed.Host != startURL.Host {
				continue
			}
			if !visited[link] {
				queue = append(queue, link)
			}
		}
	}

	close(jobs)
	wg.Wait()
	// Drain any remaining results
	close(results)
	for res := range results {
		if res.err != nil {
			continue
		}
		filePath := crawlFilePath(outDir, res.pageURL)
		if filePath == "" {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
			continue
		}
		if err := os.WriteFile(filePath, []byte(res.markdown), 0644); err != nil {
			continue
		}
		saved++
	}

	logf("Crawl complete. Saved %d/%d pages to ./%s/", saved, count, outDir)
	return nil
}

func applyBrowserHeaders(req *http.Request, target *url.URL, includeNavigation bool) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("Pragma", "no-cache")
	req.Header.Set("Sec-CH-UA", "\"Not/A)Brand\";v=\"8\", \"Chromium\";v=\"126\", \"Google Chrome\";v=\"126\"")
	req.Header.Set("Sec-CH-UA-Mobile", "?0")
	req.Header.Set("Sec-CH-UA-Platform", "\"macOS\"")
	if includeNavigation {
		req.Header.Set("Sec-Fetch-Dest", "document")
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		req.Header.Set("Sec-Fetch-Site", "none")
		req.Header.Set("Sec-Fetch-User", "?1")
		req.Header.Set("Upgrade-Insecure-Requests", "1")
	}
}

func fetchViaProxy(ctx context.Context, target *url.URL) ([]byte, error) {
	proxyURL := "https://r.jina.ai/" + target.String()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, proxyURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "url2md-proxy/1.0 (+https://github.com)")
	if key := strings.TrimSpace(os.Getenv("JINA_API_KEY")); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		if len(data) > 0 {
			detail := strings.TrimSpace(string(data))
			if len(detail) > 256 {
				detail = detail[:256] + "…"
			}
			return nil, fmt.Errorf("proxy request status %s: %s", resp.Status, detail)
		}
		return nil, fmt.Errorf("proxy request status %s", resp.Status)
	}

	return io.ReadAll(resp.Body)
}

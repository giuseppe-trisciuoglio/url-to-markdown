package main

import (
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
	"time"

	md "github.com/JohannesKaufmann/html-to-markdown"
)

func main() {
	var verbose bool
	flag.BoolVar(&verbose, "v", false, "enable verbose logging")
	flag.Parse()

	args := flag.Args()
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "usage: %s [-v] <url>\n", filepath.Base(os.Args[0]))
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

	filename := outputFilename(parsed)
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

	return data, true, nil
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

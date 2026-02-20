package main

import (
	"net/url"
	"testing"
)

func TestOutputFilename(t *testing.T) {
	cases := map[string]string{
		"https://springdoc.org":        "springdoc_org.md",
		"https://example.com/docs":     "example_com_docs.md",
		"https://example.com/docs/":    "example_com_docs.md",
		"https://docs.example.com":     "docs_example_com.md",
		"https://example.com/a/b?x=1":  "example_com_a_b.md",
		"https://example.com/a-b_c/d/": "example_com_a_b_c_d.md",
	}

	for raw, expected := range cases {
		u, err := parseURL(raw)
		if err != nil {
			t.Fatalf("parseURL(%q) returned error: %v", raw, err)
		}

		if got := outputFilename(u); got != expected {
			t.Fatalf("outputFilename(%q) = %q, expected %q", raw, got, expected)
		}
	}
}

func TestParseURLAddsScheme(t *testing.T) {
	u, err := parseURL("example.com/path")
	if err != nil {
		t.Fatalf("parseURL returned error: %v", err)
	}

	if u.Scheme != "https" {
		t.Fatalf("scheme = %q, expected https", u.Scheme)
	}

	if u.Host != "example.com" {
		t.Fatalf("host = %q, expected example.com", u.Host)
	}
}

func TestCrawlDirName(t *testing.T) {
	cases := map[string]string{
		"https://docs.sibill.com/":  "docs_sibill_com",
		"https://example.com":      "example_com",
		"https://example.com:8080": "example_com_8080",
	}
	for raw, expected := range cases {
		u, err := parseURL(raw)
		if err != nil {
			t.Fatalf("parseURL(%q) error: %v", raw, err)
		}
		if got := crawlDirName(u); got != expected {
			t.Fatalf("crawlDirName(%q) = %q, expected %q", raw, got, expected)
		}
	}
}

func TestCrawlFilePath(t *testing.T) {
	cases := []struct {
		path     string
		expected string
	}{
		{"/", "out/index.md"},
		{"", "out/index.md"},
		{"/api/auth", "out/api/auth.md"},
		{"/api/auth/", "out/api/auth/index.md"},
		{"/guide/intro.html", "out/guide/intro.md"},
		{"/page.htm", "out/page.md"},
		{"/docs", "out/docs.md"},
	}
	for _, tc := range cases {
		u := &url.URL{Scheme: "https", Host: "example.com", Path: tc.path}
		if got := crawlFilePath("out", u); got != tc.expected {
			t.Fatalf("crawlFilePath(\"out\", %q) = %q, expected %q", tc.path, got, tc.expected)
		}
	}
}

func TestCrawlFilePathTraversal(t *testing.T) {
	// Path traversal attempts must return empty string
	traversalPaths := []string{
		"/../../etc/passwd",
		"/../secret",
		"/a/../../b",
	}
	for _, p := range traversalPaths {
		u := &url.URL{Scheme: "https", Host: "example.com", Path: p}
		if got := crawlFilePath("out", u); got != "" {
			t.Fatalf("crawlFilePath should reject traversal path %q, got %q", p, got)
		}
	}
}

func TestExtractLinks(t *testing.T) {
	base, _ := url.Parse("https://example.com/docs/intro")
	html := []byte(`<html><body>
		<a href="/docs/guide">Guide</a>
		<a href="../api">API</a>
		<a href="https://external.com/page">External</a>
		<a href="#section">Anchor</a>
		<a href="mailto:test@test.com">Email</a>
		<a href="javascript:void(0)">JS</a>
		<a href="/docs/guide">Duplicate</a>
	</body></html>`)

	links := extractLinks(base, html)

	expected := map[string]bool{
		"https://example.com/docs/guide": true,
		"https://example.com/api":        true,
		"https://external.com/page":      true,
	}

	if len(links) != len(expected) {
		t.Fatalf("expected %d links, got %d: %v", len(expected), len(links), links)
	}
	for _, link := range links {
		if !expected[link] {
			t.Fatalf("unexpected link: %s", link)
		}
	}
}

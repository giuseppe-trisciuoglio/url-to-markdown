package main

import "testing"

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

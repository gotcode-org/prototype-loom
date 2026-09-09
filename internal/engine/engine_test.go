package engine

import (
	"strings"
	"testing"
)

func TestParseMarkdown(t *testing.T) {
	raw := []byte(`---
title: Hello World
author: socks
---
# Welcome
This is a **test**.
`)

	doc, err := ParseMarkdown(raw)
	if err != nil {
		t.Fatalf("ParseMarkdown failed: %v", err)
	}

	if doc.Frontmatter["title"] != "Hello World" {
		t.Errorf("Expected title 'Hello World', got %v", doc.Frontmatter["title"])
	}

	if doc.Frontmatter["author"] != "socks" {
		t.Errorf("Expected author 'socks', got %v", doc.Frontmatter["author"])
	}

	htmlOutput := string(doc.BodyHTML)
	if !strings.Contains(htmlOutput, `<h1 id="welcome">Welcome</h1>`) {
		t.Errorf("Expected <h1 id=\"welcome\">Welcome</h1>, got %s", htmlOutput)
	}
	if !strings.Contains(htmlOutput, "<strong>test</strong>") {
		t.Errorf("Expected <strong>test</strong>, got %s", htmlOutput)
	}
}

func TestWeaveAPI(t *testing.T) {
	// A simple public API for testing
	// We'll hit the GitHub API for the go-git repo
	url := "https://api.github.com/repos/go-git/go-git"
	
	data, err := weaveAPI(url)
	if err != nil {
		t.Fatalf("weaveAPI failed: %v", err)
	}

	// data should be a map[string]interface{}
	m, ok := data.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected data to be map[string]interface{}, got %T", data)
	}

	// Check if a known field exists in the GitHub API response
	if m["name"] != "go-git" {
		t.Errorf("Expected repo name 'go-git', got %v", m["name"])
	}
}

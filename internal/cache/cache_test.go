package cache

import (
	"testing"
	"time"
)

func TestCache(t *testing.T) {
	// Use an in-memory database for testing
	c, err := New("file::memory:?cache=shared")
	if err != nil {
		t.Fatalf("Failed to initialize cache: %v", err)
	}
	defer c.Close()

	domain := "gotcode.org"
	path := "/about"
	expectedHTML := "<h1>About Us</h1>"

	// 1. Test Cache Miss
	html, err := c.Get(domain, path)
	if err != nil {
		t.Fatalf("Unexpected error on miss: %v", err)
	}
	if html != "" {
		t.Errorf("Expected empty string on miss, got %s", html)
	}

	// 2. Test Set & Get
	err = c.Set(domain, path, expectedHTML, 1*time.Minute)
	if err != nil {
		t.Fatalf("Failed to set cache: %v", err)
	}

	html, err = c.Get(domain, path)
	if err != nil {
		t.Fatalf("Unexpected error on get: %v", err)
	}
	if html != expectedHTML {
		t.Errorf("Expected %s, got %s", expectedHTML, html)
	}

	// 3. Test Expiration
	// Set with a negative TTL so it expires instantly
	err = c.Set(domain, "/expired", "expired", -1*time.Second)
	if err != nil {
		t.Fatalf("Failed to set expired cache: %v", err)
	}

	html, err = c.Get(domain, "/expired")
	if err != nil {
		t.Fatalf("Unexpected error on get expired: %v", err)
	}
	if html != "" {
		t.Errorf("Expected empty string for expired item, got %s", html)
	}

	// 4. Test Invalidation
	err = c.Invalidate(domain)
	if err != nil {
		t.Fatalf("Failed to invalidate domain: %v", err)
	}

	html, err = c.Get(domain, path)
	if err != nil {
		t.Fatalf("Unexpected error on get after invalidate: %v", err)
	}
	if html != "" {
		t.Errorf("Expected empty string after invalidation, got %s", html)
	}
}

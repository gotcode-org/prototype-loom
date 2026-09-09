package cache

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // Pure Go SQLite driver (No CGO required, which is great for cross-compiling)
)

// ============================================================================
// SQLITE EDGE CACHE
// ============================================================================
// Evaluating Go templates and parsing Markdown into HTML takes CPU cycles. 
// If an article goes viral and hits the front page of Hacker News, we don't
// want the server parsing the exact same Markdown file 10,000 times a second.
//
// Instead, the very first time someone visits a page, Loom compiles it, 
// and then saves the final output string into a local SQLite database.
// Everyone else who visits that page gets served the pre-compiled HTML instantly.
// ============================================================================

// Cache represents the SQLite-backed database connection.
type Cache struct {
	db *sql.DB
}

// New creates the database file (e.g. loom.db) on the physical hard drive
// and sets up the SQL tables if they don't already exist.
func New(dsn string) (*Cache, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite db: %w", err)
	}

	// We create a simple table with a composite Primary Key (Domain + Path).
	// This ensures that `gotcode.org/about` and `gotunix.net/about` don't overwrite each other.
	query := `
	CREATE TABLE IF NOT EXISTS page_cache (
		domain TEXT,
		path TEXT,
		html TEXT,
		expires_at DATETIME,
		PRIMARY KEY (domain, path)
	);`
	if _, err := db.Exec(query); err != nil {
		return nil, fmt.Errorf("failed to create cache table: %w", err)
	}

	return &Cache{db: db}, nil
}

// Get tries to pull a fully rendered HTML page from the SQLite database.
func (c *Cache) Get(domain, path string) (string, error) {
	var html string
	var expiresAt time.Time

	// Query SQLite for the page
	err := c.db.QueryRow("SELECT html, expires_at FROM page_cache WHERE domain = ? AND path = ?", domain, path).Scan(&html, &expiresAt)
	if err != nil {
		// If the page isn't in the database, it's a "Cache Miss".
		// We return an empty string, which tells the Router it needs to build the page from scratch.
		if err == sql.ErrNoRows {
			return "", nil 
		}
		return "", err
	}

	// If the page is in the database, but it has exceeded its Time-To-Live (TTL),
	// we pretend it's a Cache Miss so the Router builds a fresh copy.
	if time.Now().After(expiresAt) {
		return "", nil 
	}

	// Cache Hit! Return the compiled HTML.
	return html, nil
}

// Set saves a freshly rendered HTML page into the SQLite database.
func (c *Cache) Set(domain, path, html string, ttl time.Duration) error {
	// Calculate exactly what time this page should expire.
	expiresAt := time.Now().Add(ttl)
	
	// UPSERT: Insert the new page. If the page already exists in the database,
	// overwrite the old HTML and reset the expiration timer.
	query := `
		INSERT INTO page_cache (domain, path, html, expires_at) 
		VALUES (?, ?, ?, ?) 
		ON CONFLICT(domain, path) 
		DO UPDATE SET html=excluded.html, expires_at=excluded.expires_at`
		
	_, err := c.db.Exec(query, domain, path, html, expiresAt)
	return err
}

// Invalidate acts as the "Purge" button. 
// When the Router detects that a Git repository has a new commit (i.e. you updated the website),
// it calls this function to instantly delete every single cached page for that domain.
// This guarantees that the very next visitor instantly sees your new updates.
func (c *Cache) Invalidate(domain string) error {
	_, err := c.db.Exec("DELETE FROM page_cache WHERE domain = ?", domain)
	return err
}

// Close gracefully closes the database connection on server shutdown.
func (c *Cache) Close() error {
	return c.db.Close()
}

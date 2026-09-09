package server

import (
	"context"
	"fmt"
	"html/template"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gotcode.org/loom/internal/cache"
	"gotcode.org/loom/internal/engine"
	"gotcode.org/loom/internal/vfs"
)

// ============================================================================
// EDGE ROUTER & MIDDLEWARE
// ============================================================================
// The Router is the central nervous system of Loom.
// Every single HTTP request that hits the server comes through here.
// It is responsible for figuring out what domain the user is visiting, checking
// the SQLite cache for a pre-rendered page, or spinning up the Content Engine
// to build the page from scratch using the Virtual File System.
// ============================================================================

// Router holds all the global state for the running server.
type Router struct {
	Config *Config       // The parsed server.yaml file
	Cache  *cache.Cache  // The SQLite database used for saving rendered HTML
	Logger *slog.Logger  // Structured JSON logger for telemetry

	// Because Loom doesn't clone a repository on EVERY request (that would be slow),
	// we keep a map of active repositories loaded in memory (Virtual File Systems).
	mu     sync.RWMutex
	vfsMap map[string]*vfs.GitVFS
}

// NewRouter acts as a constructor, spinning up the router and triggering the background sync.
func NewRouter(cfg *Config, c *cache.Cache, logger *slog.Logger) *Router {
	rt := &Router{
		Config: cfg,
		Cache:  c,
		Logger: logger,
		vfsMap: make(map[string]*vfs.GitVFS),
	}
	
	// Start the background polling routine to automatically detect new Git commits.
	go rt.syncRepositories()
	
	return rt
}

// syncRepositories runs forever in the background. It checks the remote Git servers
// every 30 seconds. If someone pushes a new commit to one of the hosted websites,
// it instantly clones the new files into RAM and wipes the SQLite cache so the web
// server instantly updates without needing a reboot.
func (rt *Router) syncRepositories() {
	ticker := time.NewTicker(30 * time.Second)
	for range ticker.C {
		for siteName, siteConfig := range rt.Config.Sites {
			// Vanity imports or sites that just redirect don't have repositories.
			if siteConfig.Repo == "" || siteConfig.Redirect != "" {
				continue
			}

			rt.mu.RLock()
			currentVFS, exists := rt.vfsMap[siteConfig.Repo]
			rt.mu.RUnlock()
			
			// If nobody has visited the site yet, it hasn't been cloned, so we skip it.
			if !exists {
				continue
			}
			
			// Figure out which SSH key to use for authentication (if any).
			var authPath string
			if siteConfig.Auth != nil {
				authPath = siteConfig.Auth.Path
			}
			
			// Reach out to GitHub/GotCode without actually downloading anything to check the latest commit hash.
			remoteHash, err := vfs.GetRemoteHash(context.Background(), siteConfig.Repo, authPath, rt.Config.Security.KnownHostsPath)
			if err != nil {
				rt.Logger.Warn("Sync check failed", slog.String("site", siteName), slog.String("error", err.Error()))
				continue
			}
			
			// If the remote hash doesn't match our local RAM hash, someone updated the website!
			if currentVFS.CurrentHash() != remoteHash {
				rt.Logger.Info("New commit detected. Syncing repository...", slog.String("repo", siteConfig.Repo), slog.String("new_hash", remoteHash))
				
				// Perform a fresh in-memory clone of the new website files.
				newVFS, err := vfs.CloneInMemory(context.Background(), siteConfig.Repo, authPath, rt.Config.Security.KnownHostsPath)
				if err != nil {
					rt.Logger.Error("Failed to clone updated repository", slog.String("repo", siteConfig.Repo), slog.String("error", err.Error()))
					continue
				}
				
				// Safely swap out the old website files for the new ones.
				rt.mu.Lock()
				rt.vfsMap[siteConfig.Repo] = newVFS
				rt.mu.Unlock()
				
				// ---------------------------------------------------------
				// CACHE INVALIDATION (Critical Step)
				// ---------------------------------------------------------
				// Since we just loaded new files, we have to delete all the old HTML
				// from the SQLite database. Otherwise, users will keep seeing the old site!
				for host, cfg := range rt.Config.Sites {
					if cfg.Repo == siteConfig.Repo {
						rt.Cache.Invalidate(host)
						rt.Logger.Info("Repository updated successfully. Cache invalidated.", slog.String("site", host))
					}
				}
			}
		}
	}
}

// getVFS is a helper function that fetches the requested website's files from RAM.
// If the website hasn't been cloned yet (e.g., this is the very first request since reboot),
// it clones it on the fly.
func (rt *Router) getVFS(ctx context.Context, repoURL string, authKeyPath string) (*vfs.GitVFS, error) {
	// Fast path: Just read the map.
	rt.mu.RLock()
	v, exists := rt.vfsMap[repoURL]
	rt.mu.RUnlock()
	
	if exists {
		return v, nil
	}
	
	// Slow path: We need to clone it. We lock the entire map to prevent two simultaneous
	// visitors from triggering two clones of the same repository at the same time.
	rt.mu.Lock()
	defer rt.mu.Unlock()
	
	// Double-check (in case someone else cloned it while we were waiting for the lock)
	if v, exists := rt.vfsMap[repoURL]; exists {
		return v, nil
	}
	
	v, err := vfs.CloneInMemory(ctx, repoURL, authKeyPath, rt.Config.Security.KnownHostsPath)
	if err != nil {
		return nil, err
	}
	
	rt.vfsMap[repoURL] = v
	return v, nil
}

// responseWriter is a custom wrapper around the standard Go http.ResponseWriter.
// We use it so we can intercept and record the final HTTP Status Code (e.g., 200, 404)
// to print it in our telemetry logs later.
type responseWriter struct {
	http.ResponseWriter
	status int
}

// WriteHeader catches the status code before it goes back to the browser.
func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// ServeHTTP is the absolute entrypoint for ALL incoming web traffic.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
	var matchedSite string

	// ---------------------------------------------------------
	// PROXY RESOLVER (Extracting the Real IP)
	// ---------------------------------------------------------
	// If Loom is running behind a proxy like Cloudflare, `r.RemoteAddr` will just
	// be Cloudflare's IP address. We need to check the HTTP Headers to find the real user.
	clientIP := r.Header.Get("X-Forwarded-For")
	if clientIP == "" {
		clientIP = r.Header.Get("X-Real-Ip")
	}
	if clientIP == "" {
		clientIP = r.RemoteAddr
	}

	// ---------------------------------------------------------
	// TELEMETRY LOGGER
	// ---------------------------------------------------------
	// We use `defer` so this block of code is guaranteed to run at the absolute end
	// of the request, allowing us to record exactly how many milliseconds the whole process took.
	defer func() {
		rt.Logger.Info("HTTP Request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rw.status),
			slog.String("site", matchedSite),
			slog.String("duration", time.Since(start).String()),
			slog.String("ip", clientIP),
		)
		
		// If metrics are enabled, record the request for Prometheus
		if rt.Config.Metrics != nil && rt.Config.Metrics.Enabled {
			metricsPath := rt.Config.Metrics.Path
			if metricsPath == "" {
				metricsPath = "/metrics"
			}
			
			// We intentionally do not record Prometheus's own scraping requests,
			// otherwise we would artificially inflate our traffic stats by thousands of hits a day.
			if r.URL.Path != metricsPath {
				// If we haven't matched a site yet (e.g. rate limited before parsing), categorize as 'unknown'
				safeSite := matchedSite
				if safeSite == "" {
					safeSite = "unknown"
				}
				RecordRequest(safeSite, rw.status)
			}
		}
	}()

	// ---------------------------------------------------------
	// PROMETHEUS METRICS ENDPOINT
	// ---------------------------------------------------------
	if rt.Config.Metrics != nil && rt.Config.Metrics.Enabled {
		// Use the configured path, or default to "/metrics"
		metricsPath := rt.Config.Metrics.Path
		if metricsPath == "" {
			metricsPath = "/metrics"
		}
		
		if r.URL.Path == metricsPath {
			matchedSite = "system" // Categorize this in access logs

			// Enforce IP Access Control (Default Deny)
			ipAllowed := false
			for _, allowed := range rt.Config.Metrics.AllowedIPs {
				if clientIP == allowed {
					ipAllowed = true
					break
				}
			}
			
			// Always allow localhost for local testing
			if clientIP == "127.0.0.1" || clientIP == "::1" {
				ipAllowed = true
			}

			if !ipAllowed {
				rw.status = http.StatusForbidden
				rt.Logger.Warn("Unauthorized access attempt to /metrics", slog.String("ip", clientIP))
				http.Error(w, "403 Forbidden", http.StatusForbidden)
				return
			}

			w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
			w.Write([]byte(GeneratePrometheusPayload()))
			return
		}
	}

	// ---------------------------------------------------------
	// SECURITY MIDDLEWARE (Firewall & Throttle)
	// ---------------------------------------------------------
	// Check if this IP is spamming us or looking for vulnerability exploits.
	if !IsAllowed(r, clientIP, rt.Config.Security) {
		rw.status = http.StatusTooManyRequests
		rt.Logger.Warn("Connection throttled or rejected by security middleware", slog.String("ip", clientIP), slog.String("path", r.URL.Path))
		http.Error(w, "429 Too Many Requests / Forbidden", http.StatusTooManyRequests)
		return
	}

	// Pass the safe request down into the core router engine.
	rt.serveInternal(rw, r, &matchedSite)
}

// serveInternal handles the heavy lifting of figuring out what domain they want,
// checking the cache, and building the page.
func (rt *Router) serveInternal(w *responseWriter, r *http.Request, matchedSite *string) {
	// ---------------------------------------------------------
	// 1. VANITY GO IMPORTS (Routing `go get`)
	// ---------------------------------------------------------
	// If a user runs `go get gotcode.org/loom`, Go sends an HTTP request asking where the code is.
	// We intercept that request and tell Go to look at the actual GitHub repository URL.
	if repoURL, ok := rt.Config.VanityImports[r.URL.Path]; ok {
		*matchedSite = "vanity"
		if r.URL.Query().Get("go-get") == "1" {
			html := fmt.Sprintf(`<!DOCTYPE html><html><head><meta name="go-import" content="%s%s %s"></head><body></body></html>`, r.Host, r.URL.Path, repoURL)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(html))
			return
		}
		
		// If a human visits the module URL in their browser, we just 302 Redirect them to GitHub.
		parts := strings.Split(repoURL, " ")
		targetURL := parts[len(parts)-1]
		http.Redirect(w, r, targetURL, http.StatusFound)
		return
	}

	// 2. Strip port from r.Host if present (e.g. "localhost:8080" -> "localhost")
	host := r.Host
	if strings.Contains(host, ":") {
		host = strings.Split(host, ":")[0]
	}

	// ---------------------------------------------------------
	// 3. DOMAIN MATCHING
	// ---------------------------------------------------------
	// Check if we are configured to host the domain they typed in.
	*matchedSite = host
	site, exists := rt.Config.Sites[host]
	if !exists {
		// Fallback to a site explicitly named "default" in server.yaml.
		*matchedSite = "default"
		site, exists = rt.Config.Sites["default"]
		if !exists {
			rt.Logger.Error("HTTP Error", slog.String("error", "404 Site Not Found"), slog.Int("status", 404), slog.String("path", r.URL.Path))
			http.Error(w, "404 Site Not Found", http.StatusNotFound)
			return
		}
	}

	// ---------------------------------------------------------
	// 4. DOMAIN REDIRECTS
	// ---------------------------------------------------------
	// If the server.yaml tells us to redirect this domain entirely (e.g., gotunix.com -> gotunix.net).
	if site.Redirect != "" {
		targetURL := site.Redirect + r.URL.Path
		if r.URL.RawQuery != "" {
			targetURL += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, targetURL, http.StatusFound)
		return
	}

	// ---------------------------------------------------------
	// 5. CHECK SQLITE CACHE
	// ---------------------------------------------------------
	// Before we do any heavy lifting, check if we've already compiled this page recently!
	cachedHTML, err := rt.Cache.Get(host, r.URL.Path)
	if err == nil && cachedHTML != "" {
		contentType := "text/html; charset=utf-8"
		if ext := filepath.Ext(r.URL.Path); ext != "" {
			if ct := mime.TypeByExtension(ext); ct != "" {
				contentType = ct
			}
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("X-Loom-Cache", "HIT") // Let the browser know it came from the cache
		w.Write([]byte(cachedHTML))
		return
	}

	// ---------------------------------------------------------
	// 6. CLONE / LOAD REPOSITORY
	// ---------------------------------------------------------
	var authKeyPath string
	if site.Auth != nil && site.Auth.Type == "ssh" {
		authKeyPath = site.Auth.Path
	}

	v, err := rt.getVFS(r.Context(), site.Repo, authKeyPath)
	if err != nil {
		rt.Logger.Error("HTTP Error", slog.String("error", "Failed to load repository"), slog.String("details", err.Error()), slog.Int("status", 500), slog.String("path", r.URL.Path))
		http.Error(w, "Failed to load repository", http.StatusInternalServerError)
		return
	}

	// ---------------------------------------------------------
	// 7. FILE ROUTING & STATIC ASSETS
	// ---------------------------------------------------------
	requestPath := strings.TrimSuffix(r.URL.Path, "/")
	if requestPath == "" {
		requestPath = "/index"
	}
	
	// First, check if they are asking for a static file (like an image or CSS).
	assetPath := filepath.Join(requestPath)
	if assetPath[0] == '/' {
		assetPath = assetPath[1:]
	}
	assetData, err := v.ReadFile(assetPath)
	if err == nil {
		// If it's a static file, figure out the MIME type (e.g., image/png) and serve the raw bytes.
		ext := filepath.Ext(assetPath)
		mimeType := mime.TypeByExtension(ext)
		if mimeType != "" {
			w.Header().Set("Content-Type", mimeType)
		}
		w.Write(assetData)
		return
	}

	// If it's not a static file, we assume they want a Markdown webpage.
	mdPath := filepath.Join("content", requestPath+".md")
	mdData, err := v.ReadFile(mdPath)
	if err != nil {
		rt.Logger.Error("HTTP Error", slog.String("error", "404 Page Not Found"), slog.Int("status", 404), slog.String("path", r.URL.Path))
		rt.serveError(w, r, v, http.StatusNotFound, "404 Page Not Found")
		return
	}

	// ---------------------------------------------------------
	// 8. CONTENT ENGINE PARSING
	// ---------------------------------------------------------
	// Hand the raw Markdown bytes over to the engine to evaluate templates and convert to HTML.
	doc, err := engine.ParseMarkdown(mdData, rt.Config.GitAuth, v, rt.Config.Engine.APITimeoutSeconds, rt.Config.Engine.APIMaxPayloadMB, rt.Config.Security.KnownHostsPath)
	if err != nil {
		rt.Logger.Error("HTTP Error", slog.String("error", err.Error()), slog.Int("status", 500), slog.String("path", r.URL.Path))
		rt.serveError(w, r, v, http.StatusInternalServerError, "Failed to parse markdown")
		return
	}

	// ---------------------------------------------------------
	// 9. WRAP IN BASE LAYOUT (Master Template)
	// ---------------------------------------------------------
	var finalHTML string
	
	// [SEO FEATURE] DYNAMIC MIME TYPE DETECTION
	// If the user requests `sitemap.xml`, the default `text/html` header will cause web browsers
	// to try and render it as a webpage (which looks terrible) rather than an XML data tree.
	// We extract the file extension from the URL, run it through the system's MIME registry,
	// and dynamically override the Content-Type header so the browser knows exactly what it's receiving!
	contentType := "text/html; charset=utf-8"
	if ext := filepath.Ext(r.URL.Path); ext != "" {
		if ct := mime.TypeByExtension(ext); ct != "" {
			contentType = ct
		}
	}
	
	// [SEO FEATURE] RAW TEMPLATE BYPASSING (`layout: none`)
	// Normally, we take the evaluated markdown and inject it into the `layouts/base.html` master layout.
	// But if the user is generating an XML sitemap or JSON API endpoint, wrapping it in `<html>` tags will 
	// completely destroy the data structure.
	// 
	// We explicitly check the Markdown Frontmatter for `layout: none`.
	// If detected, we perform a massive short-circuit: we grab the pure, unadulterated payload and use 
	// a `goto` statement to jump completely past the HTML compilation steps directly down to the Caching layer!
	if layoutVal, ok := doc.Frontmatter["layout"]; ok {
		if layoutStr, isStr := layoutVal.(string); isStr && layoutStr == "none" {
			finalHTML = string(doc.BodyHTML)
			goto CacheAndServe // Zoom right past the HTML rendering block
		}
	}

	{
		// We read the global `layouts/base.html` file.
		layoutData, err := v.ReadFile("layouts/base.html")
		if err != nil {
			// If they didn't build a base layout, just spit out the raw HTML.
			finalHTML = string(doc.BodyHTML)
			goto CacheAndServe
		}

		// Compile the base layout, making sure to pass the template functions in.
		tmpl, err := template.New("layout").Funcs(engine.TemplateFuncMap(rt.Config.GitAuth, v, rt.Config.Engine.APITimeoutSeconds, rt.Config.Engine.APIMaxPayloadMB, rt.Config.Security.KnownHostsPath)).Parse(string(layoutData))
		if err != nil {
			rt.Logger.Error("HTTP Error", slog.String("error", "Failed to parse layout template"), slog.Int("status", 500), slog.String("path", r.URL.Path))
			rt.serveError(w, r, v, http.StatusInternalServerError, "Failed to parse layout template")
			return
		}

		// Inject the Markdown HTML (and Frontmatter) into the layout.
		var buf strings.Builder
		if err := tmpl.Execute(&buf, doc); err != nil {
			rt.Logger.Error("HTTP Error", slog.String("error", "Failed to execute template"), slog.Int("status", 500), slog.String("path", r.URL.Path))
			rt.serveError(w, r, v, http.StatusInternalServerError, "Failed to execute template")
			return
		}
		
		finalHTML = buf.String()
	}

CacheAndServe:
	// ---------------------------------------------------------
	// 10. SAVE TO CACHE & SERVE
	// ---------------------------------------------------------
	// Save the final, fully-built HTML string into the SQLite database for 15 minutes.
	// Next time someone visits, we skip steps 6 through 9!
	_ = rt.Cache.Set(host, r.URL.Path, finalHTML, 15*time.Minute)

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Loom-Cache", "MISS")
	w.Write([]byte(finalHTML))
}

// ============================================================================
// CUSTOM ERROR PAGE ROUTER
// ============================================================================
// serveError attempts to serve a beautifully formatted custom Markdown error 
// page (e.g. content/404.md) rather than a boring plain-text error.
// If the domain owner didn't create one, it gracefully falls back to plain-text.
func (rt *Router) serveError(w *responseWriter, r *http.Request, v *vfs.GitVFS, statusCode int, defaultMsg string) {
	// First, explicitly record the HTTP status code (e.g., 404, 500) so the telemetry 
	// logger picks it up correctly at the end of the request.
	w.status = statusCode

	// 1. SAFETY CHECK: Do we even have a Virtual File System loaded?
	// If a user gets rate-limited by the firewall BEFORE the router figures out
	// which domain they wanted, we can't show a custom domain error. Fall back immediately.
	if v == nil {
		http.Error(w, defaultMsg, statusCode)
		return
	}

	// 2. CHECK FOR CUSTOM ERROR FILE
	// We dynamically construct the filename based on the integer status code.
	// For example, if it's a 404 Not Found, we look for "content/404.md".
	mdPath := filepath.Join("content", fmt.Sprintf("%d.md", statusCode))
	mdData, err := v.ReadFile(mdPath)
	if err != nil {
		// If the file simply doesn't exist in the Git repository, just serve 
		// the boring plain-text error and call it a day.
		http.Error(w, defaultMsg, statusCode)
		return
	}

	// 3. COMPILE THE CUSTOM MARKDOWN
	// We found an error page! Now we push it through the exact same engine that 
	// processes normal web pages so they can use full Go templating inside it.
	doc, err := engine.ParseMarkdown(mdData, rt.Config.GitAuth, v, rt.Config.Engine.APITimeoutSeconds, rt.Config.Engine.APIMaxPayloadMB, rt.Config.Security.KnownHostsPath)
	if err != nil {
		// If they made a syntax error inside their error page, we fallback again 
		// to prevent an infinite loop of crashing error pages.
		http.Error(w, defaultMsg, statusCode)
		return
	}

	// 4. WRAP IT IN THE GLOBAL LAYOUT
	// We wrap the custom error page inside the site's master navigation and footer.
	layoutData, err := v.ReadFile("layouts/base.html")
	if err != nil {
		// If they don't have a layout, just serve the raw error HTML.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(doc.BodyHTML))
		return
	}

	tmpl, err := template.New("layout").Funcs(engine.TemplateFuncMap(rt.Config.GitAuth, v, rt.Config.Engine.APITimeoutSeconds, rt.Config.Engine.APIMaxPayloadMB, rt.Config.Security.KnownHostsPath)).Parse(string(layoutData))
	if err != nil {
		http.Error(w, defaultMsg, statusCode)
		return
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, doc); err != nil {
		http.Error(w, defaultMsg, statusCode)
		return
	}

	// 5. SUCCESSFUL SERVE
	// We successfully compiled the custom error page with the layout!
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(buf.String()))
}

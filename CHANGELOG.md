# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Engineered raw template bypassing (via `layout: none` frontmatter flag) to allow the dynamic generation of pure XML Sitemaps, RSS feeds, and JSON endpoints directly from markdown repositories.
- Implemented dynamic MIME type inference in the router to automatically set correct HTTP `Content-Type` headers based on requested URL extensions (e.g., `.xml`, `.json`).
- Refactored the Markdown Engine evaluation pass to use `text/template` instead of `html/template` to prevent strict HTML XSS safeguards from escaping XML processing instructions like `<?xml`.
- Hardened the Markdown parsing pipeline to aggressively trim leading whitespace artifacts left behind by YAML stripping to ensure strict compatibility with web browser XML parsers.
- Initial project scaffolding.
- Implemented full Git VFS and SQLite edge-caching router.
- Added Vanity URL Go module proxy support with human browser redirection.
- Built native SSH Authentication (with ssh-agent fallback) for cloning private repositories.
- Added multi-domain routing via `aliases` in SiteConfig.
- Enabled `html.WithUnsafe()` in Goldmark engine to support raw HTML injection.
- Replaced standard logs with structured JSON telemetry (`log/slog`) for all HTTP access and internal errors.
- Added native HTTP 302 domain redirects via `redirect` field in SiteConfig.
- AGPL-3.0 License.
- README and Architecture documentation.
- Built global GitAuth maps in `server.yaml` config for authenticating against multiple remote hosts seamlessly.
- Upgraded Content Engine to execute Markdown bytes as a Go `html/template` before parsing via Goldmark.
- Engineered the `weave_git` template function to dynamically clone remote repositories and inject their markdown directly into pages in-memory.
- Expanded `slog` telemetry to automatically resolve actual client IPs by checking `X-Forwarded-For` and `X-Real-Ip` headers when running behind proxies like Cloudflare.
- Implemented native Security Middleware to intercept and instantly drop malicious script-kiddie vulnerability scans (`.php`, `wp-admin`, `.env`).
- Engineered a lightweight sliding-window rate limiter in Go to automatically throttle IP addresses spamming the router (150 requests / min limit).
- Expanded the Security Middleware configuration to allow custom `max_requests_per_minute` thresholds directly inside `server.yaml`.
- Added explicit `slog.Warn` telemetry events whenever an IP address is blocked or throttled by the Security Middleware.
- Built native `list_pages` and `recent_pages` template functions to dynamically iterate, filter, and sort (chronologically) markdown files inside a directory.
- Built native, zero-dependency Prometheus `/metrics` exporter.
- Added `metrics` configuration block to `server.yaml`.
- Implemented IP Access Control List (ACL) with a Default-Deny policy for the `/metrics` endpoint (localhost natively allowed).
- Added logic to explicitly exclude Prometheus scraping requests from polluting the HTTP telemetry metrics.
- Built support for dynamic, domain-specific custom Markdown error pages (e.g., `404.md`, `500.md`) that inherit the domain's base layout.
- Added SSH Man-in-the-Middle (MITM) protection by strictly enforcing `known_hosts` verification on all Git clones.
- Added API OOM protection by wrapping `weave_api` payloads in an `io.LimitReader` (configurable via `api_max_payload_mb`).
- Added Thread Exhaustion protection by enforcing strict timeouts on all `weave_api` HTTP requests (configurable via `api_timeout_seconds`).
- Upgraded the Security Middleware with dynamic CIDR and IP Blocklists (e.g., banning OVHCloud/DigitalOcean subnets).
- Upgraded the Security Middleware with dynamic User-Agent bans (e.g., blocking `masscan`, `zgrab`, `nuclei`).
- Upgraded the Security Middleware with Strict Method Enforcement, dropping all HTTP methods except `GET` and `HEAD` to prevent POST/PUT payload attacks.
- Engineered automated RAM load-shedding into the sliding-window rate limiter, instantly wiping the IP tracker map if a botnet exceeds the `max_tracked_ips` threshold.

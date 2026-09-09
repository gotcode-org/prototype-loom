package server

import (
	"fmt"
	"strings"
	"sync"
)

// ============================================================================
// PROMETHEUS METRICS TRACKER
// ============================================================================
// Instead of importing the massive official Prometheus Go SDK, we use a very
// lightweight native Go struct. Prometheus exposition format is just plain text,
// so we can generate it manually in microseconds.
// ============================================================================

type metricsTracker struct {
	mu sync.Mutex
	
	// Map of Site -> Map of HTTP Status Code -> Count
	// e.g., requests["gotcode.org"][200] = 1542
	requests map[string]map[int]int
}

// Global singleton instance for tracking metrics.
var metrics = &metricsTracker{
	requests: make(map[string]map[int]int),
}

// RecordRequest increments the counter for a specific site and status code.
func RecordRequest(site string, status int) {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()

	// Initialize the inner map if this is the very first request for this site
	if _, exists := metrics.requests[site]; !exists {
		metrics.requests[site] = make(map[int]int)
	}

	metrics.requests[site][status]++
}

// GeneratePrometheusPayload locks the map and converts all tracked statistics
// into the standard Prometheus plain-text format.
func GeneratePrometheusPayload() string {
	metrics.mu.Lock()
	defer metrics.mu.Unlock()

	var sb strings.Builder
	
	// Standard Prometheus headers
	sb.WriteString("# HELP loom_http_requests_total Total number of HTTP requests processed by Loom.\n")
	sb.WriteString("# TYPE loom_http_requests_total counter\n")

	for site, statuses := range metrics.requests {
		for status, count := range statuses {
			// e.g., loom_http_requests_total{site="gotcode.org", status="200"} 1542
			sb.WriteString(fmt.Sprintf("loom_http_requests_total{site=\"%s\", status=\"%d\"} %d\n", site, status, count))
		}
	}

	return sb.String()
}

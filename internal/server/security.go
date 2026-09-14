package server

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ============================================================================
// NATIVE SECURITY FIREWALL & RATE LIMITER
// ============================================================================
// Because Loom is designed to sit directly on the edge of the internet without
// needing a WAF (Web Application Firewall) or reverse proxy like Nginx, it needs
// its own native defenses against script kiddies, DDoS attempts, and bots.
// ============================================================================

// rateLimiter acts as a "Token Bucket" / "Sliding Window" tracker for IP addresses.
type rateLimiter struct {
	// A Mutex (Mutual Exclusion lock) ensures that if 1,000 requests hit the server
	// at the exact same millisecond, the Go routines don't crash by trying to write
	// to the map at the same time.
	mu      sync.Mutex
	
	// A map where the Key is the user's IP Address (e.g., "192.168.1.5"),
	// and the Value is a pointer to their current statistics.
	clients map[string]*clientStat
}

// clientStat tracks how many times a specific IP has hit the server, and when we last saw them.
type clientStat struct {
	requests int       // Number of requests made in the current window
	lastSeen time.Time // Timestamp of their most recent request
}

// limiter is the global singleton instance that holds all IP records in RAM.
var limiter = &rateLimiter{
	clients: make(map[string]*clientStat),
}

// init() automatically runs when the server boots.
func init() {
	// We spawn a background Go routine (a lightweight thread) that acts as the garbage collector.
	go func() {
		for {
			// Every 5 minutes, we pause and scan the entire map in memory.
			time.Sleep(time.Minute * 5)
			
			limiter.mu.Lock()
			now := time.Now()
			
			// If we haven't seen an IP address in over 3 minutes, they are no longer
			// a threat, so we delete them from RAM. This prevents the server from 
			// running out of memory (Memory Leak) if a botnet hits it with millions
			// of unique spoofed IP addresses.
			for ip, stat := range limiter.clients {
				if now.Sub(stat.lastSeen) > time.Minute*3 {
					delete(limiter.clients, ip)
				}
			}
			limiter.mu.Unlock()
		}
	}()
}

// IsAllowed is called on EVERY single HTTP request before the router processes it.
// It returns 'true' if the request is safe, or 'false' if the connection should be dropped.
func IsAllowed(r *http.Request, ip string, secCfg *SecurityConfig) bool {
	// ---------------------------------------------------------
	// 1. DYNAMIC FIREWALL RULES (YAML CONFIGURABLE)
	// ---------------------------------------------------------
	
	// A. Strict Method Enforcement
	// Because Loom only serves static Markdown pages, there is zero reason for anyone 
	// to send a POST, PUT, or DELETE request. If we don't drop those instantly, hackers
	// could try to upload massive 10GB files in the request body to crash the server.
	if secCfg == nil {
		return true
	}
	methodAllowed := false
	for _, m := range secCfg.AllowedMethods {
		if r.Method == m {
			methodAllowed = true
			break
		}
	}
	if !methodAllowed {
		return false // Instantly drop the connection
	}

	// B. User-Agent Blocklist
	// Vulnerability scanners like "masscan" or "zgrab" announce their name in the 
	// HTTP headers. If we detect them, we drop them before they can scan our server.
	ua := strings.ToLower(r.UserAgent())
	for _, blockedUA := range secCfg.BlockedUserAgents {
		if blockedUA != "" && strings.Contains(ua, strings.ToLower(blockedUA)) {
			return false // Instantly drop the connection
		}
	}

	// C. IP / CIDR Blocklist
	// If a massive botnet is attacking us from OVHCloud, we can just ban their entire 
	// network block (e.g., "10.0.0.0/8"). We parse the IP and check if it falls inside 
	// any of the banned ranges configured in server.yaml.
	clientIP := net.ParseIP(ip)
	for _, blocked := range secCfg.BlockedIPs {
		// Fast check: Did they ban an exact IP address?
		if ip == blocked {
			return false
		}
		// Slower check: Did they ban an entire subnet? 
		// ParseCIDR figures out the network boundaries and Contains() checks if the IP is inside it.
		_, cidrNet, err := net.ParseCIDR(blocked)
		if err == nil && clientIP != nil && cidrNet.Contains(clientIP) {
			return false // Instantly drop the connection
		}
	}

	// ---------------------------------------------------------
	// 2. HARDCODED VULNERABILITY SCANNER BLOCK
	// ---------------------------------------------------------
	// Script kiddies constantly scan the internet looking for WordPress or PHP exploits.
	// Since Loom doesn't run PHP, we instantly drop these requests to save CPU cycles.
	pathLower := strings.ToLower(r.URL.Path)
	if strings.HasSuffix(pathLower, ".php") || strings.Contains(pathLower, "wp-admin") || strings.Contains(pathLower, ".env") || strings.Contains(pathLower, "wp-login") {
		return false
	}

	// ---------------------------------------------------------
	// 3. SLIDING WINDOW RATE LIMITER
	// ---------------------------------------------------------
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	
	// SECURITY: Botnet memory exhaustion failsafe (Load Shedding).
	// If the IP tracker gets too large, wipe it to protect RAM.
	if len(limiter.clients) > secCfg.MaxTrackedIPs {
		limiter.clients = make(map[string]*clientStat)
	}

	// Check if this IP is already in our tracker
	stat, exists := limiter.clients[ip]
	now := time.Now()
	
	// If this is the first time we've seen them, add them to the map and let them through.
	if !exists {
		limiter.clients[ip] = &clientStat{requests: 1, lastSeen: now}
		return true
	}

	// If they are in the tracker, but their last request was more than 1 minute ago,
	// their "penalty window" has expired. We reset their request count back to 1.
	if now.Sub(stat.lastSeen) > time.Minute {
		stat.requests = 1
		stat.lastSeen = now
		return true
	}

	// Otherwise, they are hitting us rapidly. Increment their request count.
	stat.requests++
	stat.lastSeen = now

	// If their total requests in the last 60 seconds exceeds the allowed limit
	// (configurable via server.yaml), we return false to trigger a 429 Too Many Requests error.
	if stat.requests > secCfg.MaxRequestsPerMinute {
		return false
	}

	// IP is behaving normally. Let them through.
	return true
}

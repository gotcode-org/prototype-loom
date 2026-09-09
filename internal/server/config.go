package server

import (
	"os"

	yaml "gopkg.in/yaml.v3"
)

// ============================================================================
// CONFIGURATION MANAGER
// ============================================================================
// This file is responsible for parsing the `server.yaml` file into Go structs.
// Loom uses a single global YAML file to determine which domains it should listen
// for, which Git repositories map to which domains, and security settings.
// ============================================================================

// AuthConfig defines the authentication method for accessing private Git repositories.
// If a repository is private, Loom needs an SSH key to clone it into RAM.
type AuthConfig struct {
	// The type of authentication. Usually "ssh" (could be expanded to "aegis" later).
	Type string `yaml:"type"` 
	
	// The absolute file path on the host machine to the SSH private key (e.g., "/root/.ssh/id_rsa").
	Path string `yaml:"path"` 
}

// SiteConfig represents the configuration for a single website hosted by Loom.
type SiteConfig struct {
	// The remote Git repository URL to clone (e.g., "https://github.com/gotcode-org/website.git").
	Repo     string      `yaml:"repo,omitempty"`
	
	// The branch to clone. Currently unused by the VFS (defaults to main/HEAD), but reserved for future use.
	Branch   string      `yaml:"branch,omitempty"`
	
	// Aliases are alternative domains that should serve the exact same site. 
	// (e.g., if Repo is mapped to "gotcode.org", an alias could be "www.gotcode.org").
	Aliases  []string    `yaml:"aliases,omitempty"`
	
	// Optional SSH authentication configuration for private repositories.
	Auth     *AuthConfig `yaml:"auth,omitempty"`
	
	// If set, Loom will instantly return an HTTP 302 Redirect to this URL instead of serving files.
	Redirect string      `yaml:"redirect,omitempty"`
	
	// Optional paths to static TLS certificates if you don't want to use Let's Encrypt auto-SSL.
	TLSCert  string      `yaml:"tls_cert,omitempty"`
	TLSKey   string      `yaml:"tls_key,omitempty"`
}

// EngineConfig controls the limits of the markdown rendering engine and templates.
type EngineConfig struct {
	APITimeoutSeconds int `yaml:"api_timeout_seconds"`
	APIMaxPayloadMB   int `yaml:"api_max_payload_mb"`
}

// SecurityConfig controls the native Loom firewall and rate limiter.
type SecurityConfig struct {
	MaxRequestsPerMinute int      `yaml:"max_requests_per_minute"`
	MaxTrackedIPs        int      `yaml:"max_tracked_ips"`
	KnownHostsPath       string   `yaml:"known_hosts_path"`
	AllowedMethods       []string `yaml:"allowed_methods"`
	BlockedUserAgents    []string `yaml:"blocked_user_agents"`
	BlockedIPs           []string `yaml:"blocked_ips"`
}

// MetricsConfig controls the Prometheus /metrics endpoint.
type MetricsConfig struct {
	Enabled    bool     `yaml:"enabled"`
	Path       string   `yaml:"path"`
	AllowedIPs []string `yaml:"allowed_ips"` // Optional list of IPs allowed to scrape metrics
}

// Config represents the master root configuration for the entire Loom server.
type Config struct {
	Sites         map[string]SiteConfig `yaml:"sites"`
	VanityImports map[string]string     `yaml:"vanity_imports"`
	GitAuth       map[string]string     `yaml:"git_auth"`
	Security      *SecurityConfig       `yaml:"security"`
	Metrics       *MetricsConfig        `yaml:"metrics"`
	Engine        *EngineConfig         `yaml:"engine"`
}

// LoadConfig reads the physical server.yaml file from disk and unmarshals it into the Config struct.
func LoadConfig(path string) (*Config, error) {
	// 1. Read the raw bytes from the file on disk.
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	
	// 2. Unmarshal (convert) the YAML text into our Go Config struct.
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	
	// 3. Safety Check: Initialize maps so we don't get nil-pointer panics later
	// if the user left them entirely blank in the YAML file.
	if cfg.Sites == nil {
		cfg.Sites = make(map[string]SiteConfig)
	}
	if cfg.VanityImports == nil {
		cfg.VanityImports = make(map[string]string)
	}
	
	// Apply Sane Defaults for Security
	if cfg.Security == nil {
		cfg.Security = &SecurityConfig{}
	}
	if cfg.Security.MaxRequestsPerMinute == 0 {
		cfg.Security.MaxRequestsPerMinute = 150
	}
	if cfg.Security.MaxTrackedIPs == 0 {
		cfg.Security.MaxTrackedIPs = 25000
	}
	if cfg.Security.KnownHostsPath == "" {
		cfg.Security.KnownHostsPath = "/root/.ssh/known_hosts"
	}
	if len(cfg.Security.AllowedMethods) == 0 {
		// By default, only allow read-only requests. Drops all POST, PUT, DELETE attacks instantly.
		cfg.Security.AllowedMethods = []string{"GET", "HEAD"}
	}
	if len(cfg.Security.BlockedUserAgents) == 0 {
		// By default, drop the most common automated vulnerability scanners
		cfg.Security.BlockedUserAgents = []string{"masscan", "zgrab", "nuclei", "nmap", "curl"}
	}
	
	// Apply Sane Defaults for Engine
	if cfg.Engine == nil {
		cfg.Engine = &EngineConfig{}
	}
	if cfg.Engine.APITimeoutSeconds == 0 {
		cfg.Engine.APITimeoutSeconds = 10
	}
	if cfg.Engine.APIMaxPayloadMB == 0 {
		cfg.Engine.APIMaxPayloadMB = 5
	}

	// 4. Flatten Aliases for O(1) Routing Lookups:
	// If the user defined `aliases: ["www.example.com"]` inside the `example.com` site config,
	// we physically copy the `example.com` SiteConfig struct into the top-level Sites map
	// under the key `www.example.com`. 
	// This means during an incoming HTTP request, the router doesn't have to loop through aliases—
	// it can just do an instant map lookup `cfg.Sites[r.Host]`.
	for _, site := range cfg.Sites {
		for _, alias := range site.Aliases {
			cfg.Sites[alias] = site
		}
	}
	
	return &cfg, nil
}

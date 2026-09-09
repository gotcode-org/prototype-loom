package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/acme/autocert"
	
	"gotcode.org/loom/internal/cache"
	"gotcode.org/loom/internal/server"
)

// ============================================================================
// LOOM: DYNAMIC EDGE ROUTING ENGINE
// ============================================================================
// Welcome to the main entry point for Loom!
// Loom is a web server that completely bypasses the traditional local file system.
// Instead of serving HTML files from your hard drive, it clones a remote Git
// repository directly into RAM (using an embedded SQLite virtual file system),
// parses the Markdown files, evaluates them as Go templates, and serves them to
// the user instantly. 
//
// This file (main.go) is responsible for bootstrapping the application:
// 1. Parsing command line arguments (using Cobra)
// 2. Loading the server.yaml configuration file
// 3. Initializing the SQLite Edge Cache
// 4. Starting the HTTP/HTTPS server (with automatic Let's Encrypt SSL)
// ============================================================================

// rootCmd represents the base command when called without any subcommands.
// Cobra is a library used to build powerful CLI applications in Go (like Docker or Kubernetes).
var rootCmd = &cobra.Command{
	Use:   "loom",
	Short: "Loom is a dynamic, Git-first web server.",
	Long: `Loom is a high-performance web server that serves Markdown and HTML templates
directly from a remote Git repository, weaving API data into the templates on the fly.`,
	// If the user just types `loom` without `serve`, we print the help menu.
	Run: func(cmd *cobra.Command, args []string) {
		cmd.Help()
	},
}

// serveCmd represents the "serve" command (e.g., `loom serve --config server.yaml`).
// This is where the actual server boots up.
var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the Loom edge server",
	Run: func(cmd *cobra.Command, args []string) {
		// Grab the flags passed in via the command line
		configPath, _ := cmd.Flags().GetString("config") // Path to server.yaml
		devMode, _ := cmd.Flags().GetBool("dev")         // Disables HTTPS for local testing
		certFile, _ := cmd.Flags().GetString("cert")     // Path to custom SSL certificate
		keyFile, _ := cmd.Flags().GetString("key")       // Path to custom SSL private key

		fmt.Printf("Starting Loom server...\n")
		
		// ---------------------------------------------------------
		// STEP 1: Load Configuration
		// ---------------------------------------------------------
		// We read server.yaml to figure out which domains we are hosting,
		// which Git repositories they point to, and our security settings.
		cfg, err := server.LoadConfig(configPath)
		if err != nil {
			log.Fatalf("Failed to load config %s: %v", configPath, err)
		}
		fmt.Printf("Loaded %d sites and %d vanity imports from config.\n", len(cfg.Sites), len(cfg.VanityImports))

		// ---------------------------------------------------------
		// STEP 2: Initialize SQLite Edge Cache
		// ---------------------------------------------------------
		// Because cloning an entire Git repository on every HTTP request would be 
		// insanely slow, we cache the Git repository locally in an SQLite database.
		// When a request comes in, we serve the files instantly out of SQLite.
		c, err := cache.New("file:loom.db")
		if err != nil {
			log.Fatalf("Failed to initialize cache: %v", err)
		}
		defer c.Close()
		fmt.Println("SQLite cache initialized.")

		// ---------------------------------------------------------
		// STEP 3: Initialize Router and Telemetry
		// ---------------------------------------------------------
		// slog is Go's structured JSON logger. We use JSON so that external
		// monitoring tools (like Datadog or ELK) can easily parse our logs.
		logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
		slog.SetDefault(logger)
		
		// The Router handles every incoming HTTP request and decides which
		// site and file they are asking for.
		router := server.NewRouter(cfg, c, logger)

		// ---------------------------------------------------------
		// STEP 4: Start the HTTP/HTTPS Server
		// ---------------------------------------------------------
		// We support three modes of operation:
		// 1. DEV MODE: Runs on port 8080 without SSL (for local testing).
		// 2. STATIC TLS MODE: Runs on port 443 using manually provided SSL certificates.
		// 3. PRODUCTION MODE: Automatically generates free SSL certs using Let's Encrypt.
		
		// First, check if any site in server.yaml has static TLS certificates configured.
		hasStaticTLS := false
		for _, site := range cfg.Sites {
			if site.TLSCert != "" && site.TLSKey != "" {
				hasStaticTLS = true
				break
			}
		}

		if devMode {
			// ==========================================
			// MODE 1: DEVELOPMENT (Localhost HTTP)
			// ==========================================
			port := ":8080"
			fmt.Printf("DEV MODE ENABLED: Listening on http://localhost%s\n", port)
			
			srv := &http.Server{
				Addr:         port,
				Handler:      router,
				ReadTimeout:  10 * time.Second, // Prevent clients from keeping connections open forever
				WriteTimeout: 10 * time.Second,
			}
			
			log.Fatal(srv.ListenAndServe())
			
		} else if hasStaticTLS || (certFile != "" && keyFile != "") {
			// ==========================================
			// MODE 2: STATIC TLS (Manual Certificates)
			// ==========================================
			fmt.Println("STATIC TLS MODE: Booting with provided SSL certificates (SNI enabled)...")
			
			// Pre-load all configured certificates into a map.
			// SNI (Server Name Indication) allows us to serve multiple different SSL
			// certificates on the exact same IP address/Port depending on which domain
			// the browser is requesting.
			certs := make(map[string]*tls.Certificate)
			for host, site := range cfg.Sites {
				if site.TLSCert != "" && site.TLSKey != "" {
					cert, err := tls.LoadX509KeyPair(site.TLSCert, site.TLSKey)
					if err != nil {
						log.Fatalf("Failed to load certificate for %s: %v", host, err)
					}
					certs[host] = &cert
				}
			}
			
			// Load the fallback cert if provided via CLI flags
			var fallbackCert *tls.Certificate
			if certFile != "" && keyFile != "" {
				cert, err := tls.LoadX509KeyPair(certFile, keyFile)
				if err != nil {
					log.Fatalf("Failed to load fallback certificate: %v", err)
				}
				fallbackCert = &cert
			}

			// Configure the TLS handshake to pick the right certificate based on the requested domain.
			tlsConfig := &tls.Config{
				GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
					if cert, ok := certs[hello.ServerName]; ok {
						return cert, nil
					}
					if fallbackCert != nil {
						return fallbackCert, nil
					}
					return nil, fmt.Errorf("no static certificate found for %s", hello.ServerName)
				},
			}

			srv := &http.Server{
				Addr:         ":443",
				Handler:      router,
				ReadTimeout:  10 * time.Second,
				WriteTimeout: 10 * time.Second,
				TLSConfig:    tlsConfig,
			}
			
			log.Println("HTTPS server listening on :443")
			log.Fatal(srv.ListenAndServeTLS("", "")) // The cert paths are empty because GetCertificate handles it
			
		} else {
			// ==========================================
			// MODE 3: PRODUCTION (Let's Encrypt Auto-SSL)
			// ==========================================
			fmt.Println("PRODUCTION MODE: Booting Let's Encrypt autocert manager...")
			
			// Autocert Manager automatically reaches out to Let's Encrypt, proves we own the domain,
			// downloads the SSL certificate, and caches it locally in the "certs" folder.
			m := &autocert.Manager{
				Cache:      autocert.DirCache("certs"),
				Prompt:     autocert.AcceptTOS, // Accept Terms of Service automatically
				HostPolicy: func(ctx context.Context, host string) error {
					// SECURITY MEASURE: Only issue certificates for domains explicitly defined in server.yaml.
					// If we didn't do this, attackers could trick our server into generating thousands
					// of fake SSL certificates until Let's Encrypt bans our IP address.
					if _, exists := cfg.Sites[host]; exists {
						return nil
					}
					return fmt.Errorf("acme/autocert: host %s not configured in server.yaml", host)
				},
			}

			// We start a lightweight HTTP server on port 80 strictly to handle ACME challenges
			// (how Let's Encrypt verifies domain ownership) and to redirect all HTTP traffic to HTTPS.
			go func() {
				log.Println("HTTP server listening on :80 (Redirecting to HTTPS)")
				log.Fatal(http.ListenAndServe(":80", m.HTTPHandler(nil)))
			}()

			// The primary HTTPS server
			srv := &http.Server{
				Addr:         ":443",
				Handler:      router,
				ReadTimeout:  10 * time.Second,
				WriteTimeout: 10 * time.Second,
				TLSConfig: &tls.Config{
					GetCertificate: m.GetCertificate, // Tell the server to fetch certs from our Autocert Manager
				},
			}
			
			log.Println("HTTPS server listening on :443")
			log.Fatal(srv.ListenAndServeTLS("", ""))
		}
	},
}

// init() runs automatically before main(). We use it to attach our CLI flags.
func init() {
	rootCmd.AddCommand(serveCmd)
	
	serveCmd.Flags().StringP("config", "c", "server.yaml", "Path to the server configuration file")
	serveCmd.Flags().Bool("dev", false, "Enable local development mode (disables Let's Encrypt)")
	serveCmd.Flags().String("cert", "", "Path to static TLS certificate")
	serveCmd.Flags().String("key", "", "Path to static TLS private key")
}

// main() is the entry point of the compiled executable.
func main() {
	// Execute the Cobra root command to parse flags and run the requested subcommand.
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}
}

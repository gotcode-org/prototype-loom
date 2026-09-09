package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	yamlContent := []byte(`
sites:
  gotcode.org:
    repo: "https://github.com/gotcode-org/gotcode-web.git"
    branch: "main"
vanity_imports:
  "/loom": "git https://github.com/gotcode-org/loom"
`)
	tmp, _ := os.CreateTemp("", "server.yaml")
	defer os.Remove(tmp.Name())
	tmp.Write(yamlContent)
	tmp.Close()

	cfg, err := LoadConfig(tmp.Name())
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if cfg.Sites["gotcode.org"].Repo != "https://github.com/gotcode-org/gotcode-web.git" {
		t.Error("Failed to parse site repo")
	}

	if cfg.VanityImports["/loom"] != "git https://github.com/gotcode-org/loom" {
		t.Error("Failed to parse vanity import")
	}
}

func TestRouter_VanityImport(t *testing.T) {
	cfg := &Config{
		VanityImports: map[string]string{
			"/loom": "git https://github.com/gotcode-org/loom",
		},
	}
	router := NewRouter(cfg, nil, nil)

	req := httptest.NewRequest("GET", "https://gotcode.org/loom?go-get=1", nil)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("Expected 200 OK, got %d", rr.Code)
	}

	expectedContent := `<meta name="go-import" content="gotcode.org/loom git https://github.com/gotcode-org/loom">`
	if !strings.Contains(rr.Body.String(), expectedContent) {
		t.Errorf("Expected vanity import meta tag, got %s", rr.Body.String())
	}
}

func TestRouter_SiteNotFound(t *testing.T) {
	cfg := &Config{Sites: make(map[string]SiteConfig)}
	router := NewRouter(cfg, nil, nil)

	req := httptest.NewRequest("GET", "https://unknown.com/", nil)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("Expected 404 Not Found, got %d", rr.Code)
	}
}

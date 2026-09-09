package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	texttemplate "text/template"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sort"
	"time"

	"github.com/adrg/frontmatter"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"gotcode.org/loom/internal/vfs"
)

// ============================================================================
// CONTENT ENGINE (Markdown & Templating)
// ============================================================================
// This is the beating heart of Loom. It takes raw text (Markdown files) fetched
// from the Virtual File System, extracts their metadata, evaluates them as dynamic 
// Go templates, and finally compiles them into pure HTML.
// ============================================================================

// Document represents the final processed state of a Markdown file.
type Document struct {
	// Frontmatter contains the YAML metadata found at the top of the Markdown file
	// (e.g., title, date, author). We store it as a generic map so templates can read it.
	Frontmatter map[string]interface{}
	
	// BodyHTML contains the final rendered HTML of the Markdown body, ready to be 
	// injected into the base layout.
	BodyHTML    template.HTML
}

// ParseMarkdown is the core rendering pipeline. It takes raw bytes from a file,
// extracts the YAML, runs it through the Go template engine, and converts it to HTML.
func ParseMarkdown(raw []byte, gitAuth map[string]string, v *vfs.GitVFS, apiTimeoutSeconds int, apiMaxPayloadMB int, knownHostsPath string) (*Document, error) {
	var fm map[string]interface{}

	// ---------------------------------------------------------
	// 1. EXTRACT FRONTMATTER
	// ---------------------------------------------------------
	// Strip the YAML block (`---`) from the top of the file and save it to the `fm` map.
	// The `rest` variable now contains just the raw Markdown text.
	rest, err := frontmatter.Parse(bytes.NewReader(raw), &fm)
	if err != nil {
		return nil, fmt.Errorf("failed to parse frontmatter: %w", err)
	}

	// ---------------------------------------------------------
	// 2. CONFIGURE GOLDMARK (The Markdown-to-HTML Compiler)
	// ---------------------------------------------------------
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM), // Enable GitHub Flavored Markdown (tables, task lists)
		goldmark.WithParserOptions(
			parser.WithAutoHeadingID(), // Automatically generate HTML IDs for headers so anchor links work
		),
		goldmark.WithRendererOptions(
			html.WithUnsafe(), // CRITICAL: Allows users to write raw raw HTML inside their Markdown
		),
	)

	// ---------------------------------------------------------
	// 3. EXECUTE AS GO TEMPLATE (First Pass)
	// ---------------------------------------------------------
	// Before we convert the Markdown to HTML, we treat the Markdown itself as a Go template.
	// This allows users to write things like `{{ "{{" }} weave_git ... {{ "}}" }}` directly inside their Markdown!
	// We inject our custom `TemplateFuncMap` so those functions are available.
	tmpl, err := texttemplate.New("markdown").Funcs(texttemplate.FuncMap(TemplateFuncMap(gitAuth, v, apiTimeoutSeconds, apiMaxPayloadMB, knownHostsPath))).Parse(string(rest))
	if err != nil {
		return nil, fmt.Errorf("failed to parse markdown as go template: %w", err)
	}

	// We pass the Frontmatter `fm` into the template execution.
	// This allows the user to write `{{ "{{" }} .title {{ "}}" }}` inside their markdown to print their own title!
	var tplBuf bytes.Buffer
	if err := tmpl.Execute(&tplBuf, fm); err != nil {
		return nil, fmt.Errorf("failed to execute markdown template: %w", err)
	}

	// ---------------------------------------------------------
	// 4. COMPILE TO HTML (Second Pass)
	// ---------------------------------------------------------
	var finalPayload string
	contentType := "text/html"
	if ctVal, ok := fm["content_type"]; ok {
		if ctStr, isStr := ctVal.(string); isStr {
			contentType = ctStr
		}
	}

	// [SEO FEATURE] AVOID PARAGRAPH POLLUTION FOR RAW DATA
	// The Goldmark compiler is designed to take raw text and wrap it in HTML <p> (paragraph) tags.
	// This is great for blogs, but catastrophic for XML or JSON outputs.
	// If the frontmatter explicitly states this file is NOT HTML (e.g. text/xml for Sitemaps),
	// we completely bypass the Markdown compiler and preserve the exact data structure!
	if !strings.Contains(contentType, "text/html") && contentType != "" {
		// [BUGFIX] STRICT XML COMPLIANCE: 
		// Stripping the YAML `---` block mathematically leaves a hidden `\n` carriage return behind.
		// Web browsers will throw a fatal syntax error if an XML file doesn't start exactly at Byte 0.
		// We use `TrimSpace` to surgically remove that invisible artifact before shipping it.
		finalPayload = strings.TrimSpace(tplBuf.String())
	} else {
		// Standard HTML page. Run it through the Goldmark engine to generate the <h1>, <p>, and <a> tags!
		var buf bytes.Buffer
		if err := md.Convert(tplBuf.Bytes(), &buf); err != nil {
			return nil, fmt.Errorf("failed to render markdown: %w", err)
		}
		finalPayload = buf.String()
	}

	return &Document{
		Frontmatter: fm,
		BodyHTML:    template.HTML(finalPayload), // Cast to template.HTML so Go doesn't auto-escape the brackets
	}, nil
}

// ============================================================================
// TEMPLATE FUNCTIONS
// ============================================================================
// The functions below are injected directly into the HTML and Markdown templates,
// giving authors extreme power to fetch data dynamically at render time.

// weaveAPI fetches JSON from an external URL and makes it available to the template.
// Example Usage: {{ "{{" }} range $repo := weave_api "https://api.github.com/users/gotunix/repos" {{ "}}" }}
func weaveAPI(url string) (interface{}, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch API %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API %s returned status %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var data interface{}
	// Unmarshal the JSON into a generic interface{} so Go templates can range over it dynamically.
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON from %s: %w", url, err)
	}

	return data, nil
}

// getHost extracts the domain name from a Git URL (e.g., git@github.com:repo.git -> github.com)
// We need this so we know which SSH private key to use from server.yaml's `git_auth` map.
func getHost(repoURL string) string {
	if strings.HasPrefix(repoURL, "git@") {
		parts := strings.SplitN(repoURL[4:], ":", 2)
		return parts[0]
	}
	u, err := url.Parse(repoURL)
	if err == nil {
		return u.Host
	}
	return ""
}

// weaveGitFile dynamically clones a REMOTE Git repository into RAM, finds a specific file,
// parses it as Markdown, and returns the rendered HTML to be injected into the current page.
// Example Usage: {{ "{{" }} weave_git "https://github.com/project/docs.git" "README.md" {{ "}}" }}
func weaveGitFile(gitAuth map[string]string, repoURL, filePath string, knownHostsPath string) (template.HTML, error) {
	// Look up the SSH key path for this remote host
	authKeyPath := ""
	if gitAuth != nil {
		host := getHost(repoURL)
		if key, ok := gitAuth[host]; ok {
			authKeyPath = key
		}
	}

	// Clone the repo entirely into memory (shallow clone)
	v, err := vfs.CloneInMemory(context.Background(), repoURL, authKeyPath, knownHostsPath)
	if err != nil {
		return "", fmt.Errorf("failed to clone %s: %w", repoURL, err)
	}

	// Read the specific file from the memory cache
	raw, err := v.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to read %s: %w", filePath, err)
	}

	// Compile that remote file's markdown into HTML
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(html.WithUnsafe()),
	)

	var buf bytes.Buffer
	if err := md.Convert(raw, &buf); err != nil {
		return "", fmt.Errorf("failed to render markdown from git: %w", err)
	}

	return template.HTML(buf.String()), nil
}

// PageMeta holds the minimal metadata needed to display a link to a blog post or page.
type PageMeta struct {
	Path        string                 // The URL path (e.g., /blog/post)
	Frontmatter map[string]interface{} // The YAML data (title, date, author)
}

// TemplateFuncMap is the master registry of all custom functions available to Loom templates.
func TemplateFuncMap(gitAuth map[string]string, v *vfs.GitVFS, apiTimeoutSeconds int, apiMaxPayloadMB int, knownHostsPath string) template.FuncMap {
	return template.FuncMap{
		// 1. weave_api: Fetch external JSON with safety limits
		"weave_api": func(url string) (interface{}, error) {
			client := &http.Client{
				Timeout: time.Duration(apiTimeoutSeconds) * time.Second,
			}
			
			resp, err := client.Get(url)
			if err != nil {
				return nil, fmt.Errorf("failed to fetch API %s: %w", url, err)
			}
			defer resp.Body.Close()
		
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return nil, fmt.Errorf("API %s returned status %d", url, resp.StatusCode)
			}
		
			// Limit the payload size to prevent OOM
			maxBytes := int64(apiMaxPayloadMB * 1024 * 1024)
			body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes))
			if err != nil {
				return nil, err
			}
		
			var data interface{}
			if err := json.Unmarshal(body, &data); err != nil {
				return nil, fmt.Errorf("failed to unmarshal JSON from %s: %w", url, err)
			}
		
			return data, nil
		},
		
		// 2. weave_git: Fetch and render remote markdown
		"weave_git": func(repoURL, filePath string) (template.HTML, error) {
			return weaveGitFile(gitAuth, repoURL, filePath, knownHostsPath)
		},
		
		// 3. list_pages: Scans a directory in the CURRENT repository, parses the frontmatter
		// of every markdown file, and returns them sorted by date. Perfect for building Blog indexes!
		"list_pages": func(dirPath string) ([]PageMeta, error) {
			if v == nil {
				return nil, fmt.Errorf("list_pages: VFS not available")
			}
			
			// Scan the directory
			files, err := v.ReadDir(dirPath)
			if err != nil {
				return nil, err
			}
			
			var pages []PageMeta
			for _, f := range files {
				// Ignore non-markdown files and the index.md file itself (to prevent infinite loops)
				if !strings.HasSuffix(f.Name(), ".md") || f.Name() == "index.md" {
					continue
				}
				
				fullPath := dirPath + "/" + f.Name()
				if strings.HasPrefix(fullPath, "/") {
					fullPath = fullPath[1:]
				}
				
				// Read the raw file bytes
				raw, err := v.ReadFile(fullPath)
				if err != nil {
					continue
				}
				
				// Extract just the Frontmatter (we don't need to render the body for an index list)
				var fm map[string]interface{}
				_, err = frontmatter.Parse(bytes.NewReader(raw), &fm)
				if err == nil {
					// Clean up the path so it generates a valid URL (e.g., /blog/my-post)
					webPath := "/" + strings.TrimSuffix(fullPath, ".md")
					webPath = strings.ReplaceAll(webPath, "content/", "")
					
					pages = append(pages, PageMeta{
						Path:        webPath,
						Frontmatter: fm,
					})
				}
			}
			
			// Automatically sort the pages by the "date" field in their YAML frontmatter (Descending)
			sort.Slice(pages, func(i, j int) bool {
				dateIStr, _ := pages[i].Frontmatter["date"].(string)
				dateJStr, _ := pages[j].Frontmatter["date"].(string)
				
				// Loom expects dates formatted like "Aug 26, 2026"
				dateI, errI := time.Parse("Jan 02, 2006", dateIStr)
				dateJ, errJ := time.Parse("Jan 02, 2006", dateJStr)
				
				if errI == nil && errJ == nil {
					return dateI.After(dateJ)
				}
				// If dates are missing or corrupted, fallback to alphabetical sorting
				return pages[i].Path > pages[j].Path
			})

			return pages, nil
		},
		
		// 4. recent_pages: Exactly the same as list_pages, but slices the array to a maximum limit.
		// Great for displaying the "3 latest posts" on a homepage.
		"recent_pages": func(dirPath string, limit int) ([]PageMeta, error) {
			pages, err := TemplateFuncMap(gitAuth, v, apiTimeoutSeconds, apiMaxPayloadMB, knownHostsPath)["list_pages"].(func(string) ([]PageMeta, error))(dirPath)
			if err != nil {
				return nil, err
			}
			if len(pages) > limit {
				return pages[:limit], nil
			}
			return pages, nil
		},
	}
}

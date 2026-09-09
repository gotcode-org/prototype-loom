package vfs

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	billy "github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/go-git/go-git/v5/config"
	"golang.org/x/crypto/ssh/knownhosts"
)

// ============================================================================
// VIRTUAL FILE SYSTEM (VFS)
// ============================================================================
// This package is the magic behind Loom. It allows us to interact with a Git
// repository identically to how we would interact with a physical hard drive, 
// EXCEPT everything happens entirely in RAM. 
// No files are ever written to the physical server's disk.
// ============================================================================

// GitVFS represents a loaded in-memory virtual filesystem.
type GitVFS struct {
	Repo *git.Repository   // The underlying go-git repository engine
	FS   billy.Filesystem  // The billy memory-filesystem interface (acts like 'os' package)
}

// GetAuth is a helper function that reads an SSH private key from the physical
// server's hard drive and converts it into a Git Authentication method.
func GetAuth(authKeyPath string, knownHostsPath string) (transport.AuthMethod, error) {
	// If no path is provided, it's a public repository. No auth needed.
	if authKeyPath == "" {
		return nil, nil
	}
	
	// If the user specifies "agent", we connect to their local ssh-agent process.
	// This is great for local development where you don't want to hardcode key paths.
	if authKeyPath == "agent" {
		auth, err := ssh.NewSSHAgentAuth("git")
		if err != nil {
			return nil, fmt.Errorf("failed to connect to ssh-agent: %w", err)
		}
		hostKeyCallback, err := knownhosts.New(knownHostsPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load known_hosts from %s: %w", knownHostsPath, err)
		}
		auth.HostKeyCallback = hostKeyCallback
		return auth, nil
	}
	
	// Otherwise, we load the specific private key file from disk.
	publicKeys, err := ssh.NewPublicKeysFromFile("git", authKeyPath, "")
	if err != nil {
		// If the SSH key is encrypted with a password, we can't read it automatically.
		// As a fallback, we try to grab it from the ssh-agent.
		if strings.Contains(err.Error(), "empty password") {
			auth, agentErr := ssh.NewSSHAgentAuth("git")
			if agentErr == nil {
				hostKeyCallback, err := knownhosts.New(knownHostsPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load known_hosts from %s: %w", knownHostsPath, err)
		}
		auth.HostKeyCallback = hostKeyCallback
				return auth, nil
			}
			return nil, fmt.Errorf("ssh key requires a password, but ssh-agent fallback failed: %w", err)
		}
		return nil, fmt.Errorf("failed to load ssh key %s: %w", authKeyPath, err)
	}
	
	// WARNING: We ignore host key checking here. In a highly secure production
	// environment, you would want to strictly verify the known_hosts file of the Git server.
	hostKeyCallback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load known_hosts from %s: %w", knownHostsPath, err)
	}
	publicKeys.HostKeyCallback = hostKeyCallback
	return publicKeys, nil
}

// CloneInMemory reaches out to a remote Git repository (e.g. GitHub) and downloads
// it directly into RAM. It NEVER touches the physical hard drive.
func CloneInMemory(ctx context.Context, url string, authKeyPath string, knownHostsPath string) (*GitVFS, error) {
	// Create the memory storage buckets
	storer := memory.NewStorage() // Holds the Git objects/commits
	fs := memfs.New()             // Holds the actual file tree

	opts := &git.CloneOptions{
		URL:          url,
		Depth:        1,   // We ONLY download the very last commit (Shallow Clone) to save RAM and time!
		SingleBranch: true, // We don't care about feature branches, only the main deployed branch.
	}

	auth, err := GetAuth(authKeyPath, knownHostsPath)
	if err != nil {
		return nil, err
	}
	opts.Auth = auth

	// Execute the clone over the network
	repo, err := git.CloneContext(ctx, storer, fs, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to clone %s: %w", url, err)
	}

	return &GitVFS{
		Repo: repo,
		FS:   fs,
	}, nil
}

// ReadFile acts exactly like os.ReadFile, except it reads from our RAM cache instead of the hard drive.
func (v *GitVFS) ReadFile(path string) ([]byte, error) {
	file, err := v.FS.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open file %s: %w", path, err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %w", path, err)
	}

	return data, nil
}

// ReadDir acts exactly like os.ReadDir, listing the contents of a directory in RAM.
func (v *GitVFS) ReadDir(path string) ([]os.FileInfo, error) {
	return v.FS.ReadDir(path)
}

// GetRemoteHash is a highly optimized function that pings the remote Git server
// and asks "What is your latest commit hash?" WITHOUT actually cloning the repo.
// The Router uses this in a background loop every 30 seconds to know if it needs to update.
func GetRemoteHash(ctx context.Context, url string, authKeyPath string, knownHostsPath string) (string, error) {
	auth, err := GetAuth(authKeyPath, knownHostsPath)
	if err != nil {
		return "", err
	}

	// Create a "fake" remote pointer
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name: "origin",
		URLs: []string{url},
	})

	// Just list the references (like `git ls-remote`)
	refs, err := remote.List(&git.ListOptions{Auth: auth})
	if err != nil {
		return "", fmt.Errorf("failed to list remote refs: %w", err)
	}

	// Try to find the 'main' branch specifically
	for _, ref := range refs {
		if ref.Name().IsBranch() && ref.Name().Short() == "main" {
			return ref.Hash().String(), nil
		}
	}
	
	// If there is no branch explicitly named 'main', fallback to whatever HEAD points to (e.g. master)
	for _, ref := range refs {
		if ref.Name().String() == "HEAD" {
			return ref.Hash().String(), nil
		}
	}

	return "", fmt.Errorf("could not find main branch or HEAD on remote")
}

// CurrentHash tells us the exact commit hash of the files we currently have loaded in RAM.
func (v *GitVFS) CurrentHash() string {
	ref, err := v.Repo.Head()
	if err != nil {
		return ""
	}
	return ref.Hash().String()
}

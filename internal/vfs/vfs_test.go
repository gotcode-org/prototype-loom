package vfs

import (
	"context"
	"testing"
	"time"
)

func TestCloneInMemory(t *testing.T) {
	// We'll use a fast, public, and small Git repository for testing the VFS.
	// We use the go-billy repo itself as a reliable test target.
	testURL := "https://github.com/go-git/go-billy.git"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	vfs, err := CloneInMemory(ctx, testURL, "")
	if err != nil {
		t.Fatalf("Failed to clone repository: %v", err)
	}

	if vfs.Repo == nil {
		t.Error("Expected repository to be initialized, got nil")
	}

	if vfs.FS == nil {
		t.Error("Expected filesystem to be initialized, got nil")
	}

	// Try reading a file that should exist in the repository (e.g., README.md)
	data, err := vfs.ReadFile("README.md")
	if err != nil {
		t.Fatalf("Failed to read README.md: %v", err)
	}

	if len(data) == 0 {
		t.Error("Expected README.md to have content, got 0 bytes")
	}
}

func TestReadFile_NotFound(t *testing.T) {
	testURL := "https://github.com/go-git/go-billy.git"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	vfs, err := CloneInMemory(ctx, testURL, "")
	if err != nil {
		t.Fatalf("Failed to clone repository: %v", err)
	}

	_, err = vfs.ReadFile("does_not_exist.txt")
	if err == nil {
		t.Error("Expected error when reading non-existent file, got nil")
	}
}

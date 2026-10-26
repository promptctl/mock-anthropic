package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/promptctl/mock-anthropic/internal/account"
)

// PROJECT.md Acceptance: the repository contains no real identifiers. Every tracked file is
// scanned as bytes, markdown included, so a foreign UUID anywhere in the tree fails here.
func TestTrackedFilesCarryNoForeignUUIDs(t *testing.T) {
	top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	root := strings.TrimSpace(string(top))
	list, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	files := bytes.Split(bytes.TrimSuffix(list, []byte{0}), []byte{0})
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(root, string(name)))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		for _, uuid := range account.ForeignUUIDs(data) {
			t.Errorf("%s: foreign UUID %s", name, uuid)
		}
	}
}

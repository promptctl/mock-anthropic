package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/promptctl/mock-anthropic/internal/account"
)

// PROJECT.md Acceptance: the repository contains no real identifiers. Every blob in the index, markdown
// included, is scanned as bytes: the index is what a commit carries, so the working tree's edits and
// unstaged deletions neither hide nor invent a hit. A hit is reported by offset, never by value, so a
// failing run does not copy a real identifier into a public CI log.
func TestTrackedFilesCarryNoForeignUUIDs(t *testing.T) {
	root := strings.TrimSpace(string(git(t, nil, "rev-parse", "--show-toplevel")))
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// Outside this repo's own checkout (a module cache inside some other work tree), the index is not ours.
	if same, err := sameDir(root, wd); err != nil || !same {
		t.Fatalf("test runs in %s, but the git top level is %s: not this repository's checkout (%v)", wd, root, err)
	}
	var objects bytes.Buffer
	var names []string
	for _, entry := range strings.Split(strings.TrimSuffix(string(git(t, nil, "ls-files", "-s", "-z")), "\x00"), "\x00") {
		// "<mode> <object> <stage>\t<path>"; a gitlink (160000) names a commit in another repo, not a blob.
		meta, name, found := strings.Cut(entry, "\t")
		f := strings.Fields(meta)
		if !found || len(f) != 3 {
			t.Fatalf("git ls-files -s entry %q", entry)
		}
		if f[0] == "160000" {
			continue
		}
		objects.WriteString(f[1] + "\n")
		names = append(names, name)
	}
	out := bufio.NewReader(bytes.NewReader(git(t, &objects, "cat-file", "--batch")))
	for _, name := range names {
		data, err := readBlob(out)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, offset := range account.ForeignUUIDs(data) {
			t.Errorf("%s: foreign UUID at byte %d", name, offset)
		}
	}
}

func sameDir(a, b string) (bool, error) {
	ai, err := os.Stat(filepath.Clean(a))
	if err != nil {
		return false, err
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(ai, bi), nil
}

// git runs git and returns its stdout as bytes: cat-file content is data, not a line.
func git(t *testing.T, stdin io.Reader, args ...string) []byte {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Stdin = stdin
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, bytes.TrimSpace(exit.Stderr))
		}
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

// readBlob reads one "<object> blob <size>\n<content>\n" record of git cat-file --batch.
func readBlob(r *bufio.Reader) ([]byte, error) {
	header, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	var object, kind string
	var size int
	if _, err := fmt.Sscanf(header, "%s %s %d\n", &object, &kind, &size); err != nil || kind != "blob" {
		return nil, fmt.Errorf("cat-file header %q", strings.TrimSpace(header))
	}
	data := make([]byte, size+1)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, err
	}
	if data[size] != '\n' {
		return nil, fmt.Errorf("cat-file record for %s does not end after %d bytes", object, size)
	}
	return data[:size], nil
}

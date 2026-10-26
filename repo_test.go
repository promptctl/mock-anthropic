package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/promptctl/mock-anthropic/internal/account"
)

// PROJECT.md Acceptance: the repository contains no real identifiers. Every blob in the index, markdown
// included, is scanned as bytes: the index is what a commit carries, so the working tree's edits and
// unstaged deletions neither hide nor invent a hit.
func TestTrackedFilesCarryNoForeignUUIDs(t *testing.T) {
	root := strings.TrimSpace(git(t, nil, "rev-parse", "--show-toplevel"))
	var objects bytes.Buffer
	var names []string
	for _, entry := range strings.Split(strings.TrimSuffix(git(t, nil, "-C", root, "ls-files", "-s", "-z"), "\x00"), "\x00") {
		// "<mode> <object> <stage>\t<path>"; a gitlink (160000) names a commit in another repo, not a blob.
		meta, name, _ := strings.Cut(entry, "\t")
		f := strings.Fields(meta)
		if f[0] == "160000" {
			continue
		}
		objects.WriteString(f[1] + "\n")
		names = append(names, name)
	}
	out := bufio.NewReader(strings.NewReader(git(t, &objects, "-C", root, "cat-file", "--batch")))
	for _, name := range names {
		data, err := readBlob(out)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, uuid := range account.ForeignUUIDs(data) {
			t.Errorf("%s: foreign UUID %s", name, uuid)
		}
	}
}

// git runs git and returns its stdout untrimmed: cat-file content is bytes, not a line.
func git(t *testing.T, stdin io.Reader, args ...string) string {
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
	return string(out)
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
	return data[:size], nil
}

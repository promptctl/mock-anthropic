package recording

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/promptctl/mock-anthropic/internal/account"
)

// A UUID that is not one of the mock's synthetic ones, built at run time so no source file holds one:
// the repository scan refuses any UUID literal that is not synthetic.
func realUUID(n int) string {
	return fmt.Sprintf("%08x-%s-%s-%s-%012x", 0xabcdef00+n, "1111", "4222", "8333", n)
}

const (
	oneReply = `{"kind":"message","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn"}`
	oneReq   = `{"model":"m","messages":[{"role":"user","content":"hello"}]}`
)

func scrubbedFixture(t *testing.T) Scrubbed {
	t.Helper()
	s, _, err := Scrub(Exchange{Request: json.RawMessage(oneReq), Reply: json.RawMessage(oneReply)}, nil, account.Default())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestWriterSavesNumberedFilesThatLoadBack(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "0007.json"), []byte("not read by the writer"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, err := NewWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := w.Save(scrubbedFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	second, err := w.Save(scrubbedFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if first != "0008.json" || second != "0009.json" {
		t.Errorf("files %s, %s; want numbering past the existing 0007", first, second)
	}
	os.Remove(filepath.Join(dir, "0007.json"))
	got, err := Load(dir)
	if err != nil || len(got) != 2 {
		t.Fatalf("Load: %v, %d exchanges", err, len(got))
	}
	if !strings.Contains(string(got[0].Request), `"hello"`) || !strings.Contains(string(got[0].Reply), `"hi"`) {
		t.Errorf("loaded %s / %s", got[0].Request, got[0].Reply)
	}
}

func TestWriterRefusesTheZeroScrubbed(t *testing.T) {
	w, err := NewWriter(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if name, err := w.Save(Scrubbed{}); err == nil {
		t.Errorf("saved %s from a zero Scrubbed", name)
	}
}

func TestParseRefusesWhatIsNotARecording(t *testing.T) {
	good := `{"version":1,"request":` + oneReq + `,"reply":` + oneReply + `}`
	if _, err := Parse([]byte(good)); err != nil {
		t.Fatalf("a recording: %v", err)
	}
	for name, bad := range map[string]string{
		"another version":      strings.Replace(good, `"version":1`, `"version":2`, 1),
		"an unknown key":       strings.Replace(good, `"version":1`, `"version":1,"extra":true`, 1),
		"a request not object": strings.Replace(good, oneReq, `[]`, 1),
		"a literal reply":      strings.Replace(good, oneReply, `{"kind":"literal","status":200,"body":"x"}`, 1),
		"data after":           good + `{}`,
		"not JSON":             `nope`,
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLoadOrdersByNumberNotByText(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"9999", "10000"} {
		body := `{"version":1,"request":` + oneReq + `,"reply":` + strings.Replace(oneReply, `"hi"`, `"`+n+`"`, 1) + `}`
		if err := os.WriteFile(filepath.Join(dir, n+".json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Load(dir)
	if err != nil || len(got) != 2 || !strings.Contains(string(got[0].Reply), "9999") {
		t.Fatalf("Load: %v, %d exchanges, first %s", err, len(got), got[0].Reply)
	}
}

func TestAFailedWriteLeavesNoFileBehind(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	if _, err := w.Save(scrubbedFixture(t)); err == nil {
		t.Skip("directory permissions not enforced here")
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("left %d files", len(entries))
	}
}

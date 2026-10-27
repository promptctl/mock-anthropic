// Package recording is PROJECT.md "Recordings": the form a saved exchange takes, and the only code that
// reads or writes it. [LAW:one-source-of-truth] Record mode writes it and replay mode reads it through
// here, so the two cannot drift.
package recording

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"sync"

	"github.com/promptctl/mock-anthropic/internal/script"
)

// Version is the form's version; a file of another version is refused rather than guessed at.
const Version = 2

// Exchange is one recorded Messages exchange: the request body as sent, and the assistant message it was
// answered with. Only a successful `POST /v1/messages` is ever recorded, so the method, host, path and
// status are not stored: each can hold one value. Reply is a script file's reply form (kind "message"),
// read by script.ParseReply, so a replay renders it exactly as it renders a script's.
//
// MessageID is the id of the real reply: the request that continues the conversation names it, so a
// replay answers with the same one. File is where Load read it from, and is empty for an exchange made
// in memory; it is not part of the saved form.
type Exchange struct {
	Request   json.RawMessage
	Reply     json.RawMessage
	MessageID string
	File      string
}

type wire struct {
	Version   int             `json:"version"`
	Request   json.RawMessage `json:"request"`
	Reply     json.RawMessage `json:"reply"`
	MessageID string          `json:"message_id"`
}

// messageID is the shape of the API's message ids.
var messageID = regexp.MustCompile(`^msg_[A-Za-z0-9_]+$`)

// Parse is the one reader of a recording. [LAW:parse-dont-validate] An Exchange that came out of it has a
// request that is a JSON object and a reply script.ParseReply accepts as a message.
func Parse(data []byte) (Exchange, error) {
	var w wire
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return Exchange{}, fmt.Errorf("not a recording: %w", err)
	}
	if dec.More() {
		return Exchange{}, errors.New("not a recording: data after the object")
	}
	if w.Version != Version {
		return Exchange{}, fmt.Errorf("version %d, this mock reads version %d", w.Version, Version)
	}
	e := Exchange{Request: w.Request, Reply: w.Reply, MessageID: w.MessageID}
	return e, e.check()
}

func (e Exchange) check() error {
	var request map[string]json.RawMessage
	if err := json.Unmarshal(e.Request, &request); err != nil {
		return errors.New("request: expected a JSON object")
	}
	reply, err := script.ParseReply(e.Reply)
	if err != nil {
		return fmt.Errorf("reply: %w", err)
	}
	if _, ok := reply.(script.MessageReply); !ok {
		return errors.New(`reply: expected kind "message"`)
	}
	if !messageID.MatchString(e.MessageID) {
		return fmt.Errorf("message_id: expected msg_ and letters, digits or _, got %q", e.MessageID)
	}
	return nil
}

// marshal is the one writer: indented, so a recording diffs and reads as text.
func (e Exchange) marshal() ([]byte, error) {
	if err := e.check(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(wire{Version, e.Request, e.Reply, e.MessageID})
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

var fileName = regexp.MustCompile(`^(\d{4,})\.json$`)

// Load reads every recording in dir, in numbered order.
func Load(dir string) ([]Exchange, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if fileName.MatchString(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	// The number, not the text: 10000.json follows 9999.json.
	slices.SortFunc(names, func(a, b string) int {
		x, _ := strconv.Atoi(fileName.FindStringSubmatch(a)[1])
		y, _ := strconv.Atoi(fileName.FindStringSubmatch(b)[1])
		return cmp.Compare(x, y)
	})
	var out []Exchange
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		e, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(dir, name), err)
		}
		e.File = filepath.Join(dir, name)
		out = append(out, e)
	}
	return out, nil
}

// Writer saves scrubbed exchanges into one directory, each as the next unused `NNNN.json`.
// [LAW:no-shared-mutable-globals] It owns the numbering; the directory's own files are the only other
// writer, and O_EXCL keeps a file made since the scan from being replaced.
type Writer struct {
	dir  string
	mu   sync.Mutex
	next int
}

// NewWriter numbers past the recordings dir already holds.
func NewWriter(dir string) (*Writer, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	w := &Writer{dir: dir, next: 1}
	for _, entry := range entries {
		if m := fileName.FindStringSubmatch(entry.Name()); m != nil {
			n, _ := strconv.Atoi(m[1])
			w.next = max(w.next, n+1)
		}
	}
	return w, nil
}

// Save writes s as a new file and returns its name. Only a Scrubbed can be saved.
func (w *Writer) Save(s Scrubbed) (string, error) {
	data, err := s.exchange.marshal()
	if err != nil {
		return "", err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for {
		name := fmt.Sprintf("%04d.json", w.next)
		w.next++
		f, err := os.OpenFile(filepath.Join(w.dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, werr := f.Write(data)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			// A truncated file would make the whole directory unloadable.
			return "", errors.Join(werr, os.Remove(filepath.Join(w.dir, name)))
		}
		return name, nil
	}
}

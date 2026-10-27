// Package script is PROJECT.md "Scripts of model replies": which reply answers a Messages request. A
// consumer declares scripts as files in directories; the mock's built-in answers are scripts written in
// code. Both are tried in one order, and the first that answers wins.
package script

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/promptctl/mock-anthropic/internal/messages"
)

// Reply is what a script answers with: an assistant message the mock renders as the request asks
// (stream or JSON), or a literal response sent as written. [LAW:types-are-the-program] A delay on a
// literal or a status on a message cannot be written.
type Reply interface{ reply() }

// StopReason is why a message reply ends, spelled as the API spells it.
type StopReason string

const (
	StopEndTurn   StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
)

// MessageReply ends on tool_use exactly when it holds a tool call, as the API's replies do; one cut off
// at max_tokens may hold one or not. EventDelay is the wait between streamed events.
type MessageReply struct {
	Content    []Block
	StopReason StopReason
	EventDelay time.Duration
}

// LiteralReply is a response a Response can carry. Body is nil for a status that has none (204, 205,
// 304: RFC 9110 15.3.5, 15.3.6, 15.4.5), so the response carries no body and no Content-Length.
type LiteralReply struct {
	Status int
	Header http.Header
	Body   []byte
}

// RecordedReply is a message reply that keeps the id of the real reply it was recorded from, so that the
// request which continues the conversation names it (`diagnostics.previous_message_id`). File is the
// recording it came from. Only package recording makes one; a script file's form cannot carry an id.
type RecordedReply struct {
	MessageReply
	ID   string
	File string
}

func (MessageReply) reply()  {}
func (LiteralReply) reply()  {}
func (RecordedReply) reply() {}

// Block is one content block of a message reply.
type Block interface{ block() }

type (
	// Text is a `text` block.
	Text struct{ Text string }
	// ToolUse is a `tool_use` block; Input is a JSON object, as written.
	ToolUse struct {
		Name  string
		Input json.RawMessage
	}
	// Thinking is a `thinking` block.
	Thinking struct{ Thinking string }
)

func (Text) block()     {}
func (ToolUse) block()  {}
func (Thinking) block() {}

// Script is a named source that answers a request or passes (Answer returns nil). [LAW:one-type-per-behavior]
// A consumer's file and a built-in answer are one thing; Origin is a file's path or a built-in's name.
type Script struct {
	Origin string
	Answer func(messages.Request) Reply
}

// Selection is which script answered, or every script tried, so a request no script answered can be
// told apart from one that found no scripts at all. [LAW:nothing-unseen]
type Selection interface{ selection() }

// Matched is the script that answered first.
type Matched struct {
	Origin string
	Reply  Reply
}

// Unmatched lists every script tried, in order.
type Unmatched struct{ Tried []string }

func (Matched) selection()   {}
func (Unmatched) selection() {}

// FirstReply is the first script, in order, that answers r.
func FirstReply(scripts []Script, r messages.Request) Selection {
	tried := make([]string, 0, len(scripts))
	for _, s := range scripts {
		if reply := s.Answer(r); reply != nil {
			return Matched{Origin: s.Origin, Reply: reply}
		}
		tried = append(tried, s.Origin)
	}
	return Unmatched{Tried: tried}
}

// InOrder is [LAW:one-source-of-truth] the one place the order is written: consumer directories in the
// order given, then the recordings, then the built-ins. Directories are read on every call, so a script
// written after the mock started answers the next request; recordings are loaded once, by the caller.
func InOrder(dirs []string, recorded []Script, builtIns []Script) ([]Script, error) {
	scripts, err := Read(dirs)
	if err != nil {
		return nil, err
	}
	return append(append(scripts, recorded...), builtIns...), nil
}

// Error is a script, or a scripts directory, that cannot be used; the text names its path.
// [LAW:no-silent-failure] The Messages generator turns it into a 500 and a FAILED line.
type Error struct {
	Subject string // "script" or "scripts directory"
	Path    string
	Why     string
}

func (e *Error) Error() string { return e.Subject + " " + e.Path + ": " + e.Why }

const suffix = ".json"

// Read is [LAW:effects-at-boundaries] the one read of the consumer's directories: each directory's
// `.json` entries in the byte order of their names, which no locale changes. A hidden name (an editor's
// lock file, macOS's `._` metadata) or any other suffix is not a script, so a consumer that writes
// `x.json.tmp` and renames it never has a half-written script read. One file is read at a time, in the
// order they are tried, so when several are bad the one named is the first in that order.
func Read(dirs []string) ([]Script, error) {
	scripts := []Script{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir) // sorted by file name
		if err != nil {
			return nil, &Error{"scripts directory", dir, "cannot read: " + err.Error()}
		}
		for _, e := range entries {
			name := e.Name()
			if !strings.HasSuffix(name, suffix) || strings.HasPrefix(name, ".") {
				continue
			}
			s, err := readScript(filepath.Join(dir, name))
			if err != nil {
				return nil, err
			}
			if s != nil {
				scripts = append(scripts, *s)
			}
		}
	}
	return scripts, nil
}

// readScript is nil for a script removed after its directory was listed; a name still there that cannot
// be read, such as a dangling symlink, is an error.
func readScript(path string) (*Script, error) {
	text, err := readRegularFile(path)
	if err != nil {
		return nil, err
	}
	if text == nil {
		return nil, nil
	}
	bad := func(why string) (*Script, error) { return nil, &Error{"script", path, why} }
	var anything any
	if err := json.Unmarshal(text, &anything); err != nil {
		return bad("not JSON: " + err.Error())
	}
	f, err := parseFile(text)
	if err != nil {
		return bad(err.Error())
	}
	return &Script{Origin: path, Answer: func(r messages.Request) Reply {
		for _, needle := range f.bodyContains {
			if !strings.Contains(r.Raw, needle) {
				return nil
			}
		}
		for _, needle := range f.lastUserContains {
			if !anyContains(r.LastUserTexts, needle) {
				return nil
			}
		}
		return f.reply
	}}, nil
}

func anyContains(texts []string, needle string) bool {
	for _, t := range texts {
		if strings.Contains(t, needle) {
			return true
		}
	}
	return false
}

// readRegularFile is nil when the file is gone. [LAW:no-silent-failure] It is opened without blocking
// and checked on the open handle, so a name that is not a regular file is an error instead of being read:
// a named pipe would hold the request open forever, waiting for a writer. The check and the read are on
// one handle, so nothing can swap the file between. Not UTF-8 is an error, so a file never has needles
// that silently never match.
func readRegularFile(path string) ([]byte, error) {
	cannotRead := func(err error) error { return &Error{"script", path, "cannot read: " + err.Error()} }
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if _, lerr := os.Lstat(path); errors.Is(lerr, os.ErrNotExist) {
			return nil, nil
		}
		return nil, cannotRead(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, cannotRead(err)
	}
	if !info.Mode().IsRegular() {
		return nil, cannotRead(errors.New("not a regular file"))
	}
	text, err := io.ReadAll(f)
	if err != nil {
		return nil, cannotRead(err)
	}
	if !utf8.Valid(text) {
		return nil, cannotRead(errors.New("not UTF-8"))
	}
	return text, nil
}

type file struct {
	bodyContains, lastUserContains []string
	reply                          Reply
}

// [LAW:parse-dont-validate] parseFile is the one checkpoint a script file crosses. What mirrors the
// Messages API is spelled as the API spells it (`content`, `stop_reason`, the block fields); the mock's
// own concepts take PROJECT.md's names. Strict throughout, so a misspelled key is rejected by name
// instead of ignored.
func parseFile(text []byte) (file, error) {
	top, err := object(text, "match", "reply")
	if err != nil {
		return file{}, err
	}
	var f file
	if raw, ok := top["match"]; ok {
		if err := f.parseMatch(raw); err != nil {
			return file{}, fmt.Errorf("match: %w", err)
		}
	}
	raw, ok := top["reply"]
	if !ok {
		return file{}, errors.New("reply: required")
	}
	if f.reply, err = ParseReply(raw); err != nil {
		return file{}, fmt.Errorf("reply: %w", err)
	}
	return f, nil
}

// Every needle must appear: bodyContains anywhere in the raw body (the JSON text as sent, so a needle can
// name a field), lastUserContains within one of the last user message's texts. An empty needle would
// match everything, so it is refused; a script with no needles answers every Messages request.
func (f *file) parseMatch(raw json.RawMessage) error {
	m, err := object(raw, "bodyContains", "lastUserContains")
	if err != nil {
		return err
	}
	// A slice, not a map: the first bad needle named is the same on every run.
	for _, field := range []struct {
		key  string
		into *[]string
	}{{"bodyContains", &f.bodyContains}, {"lastUserContains", &f.lastUserContains}} {
		if raw, ok := m[field.key]; ok {
			if *field.into, err = needles(raw); err != nil {
				return fmt.Errorf("%s: %w", field.key, err)
			}
		}
	}
	return nil
}

func needles(raw json.RawMessage) ([]string, error) {
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, errors.New("expected a list of strings")
	}
	out := []string{}
	for i, item := range list {
		s, err := str(item)
		if err == nil && s == "" {
			err = errors.New("an empty needle would match everything")
		}
		if err != nil {
			return nil, fmt.Errorf("[%d]: %w", i, err)
		}
		out = append(out, s)
	}
	return out, nil
}

// ParseReply is the one reader of a reply's JSON form, shared by a script file and a recording.
func ParseReply(raw json.RawMessage) (Reply, error) {
	var peek map[string]json.RawMessage
	if err := json.Unmarshal(raw, &peek); err != nil {
		return nil, errors.New("expected an object")
	}
	kind, err := str(peek["kind"])
	if peek["kind"] == nil {
		err = errors.New("required")
	}
	if err != nil {
		return nil, fmt.Errorf("kind: %w", err)
	}
	switch kind {
	case "message":
		return parseMessage(raw)
	case "literal":
		return parseLiteral(raw)
	}
	return nil, fmt.Errorf(`kind: expected "message" or "literal", got %q`, kind)
}

// A ceiling on one event's delay (about 24 days): a mistyped delay is an error at load, and the
// millisecond count stays far inside what time.Duration holds.
const maxDelayMs = 1<<31 - 1

func parseMessage(raw json.RawMessage) (Reply, error) {
	o, err := object(raw, "kind", "content", "stop_reason", "eventDelayMs")
	if err != nil {
		return nil, err
	}
	var list []json.RawMessage
	if json.Unmarshal(o["content"], &list) != nil || len(list) == 0 {
		return nil, errors.New("content: expected a non-empty list of blocks")
	}
	reply := MessageReply{}
	hasToolUse := false
	for i, b := range list {
		block, err := parseBlock(b)
		if err != nil {
			return nil, fmt.Errorf("content[%d]: %w", i, err)
		}
		_, isToolUse := block.(ToolUse)
		hasToolUse = hasToolUse || isToolUse
		reply.Content = append(reply.Content, block)
	}
	stop, err := str(o["stop_reason"])
	if err != nil {
		return nil, fmt.Errorf("stop_reason: %w", err)
	}
	switch reply.StopReason = StopReason(stop); reply.StopReason {
	case StopEndTurn, StopToolUse, StopMaxTokens:
	default:
		return nil, fmt.Errorf(`stop_reason: expected "end_turn", "tool_use" or "max_tokens", got %q`, stop)
	}
	// The stop reason agrees with the blocks as the API's replies do.
	if reply.StopReason != StopMaxTokens && (reply.StopReason == StopToolUse) != hasToolUse {
		return nil, errors.New(`stop_reason: "tool_use" needs a tool_use block, and "end_turn" allows none`)
	}
	if raw, ok := o["eventDelayMs"]; ok {
		var ms int64
		if err := json.Unmarshal(raw, &ms); err != nil || ms < 0 || ms > maxDelayMs {
			return nil, fmt.Errorf("eventDelayMs: expected an integer from 0 to %d", maxDelayMs)
		}
		reply.EventDelay = time.Duration(ms) * time.Millisecond
	}
	return reply, nil
}

// The API's rule for a tool's name.
var toolName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func parseBlock(raw json.RawMessage) (Block, error) {
	var peek map[string]json.RawMessage
	if err := json.Unmarshal(raw, &peek); err != nil {
		return nil, errors.New("expected an object")
	}
	kind, err := str(peek["type"])
	if err != nil {
		return nil, fmt.Errorf("type: %w", err)
	}
	switch kind {
	case "text":
		o, err := object(raw, "type", "text")
		if err != nil {
			return nil, err
		}
		text, err := str(o["text"])
		if err != nil {
			return nil, fmt.Errorf("text: %w", err)
		}
		return Text{text}, nil
	case "thinking":
		o, err := object(raw, "type", "thinking")
		if err != nil {
			return nil, err
		}
		text, err := str(o["thinking"])
		if err != nil {
			return nil, fmt.Errorf("thinking: %w", err)
		}
		return Thinking{text}, nil
	case "tool_use":
		o, err := object(raw, "type", "name", "input")
		if err != nil {
			return nil, err
		}
		name, err := str(o["name"])
		if err == nil && !toolName.MatchString(name) {
			err = errors.New("expected 1 to 64 letters, digits, underscores or hyphens")
		}
		if err != nil {
			return nil, fmt.Errorf("name: %w", err)
		}
		if _, err := object(o["input"]); err != nil {
			return nil, fmt.Errorf("input: %w", err)
		}
		return ToolUse{Name: name, Input: o["input"]}, nil
	}
	return nil, fmt.Errorf(`type: expected "text", "tool_use" or "thinking", got %q`, kind)
}

func parseLiteral(raw json.RawMessage) (Reply, error) {
	o, err := object(raw, "kind", "status", "headers", "body")
	if err != nil {
		return nil, err
	}
	var status int
	if err := json.Unmarshal(o["status"], &status); err != nil || status < 200 || status > 599 {
		return nil, errors.New("status: expected an integer from 200 to 599")
	}
	reply := LiteralReply{Status: status, Header: http.Header{}}
	if raw, ok := o["headers"]; ok {
		if err := parseHeaders(raw, reply.Header); err != nil {
			return nil, fmt.Errorf("headers: %w", err)
		}
	}
	body, err := str(o["body"])
	if err != nil {
		return nil, fmt.Errorf("body: %w", err)
	}
	// A body on a status that has none is refused; an empty one parses to no body at all.
	switch status {
	case 204, 205, 304:
		if body != "" {
			return nil, errors.New("body: a 204, 205 or 304 has no body")
		}
	default:
		reply.Body = []byte(body)
	}
	return reply, nil
}

// [LAW:parse-dont-validate] A header a response can carry, checked when the script is read so the error
// names the script. Field names are case-insensitive (RFC 9110 5.1): two keys differing only in case are
// one field written twice, which is refused rather than joined.
func parseHeaders(raw json.RawMessage, into http.Header) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return errors.New("expected an object of strings")
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	seen := map[string]bool{}
	repeated := []string{}
	for _, name := range names {
		value, err := str(fields[name])
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if !validName(name) {
			return fmt.Errorf("Invalid header name %q", name)
		}
		if !validValue(value) {
			return fmt.Errorf("%s: invalid value %q", name, value)
		}
		lower := strings.ToLower(name)
		if seen[lower] && !slices.Contains(repeated, lower) {
			repeated = append(repeated, lower)
		}
		seen[lower] = true
		into.Set(name, value)
	}
	if len(repeated) > 0 {
		return fmt.Errorf("names a field twice: %s", strings.Join(repeated, ", "))
	}
	return nil
}

// A field name is a token (RFC 9110 5.6.2).
func validName(name string) bool {
	if name == "" {
		return false
	}
	for _, c := range []byte(name) {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// A field value holds no control character but a tab (RFC 9110 5.5): a line break would split the
// field, and net/http refuses to write one.
func validValue(value string) bool {
	for _, c := range []byte(value) {
		if (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

// object is raw as one JSON object holding only the keys allowed; with none allowed, any keys.
func object(raw json.RawMessage, allowed ...string) (map[string]json.RawMessage, error) {
	var o map[string]json.RawMessage
	if raw == nil || json.Unmarshal(raw, &o) != nil || o == nil {
		return nil, errors.New("expected an object")
	}
	// The first by name, so the error is the same on every run.
	unrecognized, found := "", false
	for key := range o {
		if len(allowed) > 0 && !slices.Contains(allowed, key) && (!found || key < unrecognized) {
			unrecognized, found = key, true
		}
	}
	if found {
		return nil, fmt.Errorf("Unrecognized key: %q", unrecognized)
	}
	return o, nil
}

// str is raw as a JSON string; absent (nil) and null are not one.
func str(raw json.RawMessage) (string, error) {
	var s string
	if raw == nil {
		return "", errors.New("required")
	}
	if string(raw) == "null" || json.Unmarshal(raw, &s) != nil {
		return "", errors.New("expected a string")
	}
	return s, nil
}

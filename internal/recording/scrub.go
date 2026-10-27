package recording

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/promptctl/mock-anthropic/internal/account"
	"github.com/promptctl/mock-anthropic/internal/oauth"
)

// Kind is a kind of identifier a recording is scrubbed of: PROJECT.md "Recordings" names exactly these.
type Kind string

const (
	Token    Kind = "token"     // a credential the request carried, or one by its shape
	Email    Kind = "email"     // any email address
	UUID     Kind = "uuid"      // any UUID
	UserHash Kind = "user_hash" // the per-user hash in metadata.user_id
)

// Counts is how many strings of each kind were replaced.
type Counts map[Kind]int

// Scrubbed is an Exchange that Scrub produced and re-checked, the only thing Writer.Save accepts.
// [LAW:parse-dont-validate] The zero value holds no exchange and cannot be saved.
type Scrubbed struct{ exchange Exchange }

// minSecret is the shortest credential taken for one: a shorter string is no token, and replacing it
// everywhere would destroy the text around it.
const minSecret = 8

var (
	credentialShape = regexp.MustCompile(`sk-ant-[A-Za-z0-9_-]{10,}|eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}|(?i:bearer)\s+[A-Za-z0-9._~+/=-]{16,}`)
	emailShape      = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
	userHashShape   = regexp.MustCompile(`user_[0-9a-f]{64}`)
	hex64           = regexp.MustCompile(`^[0-9a-f]{64}$`)
	accountContext  = regexp.MustCompile(`(?i)(_account_)(` + account.UUIDPattern + `)`)
)

// syntheticHash stands in for every per-user hash.
var syntheticHash = strings.Repeat("0", 64)

// Scrub replaces, in the request and in the reply, every credential, email, UUID and per-user hash: the
// credentials the request carried (secrets) and anything shaped like one, any email address with the
// account's, the account's UUID where the text marks it, every other UUID with a synthetic one, and the
// per-user hash with a fixed one. It reads decoded JSON, so a value split by JSON escapes or nested in a
// string, as `metadata.user_id` can be, is whole when it is scanned. Then it scans its own output and
// returns an error naming the kind and place, never the value, if any of these is left.
// [LAW:single-enforcer] The one checkpoint between a raw exchange and a saved one.
func Scrub(e Exchange, secrets []string, a account.Account) (Scrubbed, Counts, error) {
	s := &scrubber{a: a, counts: Counts{}}
	for _, secret := range secrets {
		if len(secret) >= minSecret {
			s.secrets = append(s.secrets, secret)
		}
	}
	// Longest first, so a secret that holds another is replaced whole.
	slices.SortFunc(s.secrets, func(x, y string) int { return len(y) - len(x) })
	out := Exchange{MessageID: e.MessageID}
	var err error
	if out.Request, err = s.json(e.Request, ".request"); err != nil {
		return Scrubbed{}, nil, err
	}
	if out.Reply, err = s.json(e.Reply, ".reply"); err != nil {
		return Scrubbed{}, nil, err
	}
	if err := out.check(); err != nil {
		return Scrubbed{}, nil, fmt.Errorf("the scrubbed exchange is no recording: %w", err)
	}
	var leaks []string
	for _, part := range []struct {
		path string
		raw  json.RawMessage
	}{{".request", out.Request}, {".reply", out.Reply}} {
		v, err := decode(part.raw)
		if err != nil {
			return Scrubbed{}, nil, err
		}
		leaks = append(leaks, s.leaks(v, part.path)...)
	}
	if len(leaks) > 0 {
		sort.Strings(leaks)
		return Scrubbed{}, nil, fmt.Errorf("scrubbing left %s", strings.Join(leaks, ", "))
	}
	return Scrubbed{out}, s.counts, nil
}

// scrubRequest scrubs one request body as Scrub scrubs a recorded one, with no credentials of its own to
// replace (a live replay's request carries only the mock's), and re-checks the result.
func scrubRequest(raw []byte, a account.Account) ([]byte, error) {
	s := &scrubber{a: a, counts: Counts{}}
	out, err := s.json(raw, ".request")
	if err != nil {
		return nil, err
	}
	v, err := decode(out)
	if err != nil {
		return nil, err
	}
	if leaks := s.leaks(v, ".request"); len(leaks) > 0 {
		sort.Strings(leaks)
		return nil, fmt.Errorf("scrubbing left %s", strings.Join(leaks, ", "))
	}
	return out, nil
}

type scrubber struct {
	a       account.Account
	secrets []string
	counts  Counts
}

func (s *scrubber) total() int {
	n := 0
	for _, c := range s.counts {
		n += c
	}
	return n
}

func decode(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("data after the JSON value")
	}
	return v, nil
}

// encode is json.Marshal without HTML escaping, so text the exchange held reads as it was.
func encode(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

func (s *scrubber) json(raw json.RawMessage, path string) (json.RawMessage, error) {
	v, err := decode(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	v, err = s.walk(v, path)
	if err != nil {
		return nil, err
	}
	return encode(v)
}

func (s *scrubber) walk(v any, path string) (any, error) {
	switch v := v.(type) {
	case string:
		return s.text(v, path)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			var err error
			if out[i], err = s.walk(item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return nil, err
			}
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			key := s.plain(k)
			if _, dup := out[key]; dup {
				return nil, fmt.Errorf("%s: two keys scrub to the same key", path)
			}
			var err error
			if out[key], err = s.walkMember(k, item, path+"."+key); err != nil {
				return nil, err
			}
		}
		return out, nil
	}
	return v, nil
}

// walkMember handles the two members whose key says what the value is: the object form of
// metadata.user_id names the account's UUID and the per-user hash by key.
func (s *scrubber) walkMember(key string, v any, path string) (any, error) {
	if text, ok := v.(string); ok {
		switch {
		case key == "account_uuid" && text != s.a.AccountUUID():
			s.counts[UUID]++
			return s.a.AccountUUID(), nil
		case key == "device_id" && hex64.MatchString(text):
			s.counts[UserHash]++
			return syntheticHash, nil
		}
	}
	return s.walk(v, path)
}

// text scrubs one string; a string that is itself a JSON object is walked as one and written back only if
// something in it changed.
func (s *scrubber) text(t, path string) (any, error) {
	if strings.HasPrefix(strings.TrimSpace(t), "{") {
		if v, err := decode([]byte(t)); err == nil {
			if _, isObject := v.(map[string]any); isObject {
				before := s.total()
				v, err := s.walk(v, path)
				if err != nil {
					return nil, err
				}
				if s.total() == before {
					return t, nil
				}
				out, err := encode(v)
				return string(out), err
			}
		}
	}
	return s.plain(t), nil
}

func (s *scrubber) replaceAll(re *regexp.Regexp, kind Kind, t string, with func(match string) string) string {
	return re.ReplaceAllStringFunc(t, func(m string) string {
		r := with(m)
		if r != m {
			s.counts[kind]++
		}
		return r
	})
}

func (s *scrubber) plain(t string) string {
	for _, secret := range s.secrets {
		if n := strings.Count(t, secret); n > 0 {
			t = strings.ReplaceAll(t, secret, oauth.AccessToken)
			s.counts[Token] += n
		}
	}
	t = s.replaceAll(credentialShape, Token, t, func(string) string { return oauth.AccessToken })
	t = s.replaceAll(userHashShape, UserHash, t, func(string) string { return "user_" + syntheticHash })
	t = s.replaceAll(accountContext, UUID, t, func(m string) string {
		return accountContext.FindStringSubmatch(m)[1] + s.a.AccountUUID()
	})
	t = s.replaceAll(emailShape, Email, t, func(string) string { return s.a.EmailAddress() })
	return account.MapForeignUUIDs(t, s.uuid)
}

// uuid is the replacement for a UUID that is not synthetic: the account's own stay, every other is a
// synthetic UUID that is a pure function of it. [LAW:one-source-of-truth] The same UUID in a tool result
// scrubs to the same value whatever surrounds it, so a replay that scrubs a live request this way finds
// the recorded one.
func (s *scrubber) uuid(u string) string {
	l := strings.ToLower(u)
	if s.ownUUID(l) {
		return u
	}
	s.counts[UUID]++
	sum := sha256.Sum256([]byte(l))
	// 40 bits above the first 2^44 values, clear of the default account's.
	return account.SyntheticUUID(binary.BigEndian.Uint64(sum[:8])>>24 + 1<<44)
}

func (s *scrubber) ownUUID(u string) bool {
	l := strings.ToLower(u)
	return l == s.a.AccountUUID() || l == s.a.OrganizationUUID()
}

// leaks names every place v still holds one of the kinds: "<kind> at <path>".
func (s *scrubber) leaks(v any, path string) []string {
	var out []string
	switch v := v.(type) {
	case string:
		out = append(out, s.leakKinds(v, path)...)
		if strings.HasPrefix(strings.TrimSpace(v), "{") {
			if nested, err := decode([]byte(v)); err == nil {
				out = append(out, s.leaks(nested, path+"(json)")...)
			}
		}
	case []any:
		for i, item := range v {
			out = append(out, s.leaks(item, fmt.Sprintf("%s[%d]", path, i))...)
		}
	case map[string]any:
		for k, item := range v {
			out = append(out, s.leakKinds(k, path+".(key)")...)
			if text, ok := item.(string); ok && k == "device_id" && hex64.MatchString(text) && text != syntheticHash {
				out = append(out, fmt.Sprintf("%s at %s.%s", UserHash, path, k))
			}
			out = append(out, s.leaks(item, path+"."+k)...)
		}
	}
	return out
}

func (s *scrubber) leakKinds(t, path string) []string {
	var out []string
	at := func(k Kind) { out = append(out, fmt.Sprintf("%s at %s", k, path)) }
	if slices.ContainsFunc(s.secrets, func(secret string) bool { return strings.Contains(t, secret) }) {
		at(Token)
	} else if credentialShape.MatchString(t) {
		at(Token)
	}
	for _, m := range emailShape.FindAllString(t, -1) {
		if m != s.a.EmailAddress() {
			at(Email)
			break
		}
	}
	for _, off := range account.ForeignUUIDs([]byte(t)) {
		if !s.ownUUID(t[off : off+36]) {
			at(UUID)
			break
		}
	}
	for _, m := range userHashShape.FindAllString(t, -1) {
		if m != "user_"+syntheticHash {
			at(UserHash)
			break
		}
	}
	return out
}

// Package account is the identity the mock logs Claude Code in as (PROJECT.md "The account"). Every
// response that carries identity is derived from one Account.
package account

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"os"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// [LAW:one-source-of-truth] The one declaration of what a synthetic UUID looks like: built by
// SyntheticUUID, recognized by IsSyntheticUUID. The prefix fixes 74 of a random v4 UUID's 122 random
// bits, so a real UUID matches it with probability 2^-74.
const (
	syntheticPrefix = "00000000-0000-4000-8000-"
	syntheticDigits = 12
)

var syntheticForm = regexp.MustCompile(fmt.Sprintf(`^%s[0-9a-f]{%d}$`, syntheticPrefix, syntheticDigits))

// SyntheticUUID is the nth synthetic UUID. n must be below 16^12; a larger n is a programming error.
func SyntheticUUID(n uint64) string {
	if n >= 1<<(4*syntheticDigits) {
		panic(fmt.Sprintf("SyntheticUUID: %d is not below 16^%d", n, syntheticDigits))
	}
	return fmt.Sprintf("%s%0*x", syntheticPrefix, syntheticDigits, n)
}

// IsSyntheticUUID reports whether s is exactly a UUID SyntheticUUID builds.
func IsSyntheticUUID(s string) bool { return syntheticForm.MatchString(s) }

// SubscriptionType is the plan the account is on.
type SubscriptionType string

const (
	Pro        SubscriptionType = "pro"
	Max        SubscriptionType = "max"
	Team       SubscriptionType = "team"
	Enterprise SubscriptionType = "enterprise"
)

var subscriptionTypes = []SubscriptionType{Pro, Max, Team, Enterprise}

type fields struct {
	accountUUID, emailAddress, organizationUUID, organizationName, subscriptionType string
}

// [LAW:parse-dont-validate] An Account exists only as Parse's output. It shares one frozen fields value
// across copies; the zero Account holds none, so using one panics instead of reading as an empty identity.
// Not comparable: == would compare which Parse made it, not the account.
type Account struct {
	f *fields
	_ [0]func()
}

func (a Account) AccountUUID() string                { return a.f.accountUUID }
func (a Account) EmailAddress() string               { return a.f.emailAddress }
func (a Account) OrganizationUUID() string           { return a.f.organizationUUID }
func (a Account) OrganizationName() string           { return a.f.organizationName }
func (a Account) SubscriptionType() SubscriptionType { return SubscriptionType(a.f.subscriptionType) }

type schemaField struct {
	key   string
	at    func(*fields) *string
	check func(string) string // the reason s is wrong, or ""
}

// [LAW:one-source-of-truth] [LAW:domain-language] The five facts PROJECT.md names, under the names
// Claude Code gives them in its `oauthAccount` config block and its credential, so `login` and the generators copy them
// across unrenamed. Parse reads and MarshalJSON writes exactly these keys, in this order.
var schema = []schemaField{
	{"accountUuid", func(f *fields) *string { return &f.accountUUID }, checkUUID},
	{"emailAddress", func(f *fields) *string { return &f.emailAddress }, checkEmail},
	{"organizationUuid", func(f *fields) *string { return &f.organizationUUID }, checkUUID},
	{"organizationName", func(f *fields) *string { return &f.organizationName }, func(s string) string {
		if strings.TrimSpace(s) == "" {
			return "blank"
		}
		return ""
	}},
	{"subscriptionType", func(f *fields) *string { return &f.subscriptionType }, func(s string) string {
		for _, known := range subscriptionTypes {
			if SubscriptionType(s) == known {
				return ""
			}
		}
		return fmt.Sprintf("not one of %v", subscriptionTypes)
	}},
}

// MarshalJSON writes the account in the form Parse reads.
func (a Account) MarshalJSON() ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, field := range schema {
		if i > 0 {
			out.WriteByte(',')
		}
		key, _ := json.Marshal(field.key)
		value, _ := json.Marshal(*field.at(a.f))
		out.Write(key)
		out.WriteByte(':')
		out.Write(value)
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

// InputError is every way an account input can fail (unreadable, not JSON, schema). A bad
// `--account FILE` is a wrong command line: the command reading it answers with a command.UsageError.
type InputError struct {
	Origin string
	Reason string
}

func (e *InputError) Error() string { return fmt.Sprintf("account from %s: %s", e.Origin, e.Reason) }

// Lower case, as Claude Code writes them and as IsSyntheticUUID recognizes them; RFC 9562 version and
// variant, so the nil UUID is refused.
var uuidForm = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func checkUUID(s string) string {
	if !uuidForm.MatchString(s) {
		return "not a lower-case RFC 9562 UUID"
	}
	return ""
}

// A DNS name of two or more labels, each at most 63 characters, the last holding a letter, so an IP
// address is not taken for one.
var hostname = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

const maxHostname = 253

// checkText refuses what json.Unmarshal would hide: invalid UTF-8 and lone surrogate escapes both
// decode silently to U+FFFD, so the account written back would not be the one the file holds.
func checkText(s string) string {
	if strings.ContainsRune(s, utf8.RuneError) {
		return "not valid UTF-8 (holds U+FFFD)"
	}
	if strings.ContainsFunc(s, unicode.IsControl) {
		return "contains a control character"
	}
	return ""
}

func checkEmail(s string) string {
	address, err := mail.ParseAddress(s)
	if err != nil || address.Address != s {
		return "not a bare email address"
	}
	if domain := s[strings.LastIndexByte(s, '@')+1:]; len(domain) > maxHostname || !hostname.MatchString(domain) ||
		!strings.ContainsFunc(domain[strings.LastIndexByte(domain, '.')+1:], unicode.IsLetter) {
		return "domain is not a dotted host name"
	}
	return ""
}

// Parse is the one checkpoint an account crosses [LAW:single-enforcer]. origin says where data came
// from, so the error names both the source and every bad key. Keys match exactly: encoding/json's
// case-insensitive, last-duplicate-wins struct decoding would accept keys `login` reads differently.
func Parse(data []byte, origin string) (Account, error) {
	var problems []string
	failed := func() (Account, error) {
		return Account{}, &InputError{origin, strings.Join(problems, "; ")}
	}
	// A syntax error ends the walk; the problems found before it are still reported.
	notJSON := func(err error) (Account, error) {
		switch {
		case len(bytes.TrimSpace(data)) == 0:
			problems = append(problems, "empty")
		case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
			problems = append(problems, "not JSON: ends before the object closes")
		default:
			problems = append(problems, "not JSON: "+err.Error())
		}
		return failed()
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	open, err := decoder.Token()
	if err != nil {
		return notJSON(err)
	}
	if open != json.Delim('{') {
		problems = append(problems, "not a JSON object, got "+quoteJSON(data))
		return failed()
	}

	var f fields
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return notJSON(err)
		}
		key := token.(string) // inside an object, More() guarantees a key
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return notJSON(err)
		}
		i := slices.IndexFunc(schema, func(field schemaField) bool { return field.key == key })
		switch {
		case i < 0:
			problems = append(problems, fmt.Sprintf("%q: unknown key", key))
			continue
		case seen[key]:
			problems = append(problems, key+": given more than once")
			continue
		}
		seen[key] = true
		var s string
		if string(value) == "null" || json.Unmarshal(value, &s) != nil {
			problems = append(problems, fmt.Sprintf("%s: not a string, got %s", key, quoteJSON(value)))
			continue
		}
		why := checkText(s)
		if why == "" {
			why = schema[i].check(s)
		}
		if why != "" {
			problems = append(problems, fmt.Sprintf("%s: %s, got %q", key, why, s))
		}
		*schema[i].at(&f) = s
	}
	if _, err := decoder.Token(); err != nil { // the closing brace
		return notJSON(err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		problems = append(problems, "data after the object")
	}
	for _, field := range schema {
		if !seen[field.key] {
			problems = append(problems, field.key+": missing")
		}
	}
	if len(problems) > 0 {
		return failed()
	}
	return Account{f: &f}, nil
}

// quoteJSON quotes a JSON value for an error, cut to a length a terminal line holds.
func quoteJSON(value []byte) string {
	const limit = 40
	if text := bytes.TrimSpace(value); len(text) > limit {
		return fmt.Sprintf("%q...", text[:limit])
	}
	return fmt.Sprintf("%q", bytes.TrimSpace(value))
}

// [LAW:effects-at-boundaries] ReadFile is the consumer override: a JSON file holding a complete
// account, which replaces the default whole.
func ReadFile(path string) (Account, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			err = pathErr.Err
		}
		return Account{}, &InputError{path, "cannot read: " + err.Error()}
	}
	return Parse(data, path)
}

// Default is the account the mock uses when no override is given; every identifier in it is synthetic.
func Default() Account { return defaultAccount }

var defaultAccount = mustParse(`{
	"accountUuid": "` + SyntheticUUID(1) + `",
	"emailAddress": "user@mock-anthropic.invalid",
	"organizationUuid": "` + SyntheticUUID(2) + `",
	"organizationName": "Mock Organization",
	"subscriptionType": "max"
}`)

func mustParse(data string) Account {
	a, err := Parse([]byte(data), "Default")
	if err != nil {
		panic(err)
	}
	return a
}

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
	"strings"
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

// [LAW:domain-language] The five facts PROJECT.md names, under the names Claude Code gives them in its
// `oauthAccount` config block, so `login` and the generators copy them across unrenamed.
type fields struct {
	AccountUUID      string           `json:"accountUuid"`
	EmailAddress     string           `json:"emailAddress"`
	OrganizationUUID string           `json:"organizationUuid"`
	OrganizationName string           `json:"organizationName"`
	SubscriptionType SubscriptionType `json:"subscriptionType"`
}

// [LAW:parse-dont-validate] An Account exists only as Parse's output: its fields are unexported, so no
// literal outside this package can pose as one, and a value copy cannot change anyone else's.
type Account struct{ f fields }

func (a Account) AccountUUID() string                { return a.f.AccountUUID }
func (a Account) EmailAddress() string               { return a.f.EmailAddress }
func (a Account) OrganizationUUID() string           { return a.f.OrganizationUUID }
func (a Account) OrganizationName() string           { return a.f.OrganizationName }
func (a Account) SubscriptionType() SubscriptionType { return a.f.SubscriptionType }

// MarshalJSON writes the account in the form Parse reads.
func (a Account) MarshalJSON() ([]byte, error) { return json.Marshal(a.f) }

// InputError is every way an account input can fail (unreadable, not JSON, schema), so a command maps
// a bad `--account FILE` to exit 2 by type.
type InputError struct {
	Origin string
	Reason string
}

func (e *InputError) Error() string { return fmt.Sprintf("account from %s: %s", e.Origin, e.Reason) }

var uuidForm = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Parse is the one checkpoint an account crosses [LAW:single-enforcer]. origin says where data came
// from, so the error names both the source and every bad field.
func Parse(data []byte, origin string) (Account, error) {
	// Pointers tell a missing field from an empty one.
	var raw struct {
		AccountUUID      *string `json:"accountUuid"`
		EmailAddress     *string `json:"emailAddress"`
		OrganizationUUID *string `json:"organizationUuid"`
		OrganizationName *string `json:"organizationName"`
		SubscriptionType *string `json:"subscriptionType"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields() // a misspelled key is rejected by name instead of ignored
	if err := decoder.Decode(&raw); err != nil {
		return Account{}, &InputError{origin, "not a JSON account: " + err.Error()}
	}
	if _, err := decoder.Token(); err != io.EOF {
		return Account{}, &InputError{origin, "not a JSON account: data after the object"}
	}

	var problems []string
	field := func(name string, value *string, check func(string) string) string {
		if value == nil {
			problems = append(problems, name+": missing")
			return ""
		}
		if why := check(*value); why != "" {
			problems = append(problems, fmt.Sprintf("%s: %s, got %q", name, why, *value))
		}
		return *value
	}
	isUUID := func(s string) string {
		if !uuidForm.MatchString(s) {
			return "not a UUID"
		}
		return ""
	}
	f := fields{
		AccountUUID: field("accountUuid", raw.AccountUUID, isUUID),
		EmailAddress: field("emailAddress", raw.EmailAddress, func(s string) string {
			if address, err := mail.ParseAddress(s); err != nil || address.Address != s {
				return "not a bare email address"
			}
			return ""
		}),
		OrganizationUUID: field("organizationUuid", raw.OrganizationUUID, isUUID),
		OrganizationName: field("organizationName", raw.OrganizationName, func(s string) string {
			if strings.TrimSpace(s) == "" {
				return "blank"
			}
			return ""
		}),
		SubscriptionType: SubscriptionType(field("subscriptionType", raw.SubscriptionType, func(s string) string {
			for _, known := range subscriptionTypes {
				if SubscriptionType(s) == known {
					return ""
				}
			}
			return fmt.Sprintf("not one of %v", subscriptionTypes)
		})),
	}
	if len(problems) > 0 {
		return Account{}, &InputError{origin, strings.Join(problems, "; ")}
	}
	return Account{f}, nil
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
var Default = mustParse(fields{
	AccountUUID:      SyntheticUUID(1),
	EmailAddress:     "user@mock-anthropic.invalid",
	OrganizationUUID: SyntheticUUID(2),
	OrganizationName: "Mock Organization",
	SubscriptionType: Max,
}, "Default")

func mustParse(f fields, origin string) Account {
	data, err := json.Marshal(f)
	if err != nil {
		panic(err)
	}
	a, err := Parse(data, origin)
	if err != nil {
		panic(err)
	}
	return a
}

package account

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func otherAccount() map[string]any {
	return map[string]any{
		"accountUuid":      SyntheticUUID(3),
		"emailAddress":     "other@mock-anthropic.invalid",
		"organizationUuid": SyntheticUUID(4),
		"organizationName": "Other Organization",
		"subscriptionType": "pro",
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "account.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeJSON(t *testing.T, value map[string]any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return writeFile(t, string(data))
}

func TestSyntheticUUIDsAreRecognized(t *testing.T) {
	for _, n := range []uint64{0, 1, 0xabc, 1<<48 - 1} {
		uuid := SyntheticUUID(n)
		if !uuidForm.MatchString(uuid) || !IsSyntheticUUID(uuid) {
			t.Errorf("SyntheticUUID(%d) = %q is not a recognized synthetic UUID", n, uuid)
		}
	}
	if got := SyntheticUUID(1); got != "00000000-0000-4000-8000-000000000001" {
		t.Errorf("SyntheticUUID(1) = %q", got)
	}
}

func TestSyntheticUUIDPanicsOutsideTheForm(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("SyntheticUUID(16^12) did not panic")
		}
	}()
	SyntheticUUID(1 << 48)
}

func TestSyntheticFormRejectsOtherUUIDs(t *testing.T) {
	for _, s := range []string{
		"1" + SyntheticUUID(1)[1:], // a UUID outside the synthetic prefix
		strings.ToUpper(SyntheticUUID(0xabc)),
		"00000000-0000-4000-8000-00000000001",
		" " + SyntheticUUID(1),
	} {
		if IsSyntheticUUID(s) {
			t.Errorf("IsSyntheticUUID(%q) = true", s)
		}
	}
}

func TestCopiesAndValuesMarshalTheAccount(t *testing.T) {
	data, err := json.Marshal(struct{ A Account }{Default()})
	if err != nil || !strings.Contains(string(data), Default().AccountUUID()) {
		t.Errorf("an Account held by value marshals as %s, %v", data, err)
	}
}

func TestTheZeroAccountPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("reading the zero Account did not panic")
		}
	}()
	_ = Account{}.AccountUUID()
}

func TestDefaultParsesAndIsSynthetic(t *testing.T) {
	data, err := json.Marshal(Default())
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(data, "a copy of the default")
	if err != nil || *again.f != *Default().f {
		t.Fatalf("the default does not round-trip: %v, %v", again, err)
	}
	if !IsSyntheticUUID(Default().AccountUUID()) || !IsSyntheticUUID(Default().OrganizationUUID()) {
		t.Errorf("default UUIDs are not synthetic: %s", data)
	}
	if !strings.HasSuffix(Default().EmailAddress(), ".invalid") {
		t.Errorf("default email %q is not under .invalid", Default().EmailAddress())
	}
}

func TestFileReplacesTheDefaultWithExactlyItsAccount(t *testing.T) {
	want := otherAccount()
	got, err := ReadFile(writeJSON(t, want))
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(got)
	var back map[string]any
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	for k, v := range want {
		if back[k] != v {
			t.Errorf("%s = %v, want %v", k, back[k], v)
		}
	}
	if len(back) != len(want) {
		t.Errorf("account has keys %v, want exactly %v", back, want)
	}
}

func TestBadFilesAreRejectedNamingFileAndField(t *testing.T) {
	with := func(key string, value any) map[string]any {
		a := otherAccount()
		if value == nil {
			delete(a, key)
		} else {
			a[key] = value
		}
		return a
	}
	dir := t.TempDir()
	cases := []struct {
		name  string
		path  string
		names []string // every one must appear in the error, after "account from <path>: "
	}{
		{"bad uuid", writeJSON(t, with("organizationUuid", "nope")), []string{"organizationUuid"}},
		{"missing field", writeJSON(t, with("emailAddress", nil)), []string{"emailAddress", "missing"}},
		{"unknown key", writeJSON(t, with("acountUuid", SyntheticUUID(5))), []string{"acountUuid"}},
		{"blank organization name", writeJSON(t, with("organizationName", " ")), []string{"organizationName"}},
		{"email with display name", writeJSON(t, with("emailAddress", "A <a@b.invalid>")), []string{"emailAddress"}},
		{"unknown subscription", writeJSON(t, with("subscriptionType", "free")), []string{"subscriptionType"}},
		{"wrong JSON type", writeJSON(t, with("organizationName", 7)), []string{"organizationName", "not a string"}},
		{"null value", writeJSON(t, with("accountUuid", json.RawMessage("null"))), []string{"accountUuid", "not a string"}},
		{"wrong-case key", writeJSON(t, with("accountuuid", SyntheticUUID(5))), []string{`"accountuuid": unknown key`}},
		{"duplicate key", writeFile(t, `{"accountUuid":"`+SyntheticUUID(3)+`","accountUuid":"`+SyntheticUUID(5)+`"}`), []string{"accountUuid: given more than once"}},
		{"nil uuid", writeJSON(t, with("organizationUuid", strings.Replace(SyntheticUUID(0), "-4000-8000-", "-0000-0000-", 1))), []string{"organizationUuid"}},
		{"upper-case uuid", writeJSON(t, with("accountUuid", strings.ToUpper(SyntheticUUID(0xabc)))), []string{"accountUuid"}},
		{"undotted email domain", writeJSON(t, with("emailAddress", "a@b")), []string{"emailAddress"}},
		{"domain literal email", writeJSON(t, with("emailAddress", "a@[1.2.3.4]")), []string{"emailAddress"}},
		{"bad host label email", writeJSON(t, with("emailAddress", "a@b.-c")), []string{"emailAddress"}},
		{"control character", writeJSON(t, with("organizationName", "Org\x00")), []string{"organizationName", "control character"}},
		{"invalid UTF-8", writeFile(t, "{\"organizationName\":\"Org\xff\"}"), []string{"organizationName", "UTF-8"}},
		{"trailing data keeps field problems", writeFile(t, `{"accountUuid":"x"} {}`), []string{"data after the object", "accountUuid: not", "emailAddress: missing"}},
		{"lone surrogate escape", writeFile(t, `{"organizationName":"Org\ud800"}`), []string{"organizationName", "UTF-8"}},
		{"ip address email", writeJSON(t, with("emailAddress", "a@1.2.3.4")), []string{"emailAddress"}},
		{"overlong label email", writeJSON(t, with("emailAddress", "a@"+strings.Repeat("b", 64)+".invalid")), []string{"emailAddress"}},
		{"truncated keeps field problems", writeFile(t, `{"accountUuid":"x"`), []string{"accountUuid: not", "ends before the object closes"}},
		{"empty", writeFile(t, ""), []string{"empty"}},
		{"bounded raw echo", writeFile(t, `{"organizationName":`+strings.Repeat("7", 500)+`}`), []string{"organizationName: not a string, got \"" + strings.Repeat("7", 40) + "\"..."}},
		{"not an object", writeFile(t, "null"), []string{"not a JSON object"}},
		{"not JSON", writeFile(t, "{ not json"), []string{"not JSON"}},
		{"absent file", filepath.Join(dir, "absent.json"), []string{"cannot read"}},
		{"a directory", dir, []string{"cannot read"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ReadFile(c.path)
			var inputErr *InputError
			if !errors.As(err, &inputErr) {
				t.Fatalf("got %v, want an *InputError", err)
			}
			prefix := "account from " + c.path + ": "
			if !strings.HasPrefix(err.Error(), prefix) {
				t.Errorf("%q does not open with %q", err, prefix)
			}
			for _, name := range c.names {
				if !strings.Contains(err.Error(), name) {
					t.Errorf("%q does not name %q", err, name)
				}
			}
		})
	}
}

func TestEveryBadKeyIsNamedAtOnce(t *testing.T) {
	a := otherAccount()
	delete(a, "emailAddress")
	a["accountUuid"], a["subscriptionType"], a["acountUuid"], a["organizationName"] = "x", "free", "x", 7
	got, err := ReadFile(writeJSON(t, a))
	if got.f != nil {
		t.Errorf("a rejected file returned account %v, want the zero Account", got)
	}
	for _, name := range []string{"accountUuid", "subscriptionType", `"acountUuid"`, "organizationName", "emailAddress: missing"} {
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("got %v, want %s named", err, name)
		}
	}
}

// Foreign UUIDs are built at run time: a literal one would fail the repo-safety test.
func TestForeignUUIDsFindsEveryOtherUUIDShape(t *testing.T) {
	synthetic := SyntheticUUID(7)
	otherVersion := strings.Replace(synthetic, "-4000-", "-1000-", 1)
	upper := strings.ToUpper(SyntheticUUID(0xabc))
	embedded := "f" + synthetic[1:]
	data := []byte(synthetic + " " + otherVersion + "\n\"" + upper + "\" id=" + embedded + "x")
	got := ForeignUUIDs(data)
	want := []string{otherVersion, upper, embedded}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ForeignUUIDs = %q, want %q", got, want)
	}
	if got := ForeignUUIDs([]byte(synthetic + SyntheticUUID(1))); got != nil {
		t.Errorf("synthetic UUIDs reported as foreign: %q", got)
	}
}

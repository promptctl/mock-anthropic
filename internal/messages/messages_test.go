package messages

import (
	"strings"
	"testing"
)

func TestTokensCountsUTF16UnitsAtFourToAToken(t *testing.T) {
	for text, want := range map[string]int{"": 0, "abcd": 1, "abcde": 2, "😀😀": 1, "😀😀a": 2} {
		if got := tokens(text); got != want {
			t.Errorf("tokens(%q) = %d, want %d", text, got, want)
		}
	}
}

func TestParseCountTokensRefusesWhatTheAPIRefusesAndSaysWhy(t *testing.T) {
	for body, why := range map[string]string{
		`{"model":"m","messages":[{"role":"user","content":"x"}]}}`: "body is not JSON",
		`{"model":"m","messages":[{"role":"user","content":null}]}`: "content",
		`null`: "expected an object, got null",
		`[1]`:  "expected an object, got an array",
	} {
		_, err := ParseCountTokens([]byte(body))
		if err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%s: err %v, want one containing %q", body, err, why)
		}
	}
}

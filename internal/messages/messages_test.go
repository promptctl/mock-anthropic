package messages

import "testing"

func TestTokensCountsUTF16UnitsAtFourToAToken(t *testing.T) {
	for text, want := range map[string]int{"": 0, "abcd": 1, "abcde": 2, "😀😀": 1, "😀😀a": 2} {
		if got := tokens(text); got != want {
			t.Errorf("tokens(%q) = %d, want %d", text, got, want)
		}
	}
}

func TestParseCountTokensRefusesWhatTheAPIRefuses(t *testing.T) {
	for name, body := range map[string]string{
		"trailing closer": `{"model":"m","messages":[{"role":"user","content":"x"}]}}`,
		"null content":    `{"model":"m","messages":[{"role":"user","content":null}]}`,
		"null body":       `null`,
	} {
		if _, err := ParseCountTokens([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

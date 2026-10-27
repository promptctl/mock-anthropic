package coverage

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/promptctl/mock-anthropic/internal/command"
	"github.com/promptctl/mock-anthropic/internal/endpoint"
	"github.com/promptctl/mock-anthropic/internal/generator"
)

func answer(context.Context, map[string]string, generator.Request) (generator.Answer, error) {
	return generator.Answer{}, nil
}

func run(t *testing.T, rows []endpoint.Row, args ...string) (command.Result, string, error) {
	t.Helper()
	table, err := endpoint.Build(rows)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	res, err := Over(table, &out).Run(context.Background(), args, func(error) {}, func(command.Facts) {})
	return res, out.String(), err
}

func TestAnEndpointWithNoGeneratorIsAGap(t *testing.T) {
	res, out, err := run(t, []endpoint.Row{
		endpoint.InScope("a", "api.anthropic.com", "GET", "/a", answer),
		endpoint.InScope("b", "api.anthropic.com", "GET", "/b", nil),
		endpoint.OutOfScope("c", "api.anthropic.com", "GET", "/c", "because"),
	})
	if err != nil || res.Answer != command.No {
		t.Fatalf("answer %v, err %v; want no", res.Answer, err)
	}
	if got := res.Facts["gaps"].([]string); !slices.Equal(got, []string{"b"}) {
		t.Errorf("gaps %v", got)
	}
	for _, want := range []string{"ANSWERED", "GAP", "OUT OF SCOPE", "GET api.anthropic.com/c  (c): because"} {
		if !strings.Contains(out, want) {
			t.Errorf("listing lacks %q:\n%s", want, out)
		}
	}
	if res.Facts["answered"] != 1 || res.Facts["out_of_scope"] != 1 {
		t.Errorf("facts %v", res.Facts)
	}
}

func TestEveryInScopeEndpointAnsweredIsYes(t *testing.T) {
	res, _, err := run(t, []endpoint.Row{
		endpoint.InScope("a", "api.anthropic.com", "GET", "/a", answer),
		endpoint.OutOfScope("c", "api.anthropic.com", "GET", "/c", "because"),
	})
	if err != nil || res.Answer != command.OK {
		t.Fatalf("answer %v, err %v; want ok", res.Answer, err)
	}
}

func TestAnArgumentIsAWrongCommandLine(t *testing.T) {
	_, _, err := run(t, nil, "extra")
	var usage *command.UsageError
	if err == nil || !errors.As(err, &usage) {
		t.Fatalf("err %v, want a UsageError", err)
	}
}

// The real table: until the Messages generator lands (mock mock-flu.coa.c0w), its row is the one gap.
// Then this becomes the PROJECT.md acceptance: coverage exits 0.
func TestRealTableGapsAreOnlyTheMessagesRow(t *testing.T) {
	res, err := Command(new(strings.Builder)).Run(context.Background(), nil, func(error) {}, func(command.Facts) {})
	if err != nil {
		t.Fatal(err)
	}
	gaps := res.Facts["gaps"].([]string)
	if want := []string{"messages"}; !slices.Equal(gaps, want) {
		t.Errorf("gaps %v, want %v", gaps, want)
	}
}

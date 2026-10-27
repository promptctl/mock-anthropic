package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/promptctl/mock-anthropic/internal/command"
	"github.com/promptctl/mock-anthropic/internal/server"
)

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("stderr is gone") }

func TestSinkReportsAFailedWriteAsACrash(t *testing.T) {
	var crashed []error
	Sink(command.NewLog(brokenWriter{}), func(err error) { crashed = append(crashed, err) })(server.ConnectionEvent{Event: "connection", Outcome: server.Closed})
	if len(crashed) != 1 || !strings.Contains(crashed[0].Error(), "stderr is gone") {
		t.Errorf("crashes %v, want the write's error", crashed)
	}
}

func TestSinkIsQuietAfterTheRecord(t *testing.T) {
	var out strings.Builder
	log := command.NewLog(&out)
	command.Main(command.Commands{}, nil, log, nil)
	Sink(log, func(err error) { t.Errorf("crash: %v", err) })(server.ConnectionEvent{Event: "connection", Outcome: server.Closed})
	if strings.Contains(out.String(), "CLOSED") {
		t.Errorf("a record followed the invocation's: %q", out.String())
	}
}

func TestSinkWritesOneLinePerRecord(t *testing.T) {
	var out strings.Builder
	sink := Sink(command.NewLog(&out), func(err error) { t.Errorf("crash: %v", err) })
	sink(server.ConnectionEvent{Event: "connection", Outcome: server.Refused})
	sink(server.ConnectionEvent{Event: "connection", Outcome: server.Closed})
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "REFUSED {") || !strings.HasPrefix(lines[1], "CLOSED {") {
		t.Errorf("lines %q", lines)
	}
}

func TestServeAnswersAsTheAccountItWasGiven(t *testing.T) {
	file := filepath.Join(t.TempDir(), "account.json")
	const org = "00000000-0000-4000-8000-0000000000bb"
	err := os.WriteFile(file, []byte(`{"accountUuid":"00000000-0000-4000-8000-0000000000aa","emailAddress":"given@example.invalid",`+
		`"organizationUuid":"`+org+`","organizationName":"Given Org","subscriptionType":"pro"}`), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := Command(write, command.NewLog(&stderr)).Run(ctx, []string{"--port", "0", "--account", file}, func(err error) { t.Errorf("crash: %v", err) }, func(command.Facts) {})
		write.Close()
		done <- err
	}()
	var ready struct{ Port int }
	if err := json.NewDecoder(read).Decode(&ready); err != nil {
		stop()
		t.Fatalf("no ready line: %v (%s)", err, stderr.String())
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(fmt.Sprintf("http://127.0.0.1:%d/api/oauth/organizations/%s/plugins/list-plugins", ready.Port, org))
	if err != nil {
		stop()
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	stop()
	if err := <-done; err != nil {
		t.Errorf("serve ended with %v", err)
	}
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"plugins":[]`) {
		t.Errorf("%d %s: the given account's organization was not answered", resp.StatusCode, body)
	}
}

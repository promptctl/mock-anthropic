package serve

import (
	"errors"
	"strings"
	"testing"

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

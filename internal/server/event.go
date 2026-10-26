package server

import (
	"encoding/json"
	"strings"

	"github.com/promptctl/mock-anthropic/internal/dispatch"
	"github.com/promptctl/mock-anthropic/internal/endpoint"
	"github.com/promptctl/mock-anthropic/internal/generator"
)

// [LAW:nothing-unseen] One record per unit of work: each request and each connection, the plain ones,
// the tunnels and the refused ones alike.

// Event is a RequestEvent or a ConnectionEvent.
type Event interface{ outcome() string }

// RequestOutcome is how a request ended: its verdict, or Aborted when its connection ended first.
type RequestOutcome string

const (
	RequestServed RequestOutcome = RequestOutcome(dispatch.Served)
	Unmatched     RequestOutcome = RequestOutcome(dispatch.Unmatched)
	RequestFailed RequestOutcome = RequestOutcome(dispatch.Failed)
	Aborted       RequestOutcome = "aborted"
)

// RequestEvent is a request's record. Tunnel is the id of the tunnel that carried it, nil for one sent
// straight to the port. Endpoint is nil when no endpoint matched. Status is nil when the client left
// before a response existed. Facts are what the handler decided, nested so none can stand in for a
// field of the record's own; empty when nothing was decided.
type RequestEvent struct {
	Event      string          `json:"event"`
	Tunnel     *int64          `json:"tunnel"`
	Host       string          `json:"host"`
	Method     string          `json:"method"`
	Path       string          `json:"path"`
	Query      string          `json:"query"`
	Endpoint   *endpoint.ID    `json:"endpoint"`
	Status     *int            `json:"status"`
	DurationMS int64           `json:"duration_ms"`
	Facts      generator.Facts `json:"facts"`
	Outcome    RequestOutcome  `json:"outcome"`
	Why        string          `json:"why,omitempty"`
}

func (e RequestEvent) outcome() string { return string(e.Outcome) }

// ConnectionOutcome is how a connection ended.
type ConnectionOutcome string

const (
	Closed           ConnectionOutcome = "closed"
	Refused          ConnectionOutcome = "refused"
	ConnectionFailed ConnectionOutcome = "failed"
)

// ClosedBy is who ended a connection: the client, the mock's Stop, or the mock's own server (net/http
// rejecting a malformed request, or the mock cutting a failed body).
type ClosedBy string

const (
	ByClient ClosedBy = "client"
	ByStop   ClosedBy = "stop"
	ByServer ClosedBy = "server"
)

// ConnectionEvent is a connection's record. Host is the authority its requests are addressed to, and
// Port the port its CONNECT named: nil for a plain connection, and for a CONNECT whose target had
// none. A connection that was served says how many requests reached the mock, how long it lived, and
// who closed it; a refused one has none of those.
type ConnectionEvent struct {
	Event      string            `json:"event"`
	Tunnel     *int64            `json:"tunnel"`
	Host       string            `json:"host"`
	Port       *int              `json:"port"`
	Requests   *int64            `json:"requests,omitempty"`
	DurationMS *int64            `json:"duration_ms,omitempty"`
	ClosedBy   *ClosedBy         `json:"closed_by,omitempty"`
	Outcome    ConnectionOutcome `json:"outcome"`
	Why        string            `json:"why,omitempty"`
}

func (e ConnectionEvent) outcome() string { return string(e.Outcome) }

// Line is an event as the line it is written as on stderr: its outcome in capitals, then the record as
// JSON, the form the invocation's own record takes.
func Line(e Event) (string, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	return strings.ToUpper(e.outcome()) + " " + string(data), nil
}

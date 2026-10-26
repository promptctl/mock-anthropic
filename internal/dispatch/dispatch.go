// Package dispatch is PROJECT.md "Failure behavior" for one resolved request: the response that
// answers it and the verdict on it. Every request the server receives, plain or tunneled, takes this
// one path, unless a consumer such as record mode hands the server a Handler of its own.
package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/promptctl/mock-anthropic/internal/endpoint"
	"github.com/promptctl/mock-anthropic/internal/generator"
)

// Outcome is a request's verdict before its response is delivered.
type Outcome string

const (
	Served    Outcome = "served"
	Unmatched Outcome = "unmatched"
	Failed    Outcome = "failed"
)

// Verdict is how a request was answered; Why is empty exactly when it was Served.
type Verdict struct {
	Outcome Outcome
	Why     string
}

// Dispatched is a request's verdict, the response that verdict implies, and what the generator
// decided. The facts sit beside the verdict, not in it: they describe the answer, and stay true of it
// when its body later fails or is abandoned and the record's outcome changes.
type Dispatched struct {
	Verdict  Verdict
	Response generator.Response
	Facts    generator.Facts
}

// Handler answers one request the server has resolved: miss is the resolver's *endpoint.Miss, or nil
// when resolution names the endpoint. [LAW:one-source-of-truth] The server resolves once, so the
// endpoint its record names is the one the handler was given.
type Handler func(ctx context.Context, resolution endpoint.Resolution, miss error, r generator.Request) Dispatched

// Dispatch is the Handler that answers from the table: the endpoint's generator, or the table's
// refusal. [LAW:dataflow-not-control-flow] Every request gets one verdict and the response it implies;
// only the values differ.
func Dispatch(ctx context.Context, resolution endpoint.Resolution, miss error, r generator.Request) Dispatched {
	answer, err := decide(ctx, resolution, miss, r)
	if err != nil {
		// [LAW:no-silent-failure] A generator that fails is the mock's own bug: a 500 that names it.
		return Dispatched{Verdict{Failed, err.Error()}, ErrorResponse(Failure, r, err.Error()), generator.Facts{}}
	}
	switch reply := answer.Reply.(type) {
	case generator.Response:
		// [LAW:single-enforcer] A response the server cannot write is the generator's bug, caught here,
		// before the server commits a status, as a 500 that names it.
		if why := unwritable(reply); why != "" {
			why = fmt.Sprintf("generator %s gave %s", resolution.Endpoint.ID(), why)
			return Dispatched{Verdict{Failed, why}, ErrorResponse(Failure, r, why), answer.Facts}
		}
		return Dispatched{Verdict{Outcome: Served}, reply, answer.Facts}
	case generator.Declined:
		return Dispatched{Verdict{Unmatched, reply.Why}, ErrorResponse(Refusal, r, reply.Why), answer.Facts}
	default:
		why := fmt.Sprintf("generator gave no reply (%T)", answer.Reply)
		return Dispatched{Verdict{Failed, why}, ErrorResponse(Failure, r, why), answer.Facts}
	}
}

// unwritable is why a response cannot be written, or "": a status that is not a final one, or no body.
func unwritable(r generator.Response) string {
	switch {
	case r.Status < 200 || r.Status > 999:
		return fmt.Sprintf("status %d, not a final HTTP status", r.Status)
	case r.Body == nil:
		return "a response with no body"
	}
	return ""
}

// The table's refusals are declines too: a request with no endpoint, an out-of-scope endpoint, and an
// in-scope endpoint with no generator all reach the client as the same 400, each with its own reason.
func decide(ctx context.Context, resolution endpoint.Resolution, miss error, r generator.Request) (answer generator.Answer, err error) {
	if miss != nil {
		return generator.Answer{Reply: generator.Declined{Why: miss.Error()}, Facts: generator.Facts{}}, nil
	}
	e := resolution.Endpoint
	if reason, out := e.OutOfScopeReason(); out {
		return generator.Answer{Reply: generator.Declined{Why: "out of scope: " + reason}, Facts: generator.Facts{}}, nil
	}
	generate, ok := e.Generator()
	if !ok {
		return generator.Answer{Reply: generator.Declined{Why: "no generator"}, Facts: generator.Facts{}}, nil
	}
	defer func() {
		if p := recover(); p != nil {
			answer, err = generator.Answer{}, fmt.Errorf("generator %s panicked: %v", e.ID(), p)
		}
	}()
	answer, err = generate(ctx, resolution.Params, r)
	if err != nil {
		return generator.Answer{}, fmt.Errorf("generator %s: %w", e.ID(), err)
	}
	if answer.Facts == nil {
		answer.Facts = generator.Facts{}
	}
	return answer, nil
}

// The Anthropic API's error envelope, so Claude Code shows the message, and `x-should-retry: false`,
// which the Anthropic SDK obeys, so a gap is one loud error and not a run of slow retries. Every error
// response the mock makes for a request is this one, and its kind decides its status and type.
// [LAW:types-are-the-program] The kinds are the two error outcomes and nothing else, so no caller can
// ask for the error response of a Served request.
type errorKind struct {
	outcome Outcome
	status  int
	typ     string
}

var (
	Refusal = errorKind{Unmatched, http.StatusBadRequest, "invalid_request_error"}
	Failure = errorKind{Failed, http.StatusInternalServerError, "api_error"}
)

// ErrorResponse is the response for an Unmatched (Refusal) or Failed (Failure) request: its body names
// the outcome, the method, the host, the path, and why.
func ErrorResponse(kind errorKind, r generator.Request, why string) generator.Response {
	message := fmt.Sprintf("mock-anthropic %s: %s %s %s: %s", strings.ToUpper(string(kind.outcome)), r.Method, r.URL.Hostname(), r.URL.EscapedPath(), why)
	body, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"type": kind.typ, "message": message}})
	return generator.Response{
		Status: kind.status,
		Header: http.Header{"Content-Type": {"application/json"}, "X-Should-Retry": {"false"}},
		Body:   bytes.NewReader(body),
	}
}

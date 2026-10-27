// Package record is PROJECT.md "Recordings", record mode: it forwards each request to the real host with
// the client's own login, and saves the Messages exchanges that succeed, scrubbed. The client's response
// is never altered or cut by what happens to the recording.
package record

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/promptctl/mock-anthropic/internal/account"
	"github.com/promptctl/mock-anthropic/internal/builtin"
	"github.com/promptctl/mock-anthropic/internal/dispatch"
	"github.com/promptctl/mock-anthropic/internal/endpoint"
	"github.com/promptctl/mock-anthropic/internal/generator"
	"github.com/promptctl/mock-anthropic/internal/messages"
	"github.com/promptctl/mock-anthropic/internal/recording"
	"github.com/promptctl/mock-anthropic/internal/script"
)

// Totals is what the run did with the exchanges it saw: each forwarded Messages exchange is exactly one
// of saved, skipped, refused or failed. [LAW:nothing-unseen]
type Totals struct{ Saved, Skipped, Refused, Failed atomic.Int64 }

// Recorder forwards requests and saves what it can. Upstream carries every forwarded request.
type Recorder struct {
	Upstream http.RoundTripper
	Writer   *recording.Writer
	Account  account.Account
	Totals   *Totals
}

// Transport is the production upstream. [LAW:no-silent-failure] Proxy is nil on purpose: `env` points
// HTTPS_PROXY at this very mock, and a forwarder that obeyed it would send every request back to itself.
func Transport() http.RoundTripper {
	return &http.Transport{Proxy: nil, DisableCompression: true}
}

var hopByHop = []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

func clean(h http.Header, extra ...string) http.Header {
	out := h.Clone()
	for _, k := range append(hopByHop, extra...) {
		out.Del(k)
	}
	return out
}

// Handle is the dispatch.Handler of record mode.
func (rc *Recorder) Handle(ctx context.Context, resolution endpoint.Resolution, miss error, r generator.Request) dispatch.Dispatched {
	facts := generator.Facts{}
	target := *r.URL
	req, err := http.NewRequestWithContext(ctx, r.Method, target.String(), bytes.NewReader(r.Body))
	if err != nil {
		rc.Totals.Failed.Add(1)
		return dispatch.Fail(r, "building the upstream request: "+err.Error(), facts)
	}
	// Accept-Encoding is dropped so the body arrives as text the recorder can read.
	req.Header = clean(r.Header, "Accept-Encoding")
	res, err := rc.Upstream.RoundTrip(req)
	if err != nil {
		rc.Totals.Failed.Add(1)
		return dispatch.Fail(r, "upstream: "+err.Error(), facts)
	}
	t := &tee{rc: rc, upstream: res.Body, facts: facts, status: res.StatusCode, contentType: res.Header.Get("Content-Type"), request: r.Body}
	t.kind = rc.classify(resolution, miss, r, res.StatusCode)
	for _, k := range []string{"Authorization", "X-Api-Key"} {
		if v := r.Header.Get(k); v != "" {
			t.secrets = append(t.secrets, v, strings.TrimPrefix(v, "Bearer "))
		}
	}
	return dispatch.Dispatched{
		Verdict:  dispatch.Verdict{Outcome: dispatch.Served},
		Response: generator.Response{Status: res.StatusCode, Header: clean(res.Header), Body: t},
		Facts:    facts,
	}
}

// classify decides before the body is read whether this exchange could be saved; a non-nil skip is its reason.
func (rc *Recorder) classify(resolution endpoint.Resolution, miss error, r generator.Request, status int) *string {
	skip := func(why string) *string { return &why }
	switch {
	case miss != nil || resolution.Endpoint.ID() != "messages":
		return skip("not a Messages request")
	case status < 200 || status > 299:
		return skip(fmt.Sprintf("upstream answered %d", status))
	}
	if parsed, err := messages.ParseRequest(r.Body); err == nil {
		if m, ok := script.FirstReply(builtin.All(), parsed).(script.Matched); ok {
			return skip("Claude Code's own model call: " + m.Origin)
		}
	}
	return nil
}

// tee passes the upstream body to the client and keeps a copy. Close, which the server calls once on
// every path, is where the exchange is finished: saved, skipped, refused or failed.
type tee struct {
	rc          *Recorder
	upstream    io.ReadCloser
	facts       generator.Facts
	status      int
	contentType string
	request     []byte
	secrets     []string
	kind        *string // why this exchange is not a candidate, or nil
	copy        bytes.Buffer
	finished    bool // the upstream body ended with io.EOF
	readErr     error
}

func (t *tee) Read(p []byte) (int, error) {
	n, err := t.upstream.Read(p)
	t.copy.Write(p[:n])
	switch {
	case err == io.EOF:
		t.finished = true
	case err != nil:
		t.readErr = err
	}
	return n, err
}

func (t *tee) Close() error {
	upstreamErr := t.upstream.Close()
	if t.kind != nil {
		t.rc.Totals.Skipped.Add(1)
		t.facts["recording"] = map[string]any{"skipped": *t.kind}
		return nil
	}
	switch {
	case t.readErr != nil:
		t.rc.Totals.Failed.Add(1)
		t.facts["recording"] = map[string]any{"failed": "upstream body: " + t.readErr.Error()}
		return nil // the server already failed the request for the broken body
	case !t.finished:
		t.rc.Totals.Skipped.Add(1)
		t.facts["recording"] = map[string]any{"skipped": "client abandoned the response"}
		return nil
	}
	file, counts, err := t.save()
	var refused *recording.Refused
	switch {
	case err == nil:
		t.rc.Totals.Saved.Add(1)
		t.facts["recording"] = map[string]any{"file": file, "scrubbed": counts}
	case errors.Is(err, recording.ErrIncomplete):
		t.rc.Totals.Failed.Add(1)
		t.facts["recording"] = map[string]any{"failed": err.Error()}
		return err
	case errors.As(err, &refused) || errors.Is(err, errScrub):
		t.rc.Totals.Refused.Add(1)
		t.facts["recording"] = map[string]any{"refused": err.Error()}
	default:
		t.rc.Totals.Refused.Add(1)
		t.facts["recording"] = map[string]any{"refused": "saving: " + err.Error()}
	}
	return upstreamErr
}

var errScrub = errors.New("scrub")

func (t *tee) save() (string, recording.Counts, error) {
	reply, err := recording.Assemble(t.contentType, t.copy.Bytes())
	if err != nil {
		return "", nil, err
	}
	s, counts, err := recording.Scrub(recording.Exchange{Request: t.request, Reply: reply}, t.secrets, t.rc.Account)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", errScrub, err)
	}
	file, err := t.rc.Writer.Save(s)
	return file, counts, err
}

// Package generator is the seam between the mock and what answers a request (PROJECT.md "What the mock
// generates"): the request as a generator sees it, and the answer it gives.
package generator

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
)

// Request is a request as a generator sees it. URL's host is the authority the request was addressed
// to: the CONNECT target for a tunneled request, api.anthropic.com for one sent straight to the port.
type Request struct {
	Method string
	URL    *url.URL
	Header http.Header
	Body   []byte
}

// Facts are what a generator decided on the way to its answer, for the request's record: for a
// Messages request, the script that answered and what was rendered, or every script tried.
// [LAW:composability] One JSON object whose fields each generator types for itself, so this seam names
// no endpoint's concepts. An empty map is a generator that has nothing to report.
type Facts map[string]any

// Response is what reaches the client. Body may stream: it is copied as it is read, and a generator
// that produces it stops when the request's context ends. A Body that is also an io.Closer is closed
// exactly once on every path out of the request, whether the body was read to its end, abandoned, or
// never read, and before the request's Facts are read for its record; the Facts map belongs to the
// Handler until Close returns, so Close may add to it. An error from Close fails the request.
type Response struct {
	Status int
	Header http.Header
	Body   io.Reader
}

// Reply is a Response, or a Declined.
type Reply interface{ reply() }

func (Response) reply() {}

// Declined is a generator refusing a request it is the one to judge, and why: a Messages request no
// script matches is the generator's to refuse, not the table's.
type Declined struct{ Why string }

func (Declined) reply() {}

// Answer is a reply and what was decided to reach it.
type Answer struct {
	Reply Reply
	Facts Facts
}

// Func answers one request to its endpoint, given the endpoint's params by name. An error is the
// mock's own bug, and reaches the client as a 500 that names it.
type Func func(ctx context.Context, params map[string]string, r Request) (Answer, error)

// JSON is the answer most generators give: a 200 with body as JSON, and what was decided to reach it.
func JSON(body any, facts Facts) (Answer, error) {
	return JSONStatus(http.StatusOK, nil, body, facts)
}

// JSONStatus is a JSON answer with its status and extra headers.
func JSONStatus(status int, header http.Header, body any, facts Facts) (Answer, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return Answer{}, err
	}
	h := http.Header{"Content-Type": {"application/json"}}
	for k, vs := range header {
		h[k] = vs
	}
	return Answer{Reply: Response{Status: status, Header: h, Body: bytes.NewReader(data)}, Facts: facts}, nil
}

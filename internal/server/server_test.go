package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/promptctl/mock-anthropic/internal/dispatch"
	"github.com/promptctl/mock-anthropic/internal/endpoint"
	"github.com/promptctl/mock-anthropic/internal/generator"
)

var certsDir string

// One certificate directory for the package: generating the CA and leaf takes openssl a while, and
// every server here is started from the same table hosts.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mock-anthropic-server-")
	if err != nil {
		panic(err)
	}
	certsDir = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

const platform = "platform.claude.com"

func answerJSON(context.Context, map[string]string, generator.Request) (generator.Answer, error) {
	return generator.JSON(map[string]string{"ok": "yes"}, generator.Facts{"decided": "ok"})
}

// reached hears each request that reaches waitsForCtx, so a test acts once the request is in flight.
var reached = make(chan struct{}, 16)

// waitsForCtx answers only once its request's context ends.
func waitsForCtx(ctx context.Context, _ map[string]string, _ generator.Request) (generator.Answer, error) {
	reached <- struct{}{}
	<-ctx.Done()
	return generator.Answer{}, ctx.Err()
}

// streams sends a first chunk, then whatever next gives.
type streams struct {
	ctx   context.Context
	first bool
	next  func(ctx context.Context) error
}

func (s *streams) Read(p []byte) (int, error) {
	if !s.first {
		s.first = true
		return copy(p, "first chunk\n"), nil
	}
	return 0, s.next(s.ctx)
}

func streaming(next func(context.Context) error) generator.Func {
	return func(ctx context.Context, _ map[string]string, _ generator.Request) (generator.Answer, error) {
		return generator.Answer{Reply: generator.Response{Status: 200, Header: http.Header{}, Body: &streams{ctx: ctx, next: next}}, Facts: generator.Facts{}}, nil
	}
}

var table = func() *endpoint.Table {
	t, err := endpoint.Build([]endpoint.Row{
		endpoint.InScope("ok", endpoint.API, "POST", "/v1/ok", answerJSON),
		endpoint.InScope("token", platform, "POST", "/v1/oauth/token", answerJSON),
		endpoint.InScope("declines", endpoint.API, "GET", "/v1/declines", func(context.Context, map[string]string, generator.Request) (generator.Answer, error) {
			return generator.Answer{Reply: generator.Declined{Why: "no script matches"}, Facts: generator.Facts{"tried": 2}}, nil
		}),
		endpoint.InScope("errs", endpoint.API, "GET", "/v1/errs", func(context.Context, map[string]string, generator.Request) (generator.Answer, error) {
			return generator.Answer{}, errors.New("boom")
		}),
		endpoint.InScope("panics", endpoint.API, "GET", "/v1/panics", func(context.Context, map[string]string, generator.Request) (generator.Answer, error) {
			panic("kaboom")
		}),
		endpoint.InScope("bodiless", endpoint.API, "GET", "/v1/bodiless", func(context.Context, map[string]string, generator.Request) (generator.Answer, error) {
			return generator.Answer{Reply: generator.Response{Status: 204, Header: http.Header{}}, Facts: generator.Facts{}}, nil
		}),
		endpoint.InScope("statusless", endpoint.API, "GET", "/v1/statusless", func(context.Context, map[string]string, generator.Request) (generator.Answer, error) {
			return generator.Answer{Reply: generator.Response{Header: http.Header{}, Body: strings.NewReader("")}, Facts: generator.Facts{}}, nil
		}),
		endpoint.InScope("reasonless", endpoint.API, "GET", "/v1/reasonless", func(context.Context, map[string]string, generator.Request) (generator.Answer, error) {
			return generator.Answer{Reply: generator.Declined{}, Facts: generator.Facts{}}, nil
		}),
		endpoint.InScope("nobodyallowed", endpoint.API, "GET", "/v1/nobodyallowed", func(context.Context, map[string]string, generator.Request) (generator.Answer, error) {
			return generator.Answer{Reply: generator.Response{Status: 204, Header: http.Header{}, Body: strings.NewReader("null")}, Facts: generator.Facts{}}, nil
		}),
		endpoint.InScope("panicsmidway", endpoint.API, "GET", "/v1/panicsmidway", func(ctx context.Context, _ map[string]string, _ generator.Request) (generator.Answer, error) {
			body := &streams{ctx: ctx, next: func(context.Context) error { panic("midway") }}
			return generator.Answer{Reply: generator.Response{Status: 200, Header: http.Header{"content-type": {"text/event-stream"}}, Body: body}, Facts: generator.Facts{"script": "x"}}, nil
		}),
		endpoint.InScope("breaks", endpoint.API, "GET", "/v1/breaks", streaming(func(context.Context) error { return errors.New("source broke") })),
		endpoint.InScope("stalls", endpoint.API, "GET", "/v1/stalls", streaming(func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() })),
		endpoint.InScope("waits", endpoint.API, "POST", "/v1/waits", waitsForCtx),
		endpoint.InScope("bare", endpoint.API, "GET", "/v1/bare", nil),
		endpoint.OutOfScope("out", endpoint.API, "GET", "/v1/out", "remote sessions"),
	})
	if err != nil {
		panic(err)
	}
	return t
}()

type harness struct {
	server      *Server
	requests    chan RequestEvent
	connections chan ConnectionEvent
	ca          *x509.CertPool
}

func start(t *testing.T) *harness {
	t.Helper()
	h := &harness{requests: make(chan RequestEvent, 64), connections: make(chan ConnectionEvent, 64)}
	s, err := Start(Options{Table: table, Handle: dispatch.Dispatch, CertsDir: certsDir, Emit: func(e Event) {
		if _, err := Line(e); err != nil {
			t.Errorf("event %+v has no line: %v", e, err)
		}
		switch e := e.(type) {
		case RequestEvent:
			h.requests <- e
		case ConnectionEvent:
			h.connections <- e
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	h.server = s
	t.Cleanup(s.Stop)
	pem, err := os.ReadFile(s.Certificates().CA.CertPath)
	if err != nil {
		t.Fatal(err)
	}
	h.ca = x509.NewCertPool()
	h.ca.AppendCertsFromPEM(pem)
	return h
}

func (h *harness) addr() string { return fmt.Sprintf("127.0.0.1:%d", h.server.Port()) }

// proxied is a client that reaches every host through the mock as HTTPS_PROXY, trusting its CA.
func (h *harness) proxied() *http.Client {
	proxy, _ := url.Parse("http://" + h.addr())
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{RootCAs: h.ca}}}
}

func (h *harness) plain() *http.Client { return &http.Client{Transport: &http.Transport{}} }

func (h *harness) request(t *testing.T) RequestEvent {
	t.Helper()
	select {
	case e := <-h.requests:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no request record")
		return RequestEvent{}
	}
}

func (h *harness) connection(t *testing.T) ConnectionEvent {
	t.Helper()
	select {
	case e := <-h.connections:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("no connection record")
		return ConnectionEvent{}
	}
}

func lineOf(t *testing.T, e Event) string {
	t.Helper()
	line, err := Line(e)
	if err != nil {
		t.Fatal(err)
	}
	return line
}

func do(t *testing.T, c *http.Client, method, rawURL string, header http.Header) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, rawURL, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	for k, vs := range header {
		req.Header[k] = vs
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return res, string(body)
}

func TestATunneledRequestIsServedAndItsTunnelClosedByTheClient(t *testing.T) {
	h := start(t)
	c := h.proxied()
	res, body := do(t, c, "POST", "https://api.anthropic.com/v1/ok?x=1", nil)
	if res.StatusCode != 200 || body != `{"ok":"yes"}` {
		t.Fatalf("got %d %s", res.StatusCode, body)
	}
	r := h.request(t)
	if line := lineOf(t, r); !strings.HasPrefix(line, "SERVED {") {
		t.Errorf("line %s", line)
	}
	if r.Tunnel == nil || *r.Tunnel != 0 || r.Host != endpoint.API || r.Path != "/v1/ok" || r.Query != "?x=1" || *r.Endpoint != "ok" || *r.Status != 200 || r.Facts["decided"] != "ok" {
		t.Errorf("record %+v", r)
	}
	c.CloseIdleConnections()
	e := h.connection(t)
	if line := lineOf(t, e); !strings.HasPrefix(line, "CLOSED {") {
		t.Errorf("line %s", line)
	}
	if *e.Tunnel != 0 || e.Host != endpoint.API || *e.Port != 443 || *e.Requests != 1 || *e.ClosedBy != ByClient {
		t.Errorf("record %+v", e)
	}
}

func TestEveryKnownHostIsTunneledToItsOwnEndpoints(t *testing.T) {
	h := start(t)
	res, _ := do(t, h.proxied(), "POST", "https://"+platform+"/v1/oauth/token", nil)
	if r := h.request(t); res.StatusCode != 200 || r.Host != platform || *r.Endpoint != "token" {
		t.Errorf("got %d %+v", res.StatusCode, r)
	}
}

func TestAPlainRequestIsAddressedToTheAPIWhateverItsHostHeader(t *testing.T) {
	h := start(t)
	c := h.plain()
	res, _ := do(t, c, "POST", "http://"+h.addr()+"/v1/ok", nil)
	r := h.request(t)
	if res.StatusCode != 200 || r.Tunnel != nil || r.Host != endpoint.API || r.Outcome != RequestServed {
		t.Errorf("got %d %+v", res.StatusCode, r)
	}
	// platform's token endpoint, asked for by Host header on the plain port, is not reached.
	req, _ := http.NewRequest("POST", "http://"+h.addr()+"/v1/oauth/token", nil)
	req.Host = platform
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if r := h.request(t); res.StatusCode != 400 || r.Host != endpoint.API || r.Outcome != Unmatched || r.Endpoint != nil {
		t.Errorf("got %d %+v", res.StatusCode, r)
	}
	c.CloseIdleConnections()
	if e := h.connection(t); e.Outcome != Closed || e.Tunnel != nil || e.Port != nil || *e.Requests != 2 {
		t.Errorf("connection %+v", e)
	}
}

func TestUnmatchedRequestsGetA400NamingWhy(t *testing.T) {
	h := start(t)
	c := h.proxied()
	for path, why := range map[string]string{
		"/nope":        "no endpoint",
		"/v1/ok":       "no endpoint answers GET here; this path answers POST",
		"/v1/%E0":      `path segment "%E0" does not decode to UTF-8`,
		"/v1/out":      "out of scope: remote sessions",
		"/v1/bare":     "no generator",
		"/v1/declines": "no script matches",
	} {
		res, body := do(t, c, "GET", "https://api.anthropic.com"+path, nil)
		message := fmt.Sprintf("mock-anthropic UNMATCHED: GET api.anthropic.com %s: %s", path, why)
		if res.StatusCode != 400 || res.Header.Get("X-Should-Retry") != "false" || !strings.Contains(body, `"type":"invalid_request_error"`) || !strings.Contains(body, strings.ReplaceAll(message, `"`, `\"`)) {
			t.Errorf("%s: got %d %v %s", path, res.StatusCode, res.Header, body)
		}
		r := h.request(t)
		if line := lineOf(t, r); !strings.HasPrefix(line, "UNMATCHED {") || r.Why != why || *r.Status != 400 {
			t.Errorf("%s: %s", path, line)
		}
		if path == "/v1/declines" && r.Facts["tried"] != 2 {
			t.Errorf("a decline's facts are lost: %+v", r.Facts)
		}
	}
}

func TestAFailingGeneratorGetsA500NamingIt(t *testing.T) {
	h := start(t)
	c := h.proxied()
	for path, why := range map[string]string{
		"/v1/errs":       "generator errs: boom",
		"/v1/panics":     "generator panics panicked: kaboom",
		"/v1/bodiless":   "the handler gave a response with no body",
		"/v1/statusless": "the handler gave status 0, not a final HTTP status",
		"/v1/reasonless": "generator reasonless declined without saying why",
	} {
		res, body := do(t, c, "GET", "https://api.anthropic.com"+path, nil)
		if res.StatusCode != 500 || !strings.Contains(body, `"type":"api_error"`) || !strings.Contains(body, "mock-anthropic FAILED: GET api.anthropic.com "+path+": "+why) {
			t.Errorf("%s: got %d %s", path, res.StatusCode, body)
		}
		if r := h.request(t); !strings.HasPrefix(lineOf(t, r), "FAILED {") || r.Why != why || *r.Status != 500 {
			t.Errorf("%s: %+v", path, r)
		}
	}
}

func TestABodyThatBreaksFailsTheRequestAndCutsTheConnection(t *testing.T) {
	h := start(t)
	res, err := h.proxied().Get("https://api.anthropic.com/v1/breaks")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	if err == nil || string(body) != "first chunk\n" {
		t.Errorf("client read %q, %v; want the first chunk and a broken stream", body, err)
	}
	r := h.request(t)
	if r.Outcome != RequestFailed || r.Why != "response body failed: source broke" || *r.Status != 200 {
		t.Errorf("record %+v", r)
	}
	if e := h.connection(t); e.Outcome != Closed || *e.ClosedBy != ByServer {
		t.Errorf("connection %+v", e)
	}
}

func TestAClientThatLeavesAbortsItsRequest(t *testing.T) {
	h := start(t)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.anthropic.com/v1/waits", strings.NewReader("{}"))
	go func() { <-reached; cancel() }()
	if _, err := h.proxied().Do(req); err == nil {
		t.Fatal("the request was answered")
	}
	r := h.request(t)
	if line := lineOf(t, r); !strings.HasPrefix(line, "ABORTED {") || r.Why != "client closed before the response was ready" || r.Status != nil {
		t.Errorf("%s", line)
	}

	res, err := h.proxied().Get("https://api.anthropic.com/v1/stalls")
	if err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(res.Body).ReadString('\n'); err != nil || line != "first chunk\n" {
		t.Fatalf("read %q %v", line, err)
	}
	res.Body.Close()
	if r := h.request(t); r.Outcome != Aborted || r.Why != "client closed before the body finished" || *r.Status != 200 {
		t.Errorf("record %+v", r)
	}
}

// raw sends text on a connection of its own and returns what came back once the mock closed it.
func (h *harness) raw(t *testing.T, text string) string {
	t.Helper()
	conn, err := net.Dial("tcp", h.addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, text)
	// The client half-closes, so a mock that waits on more of its bytes hears there are none.
	conn.(*net.TCPConn).CloseWrite()
	got, _ := io.ReadAll(conn)
	return string(got)
}

func TestARequestWhoseBodyIsCutOffIsAborted(t *testing.T) {
	h := start(t)
	conn, err := net.Dial("tcp", h.addr())
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(conn, "POST /v1/ok HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\nExpect: 100-continue\r\n\r\n")
	// net/http sends 100 Continue once the handler reads the body, so the request is in flight.
	if line, _ := bufio.NewReader(conn).ReadString('\n'); !strings.HasPrefix(line, "HTTP/1.1 100") {
		t.Fatalf("got %q", line)
	}
	io.WriteString(conn, "short")
	conn.Close()
	if r := h.request(t); r.Outcome != Aborted || r.Why != "client closed before the request body finished" {
		t.Errorf("record %+v", r)
	}
}

func TestAPanicAfterTheResponseBeganIsRecordedWithTheStatusSent(t *testing.T) {
	h := start(t)
	res, err := h.proxied().Get("https://api.anthropic.com/v1/panicsmidway")
	if err != nil {
		t.Fatal(err)
	}
	if ct := res.Header.Values("Content-Type"); len(ct) != 1 || ct[0] != "text/event-stream" {
		t.Errorf("a lowercase header key reaches the client as net/http spells it: %v", res.Header)
	}
	if body, err := io.ReadAll(res.Body); res.StatusCode != 200 || err == nil || string(body) != "first chunk\n" {
		t.Errorf("client got %d %q, %v; want 200, the first chunk, and a broken stream", res.StatusCode, body, err)
	}
	if r := h.request(t); r.Outcome != RequestFailed || r.Why != "the mock failed: midway" || *r.Status != 200 || r.Facts["script"] != "x" {
		t.Errorf("record %+v", r)
	}
}

func TestABodyNetHTTPRefusesFailsTheRequest(t *testing.T) {
	h := start(t)
	// The 204 and its refused body are cut off together, so the client may see only a broken connection.
	if res, err := h.proxied().Get("https://api.anthropic.com/v1/nobodyallowed"); err == nil {
		res.Body.Close()
	}
	if r := h.request(t); r.Outcome != RequestFailed || !strings.HasPrefix(r.Why, "net/http refused the response body") || *r.Status != 204 {
		t.Errorf("record %+v", r)
	}
}

func TestEachPlainRequestIsJudgedByItsHost(t *testing.T) {
	h := start(t)
	c := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: h.addr()})}}
	if res, _ := do(t, c, "GET", "http://api.anthropic.com/v1/declines", nil); res.StatusCode != 400 {
		t.Fatalf("first request: %d", res.StatusCode)
	}
	h.request(t)
	// The same keep-alive connection, now for another host the table knows.
	res, body := do(t, c, "POST", "http://"+platform+"/v1/oauth/token", nil)
	if r := h.request(t); res.StatusCode != 400 || !strings.Contains(body, plainOnly) || r.Host != platform || r.Outcome != Unmatched || r.Why != plainOnly || r.Endpoint != nil {
		t.Errorf("got %d %s, record %+v", res.StatusCode, body, r)
	}
	if res, _ := do(t, c, "GET", "http://api.anthropic.com", nil); res.StatusCode != 400 {
		t.Errorf("root: %d", res.StatusCode)
	}
	if r := h.request(t); r.Path != "/" {
		t.Errorf("an empty path is /: %+v", r)
	}
}

func TestAMalformedBodyFromAClientStillThereGetsA400(t *testing.T) {
	h := start(t)
	conn, err := net.Dial("tcp", h.addr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "POST /v1/ok HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\nZZZ\r\n")
	if line, _ := bufio.NewReader(conn).ReadString('\n'); !strings.HasPrefix(line, "HTTP/1.1 400") {
		t.Errorf("answered %q", line)
	}
	if r := h.request(t); r.Outcome != Unmatched || !strings.HasPrefix(r.Why, "request body unreadable: ") {
		t.Errorf("record %+v", r)
	}
}

func TestAnIPv6ConnectTargetKeepsItsHost(t *testing.T) {
	h := start(t)
	h.raw(t, "CONNECT [::1]:443 HTTP/1.1\r\n\r\n")
	if e := h.connection(t); e.Host != "::1" || e.Outcome != Refused {
		t.Errorf("record %+v", e)
	}
}

func TestAHostWithATrailingRootDotIsTheHost(t *testing.T) {
	h := start(t)
	if got := h.raw(t, "CONNECT api.anthropic.com.:443 HTTP/1.1\r\n\r\n"); !strings.HasPrefix(got, "HTTP/1.1 200") {
		t.Errorf("CONNECT answered %q", got)
	}
	if e := h.connection(t); e.Host != endpoint.API || e.Outcome == Refused {
		t.Errorf("tunnel %+v", e)
	}
	// An HTTP_PROXY client sends an http:// URL to the plain port in absolute form.
	proxy := &url.URL{Scheme: "http", Host: h.addr()}
	if res, body := do(t, &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}, "GET", "http://api.anthropic.com./v1/declines", nil); res.StatusCode != 400 {
		t.Errorf("plain answered %d %s", res.StatusCode, body)
	}
	if r := h.request(t); *r.Endpoint != "declines" {
		t.Errorf("request %+v", r)
	}
}

func TestRefusedConnectionsAreAnsweredAndRecorded(t *testing.T) {
	h := start(t)
	for name, c := range map[string]struct {
		send, status, host, why string
		port                    *int
	}{
		"an unknown host":           {"CONNECT example.com:443 HTTP/1.1\r\n\r\n", "403", "example.com", "not a host the mock answers", ptr(443)},
		"a known host's other port": {"CONNECT api.anthropic.com:80 HTTP/1.1\r\n\r\n", "403", endpoint.API, "not port 443, the only port the mock answers", ptr(80)},
		"a malformed target":        {"CONNECT api.anthropic.com HTTP/1.1\r\n\r\n", "403", endpoint.API, "malformed CONNECT target", nil},
		"a plain request elsewhere": {"GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n", "403", "example.com", "not api.anthropic.com, the only host the plain port answers; others are reached through CONNECT", nil},
		"TLS sent to the port":      {"\x16\x03\x01\x00", "400", endpoint.API, "not an HTTP request: it opens with byte 0x16, a TLS handshake: the port speaks plain HTTP, and HTTPS only through CONNECT", nil},
		"a malformed request line":  {"hello there\r\n", "400", endpoint.API, `malformed request line: "hello there\r"`, nil},
		"a bare LF line end":        {"GET / HTTP/1.1\n", "400", endpoint.API, `malformed request line: "GET / HTTP/1.1"`, nil},
	} {
		got := h.raw(t, c.send)
		if !strings.HasPrefix(got, "HTTP/1.1 "+c.status) || !strings.Contains(got, "mock-anthropic REFUSED: ") || !strings.Contains(got, c.why) {
			t.Errorf("%s: answered %q", name, got)
		}
		e := h.connection(t)
		if line := lineOf(t, e); !strings.HasPrefix(line, "REFUSED {") || e.Host != c.host || e.Why != c.why || !samePort(e.Port, c.port) || e.Requests != nil {
			t.Errorf("%s: %s", name, line)
		}
	}
}

func ptr(n int) *int { return &n }

func samePort(a, b *int) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

func TestAClientThatDoesNotTrustTheCAFailsItsTunnel(t *testing.T) {
	h := start(t)
	proxy, _ := url.Parse("http://" + h.addr())
	_, err := (&http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}).Get("https://api.anthropic.com/v1/ok")
	if err == nil {
		t.Fatal("a client without the CA was served")
	}
	e := h.connection(t)
	if line := lineOf(t, e); !strings.HasPrefix(line, "FAILED {") || !strings.HasPrefix(e.Why, "TLS handshake failed: ") || *e.Requests != 0 {
		t.Errorf("%s", line)
	}
}

func TestConnectionsThatCarryNoRequest(t *testing.T) {
	h := start(t)
	for name, c := range map[string]struct {
		send    string
		outcome ConnectionOutcome
		why     string
	}{
		"one that sends nothing":                {"", Closed, ""},
		"one that leaves mid request line":      {"GE", ConnectionFailed, unfinishedLine},
		"one that leaves mid CONNECT head":      {"CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: x\r\n", ConnectionFailed, unfinishedHead},
		"one whose request net/http turns away": {"GET / HTTP/1.1\r\nno colon here\r\n\r\n", ConnectionFailed, plainUnanswered},
	} {
		h.raw(t, c.send)
		if e := h.connection(t); e.Outcome != c.outcome || e.Why != c.why || *e.Requests != 0 {
			t.Errorf("%s: %+v", name, e)
		}
	}

	// A tunnel whose client completes the handshake and leaves.
	conn, err := net.Dial("tcp", h.addr())
	if err != nil {
		t.Fatal(err)
	}
	io.WriteString(conn, "CONNECT api.anthropic.com:443 HTTP/1.1\r\n\r\n")
	br := bufio.NewReader(conn)
	if line, _ := br.ReadString('\n'); !strings.HasPrefix(line, "HTTP/1.1 200") {
		t.Fatalf("CONNECT answered %q", line)
	}
	br.ReadString('\n')
	t2 := tls.Client(conn, &tls.Config{RootCAs: h.ca, ServerName: endpoint.API})
	if err := t2.Handshake(); err != nil {
		t.Fatal(err)
	}
	t2.Close()
	if e := h.connection(t); e.Outcome != ConnectionFailed || e.Why != tunnelUnanswered || *e.ClosedBy != ByClient {
		t.Errorf("tunnel: %+v", e)
	}
}

func TestStopEndsEveryUnitAndSaysSo(t *testing.T) {
	h := start(t)
	done := make(chan error, 1)
	go func() {
		_, err := h.proxied().Post("https://api.anthropic.com/v1/waits", "application/json", strings.NewReader("{}"))
		done <- err
	}()
	<-reached
	h.server.Stop()
	if err := <-done; err == nil {
		t.Error("the request was answered")
	}
	if r := h.request(t); r.Outcome != Aborted || r.Why != "the mock stopped before the response was ready" {
		t.Errorf("request %+v", r)
	}
	if e := h.connection(t); e.Outcome != Closed || *e.ClosedBy != ByStop || *e.Requests != 1 {
		t.Errorf("connection %+v", e)
	}
}

package record

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/promptctl/mock-anthropic/internal/account"
	"github.com/promptctl/mock-anthropic/internal/dispatch"
	"github.com/promptctl/mock-anthropic/internal/endpoint"
	"github.com/promptctl/mock-anthropic/internal/generator"
	"github.com/promptctl/mock-anthropic/internal/recording"
	"github.com/promptctl/mock-anthropic/internal/server"
)

var certsDir string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mock-anthropic-record-")
	if err != nil {
		panic(err)
	}
	certsDir = dir
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// Values a real login would carry, built at run time.
func uuid(n int) string { return fmt.Sprintf("%08x-1111-4222-8333-%012x", 0xabcdef00+n, n) }

var (
	secretToken = "tok-" + strings.Repeat("r3alS3cret", 3)
	realEmail   = "someone" + "@" + "private-corp.example"
	realAcct    = uuid(1)
	realOrg     = uuid(2)
	userHash    = strings.Repeat("ab12", 16)
)

// upstream is the stand-in for the real API: it records what reached it and answers per path.
type upstream struct {
	srv  *httptest.Server
	mu   sync.Mutex
	seen []*http.Request
	hook func(w http.ResponseWriter, r *http.Request)
}

func newUpstream(t *testing.T) *upstream {
	u := &upstream{}
	u.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.seen = append(u.seen, r)
		u.mu.Unlock()
		u.hook(w, r)
	}))
	t.Cleanup(u.srv.Close)
	return u
}

// transport sends every host to the stand-in.
func (u *upstream) transport() http.RoundTripper {
	pool := x509.NewCertPool()
	pool.AddCert(u.srv.Certificate())
	return &http.Transport{
		DisableCompression: true,
		TLSClientConfig:    &tls.Config{RootCAs: pool, ServerName: "example.com"},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, u.srv.Listener.Addr().String())
		},
	}
}

type rig struct {
	dir    string
	totals *Totals
	client *http.Client
	events chan server.RequestEvent
	up     *upstream
}

func start(t *testing.T) *rig {
	t.Helper()
	up := newUpstream(t)
	dir := t.TempDir()
	w, err := recording.NewWriter(dir)
	if err != nil {
		t.Fatal(err)
	}
	totals := &Totals{}
	rc := &Recorder{Upstream: up.transport(), Writer: w, Account: account.Default(), Totals: totals}
	events := make(chan server.RequestEvent, 64)
	s, err := server.Start(server.Options{Table: endpoint.For(account.Default(), nil), Handle: rc.Handle, CertsDir: certsDir,
		Emit: func(e server.Event) {
			if r, ok := e.(server.RequestEvent); ok {
				events <- r
			}
		}, Crash: func(err error) { t.Errorf("crash: %v", err) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	pem, _ := os.ReadFile(s.Certificates().CA.CertPath)
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", s.Port()))
	return &rig{dir: dir, totals: totals, events: events, up: up,
		client: &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{RootCAs: pool}}}}
}

func (r *rig) post(t *testing.T, path, body string, header http.Header) (*http.Response, string, server.RequestEvent) {
	t.Helper()
	req, _ := http.NewRequest("POST", "https://api.anthropic.com"+path, strings.NewReader(body))
	for k, v := range header {
		req.Header[k] = v
	}
	res, err := r.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, string(got), <-r.events
}

func (r *rig) saved(t *testing.T) []string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(r.dir, "*.json"))
	var out []string
	for _, f := range files {
		b, _ := os.ReadFile(f)
		out = append(out, string(b))
	}
	return out
}

func sseBody(text string) string {
	half := len(text) / 2
	ev := func(typ, data string) string { return "event: " + typ + "\ndata: " + data + "\n\n" }
	return ev("message_start", `{"type":"message_start","message":{"id":"msg_1"}}`) + ev("ping", `{"type":"ping"}`) +
		ev("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`+text[:half]+`"}}`) +
		ev("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`+text[half:]+`"}}`) +
		ev("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		ev("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`) +
		ev("message_stop", `{"type":"message_stop"}`)
}

func messagesRequest() string {
	return fmt.Sprintf(`{"model":"m","stream":true,"max_tokens":50,"metadata":{"user_id":"user_%s_account_%s_session_%s"},"messages":[{"role":"user","content":"I am %s in org %s, key %s"}]}`,
		userHash, realAcct, uuid(3), realEmail, realOrg, secretToken)
}

func TestARecordedMessagesExchangeHoldsNothingTheUpstreamKnows(t *testing.T) {
	r := start(t)
	r.up.hook = func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sseBody("hello "+realEmail+" "+realOrg))
	}
	header := http.Header{"Authorization": {"Bearer " + secretToken}, "Content-Type": {"application/json"}, "Accept-Encoding": {"gzip"}}
	res, body, ev := r.post(t, "/v1/messages", messagesRequest(), header)
	if res.StatusCode != 200 || !strings.Contains(body, realEmail) {
		t.Fatalf("the client must get the upstream answer untouched: %d %q", res.StatusCode, body)
	}
	up := r.up.seen[0]
	if up.Header.Get("Authorization") != "Bearer "+secretToken || up.Header.Get("Accept-Encoding") != "" {
		t.Errorf("forwarded headers: %v", up.Header)
	}
	files := r.saved(t)
	if len(files) != 1 {
		t.Fatalf("%d recordings; facts %v", len(files), ev.Facts)
	}
	for _, secret := range []string{secretToken, realEmail, realAcct, realOrg, uuid(3), userHash} {
		if strings.Contains(files[0], secret) {
			t.Errorf("the recording holds a value the upstream knew (%d bytes of it)", len(secret))
		}
	}
	if _, err := recording.Parse([]byte(files[0])); err != nil {
		t.Error(err)
	}
	if rec, _ := ev.Facts["recording"].(map[string]any); rec["file"] != "0001.json" || r.totals.Saved.Load() != 1 {
		t.Errorf("facts %v, totals saved %d", ev.Facts, r.totals.Saved.Load())
	}
}

func TestAnAPIKeyLoginIsScrubbedToo(t *testing.T) {
	r := start(t)
	key := "sk-ant-" + "api03-" + strings.Repeat("Zq9", 8)
	r.up.hook = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"content":[{"type":"text","text":"your key is `+key+`"}],"stop_reason":"end_turn"}`)
	}
	r.post(t, "/v1/messages", `{"model":"m","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`, http.Header{"X-Api-Key": {key}})
	files := r.saved(t)
	if len(files) != 1 || strings.Contains(files[0], key) {
		t.Errorf("recordings %v", files)
	}
}

func TestExchangesThatAreNotSavedSayWhy(t *testing.T) {
	r := start(t)
	status := 200
	r.up.hook = func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, `{"content":[{"type":"text","text":"x"}],"stop_reason":"end_turn"}`)
	}
	reason := func(ev server.RequestEvent) any {
		rec, _ := ev.Facts["recording"].(map[string]any)
		return rec["skipped"]
	}

	_, _, ev := r.post(t, "/v1/messages/count_tokens", `{}`, nil)
	if reason(ev) != "not a Messages request" {
		t.Errorf("count_tokens: %v", ev.Facts)
	}
	status = 500
	_, _, ev = r.post(t, "/v1/messages", `{"model":"m","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if reason(ev) != "upstream answered 500" {
		t.Errorf("non-2xx: %v", ev.Facts)
	}
	status = 200
	_, _, ev = r.post(t, "/v1/messages", `{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"quota"}]}`, nil)
	if got, _ := reason(ev).(string); !strings.HasPrefix(got, "Claude Code's own model call: built-in: quota probe") {
		t.Errorf("builtin: %v", ev.Facts)
	}
	if len(r.saved(t)) != 0 || r.totals.Skipped.Load() != 3 {
		t.Errorf("saved %d, skipped %d", len(r.saved(t)), r.totals.Skipped.Load())
	}
}

func TestAnUnreadableResponseIsRefusedAndTheClientStillGetsIt(t *testing.T) {
	r := start(t)
	r.up.hook = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"type\":\"mystery\"}\n\n")
	}
	_, body, ev := r.post(t, "/v1/messages", `{"model":"m","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`, nil)
	rec, _ := ev.Facts["recording"].(map[string]any)
	if !strings.Contains(body, "mystery") || !strings.Contains(fmt.Sprint(rec["refused"]), `event type "mystery"`) || r.totals.Refused.Load() != 1 || len(r.saved(t)) != 0 {
		t.Errorf("body %q facts %v refused %d", body, ev.Facts, r.totals.Refused.Load())
	}
}

func TestAStreamThatEndsWithoutMessageStopFailsTheRequest(t *testing.T) {
	r := start(t)
	r.up.hook = func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		full := sseBody("cut")
		io.WriteString(w, full[:strings.Index(full, "event: message_stop")])
	}
	_, _, ev := r.post(t, "/v1/messages", `{"model":"m","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if ev.Outcome != server.RequestOutcome(dispatch.Failed) || r.totals.Failed.Load() != 1 || len(r.saved(t)) != 0 {
		t.Errorf("%+v failed %d", ev, r.totals.Failed.Load())
	}
}

func TestAnUpstreamThatCannotBeReachedFailsTheRequest(t *testing.T) {
	r := start(t)
	r.up.srv.Close()
	res, _, ev := r.post(t, "/v1/messages", `{"model":"m","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`, nil)
	if res.StatusCode != 500 || ev.Outcome != server.RequestOutcome(dispatch.Failed) || r.totals.Failed.Load() != 1 {
		t.Errorf("%d %+v failed %d", res.StatusCode, ev, r.totals.Failed.Load())
	}
}

func TestTheProductionTransportIgnoresTheProxyEnvironment(t *testing.T) {
	if Transport().(*http.Transport).Proxy != nil {
		t.Error("the production transport consults a proxy; HTTPS_PROXY points at this very mock")
	}
}

func TestCleanDropsTheHeadersConnectionNames(t *testing.T) {
	h := http.Header{"Connection": {"keep-alive, X-Hop"}, "X-Hop": {"1"}, "X-Keep": {"2"}}
	got := clean(h)
	if got.Get("X-Hop") != "" || got.Get("X-Keep") != "2" {
		t.Errorf("%v", got)
	}
}

func TestAClientThatLeavesMidReadIsAbandonmentNotAnUpstreamFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tm := &tee{ctx: ctx, rc: &Recorder{Totals: &Totals{}}, facts: generator.Facts{}, upstream: io.NopCloser(errReader{context.Canceled})}
	tm.Read(make([]byte, 8))
	tm.Close()
	if tm.rc.Totals.Failed.Load() != 0 || tm.rc.Totals.Skipped.Load() != 1 {
		t.Errorf("failed %d skipped %d", tm.rc.Totals.Failed.Load(), tm.rc.Totals.Skipped.Load())
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func TestAnUnreachableUpstreamFailsOnlyMessagesRequests(t *testing.T) {
	r := start(t)
	r.up.srv.Close()
	r.post(t, "/api/event_logging/batch", `{}`, nil)
	if r.totals.Failed.Load() != 0 || r.totals.Skipped.Load() != 1 {
		t.Errorf("failed %d skipped %d", r.totals.Failed.Load(), r.totals.Skipped.Load())
	}
}

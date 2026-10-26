// Package server is PROJECT.md "How a program reaches it" and "Failure behavior": one listener on
// 127.0.0.1 that is both an HTTPS forward proxy for the table's hosts and a plain origin for
// api.anthropic.com. It moves bytes between sockets and a Handler, and it is the one place a record of
// each unit of work is made.
package server

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/promptctl/mock-anthropic/internal/certificate"
	"github.com/promptctl/mock-anthropic/internal/dispatch"
	"github.com/promptctl/mock-anthropic/internal/endpoint"
	"github.com/promptctl/mock-anthropic/internal/generator"
)

// Options are what a server is started from. Handle answers every request; dispatch.Dispatch answers
// from the table's generators, and record mode forwards instead. Emit receives every record, one per
// request and one per connection, from many goroutines at once.
type Options struct {
	Port     int
	Table    *endpoint.Table
	Handle   dispatch.Handler
	CertsDir string
	Emit     func(Event)
}

// Server is a started mock. Stop ends it.
type Server struct {
	table        *endpoint.Table
	handle       dispatch.Handler
	emit         func(Event)
	certificates certificate.Certificates
	tls          *tls.Config
	front        net.Listener
	inner        *http.Server
	served       *queue

	// [LAW:no-ambient-temporal-coupling] This instance owns every socket it accepts and every unit of
	// work in flight, each added the moment it starts, so Stop ends them itself instead of waiting on
	// how far each client got.
	mu       sync.Mutex
	stopping bool
	conns    map[*clientConn]struct{}
	units    sync.WaitGroup

	nextTunnel atomic.Int64
}

const (
	loopback    = "127.0.0.1"
	established = "HTTP/1.1 200 Connection Established\r\n\r\n"
	// Every host in the table speaks HTTPS on 443 and nothing else, and a tunnel is always TLS.
	httpsPort = 443
	// An HTTP/1 request line (RFC 9112 3): a method, a target, and the version, ended by CRLF. Every
	// method Claude Code sends is capitals, and a line ended by a bare LF, which RFC 9112 2.2 lets a
	// server refuse, is refused, so a client that sends one hears why instead of waiting on a CRLF.
	maxLine = 8192
	maxHead = 65536
	// How long a refused client is given to read its answer and leave.
	refuseDrain = 5 * time.Second
	// net/http answers a malformed request or oversized headers itself, before the mock sees it.
	tunnelUnanswered = "closed before any request reached the mock: a client that opens a tunnel and never uses it closes this way, and so does a request net/http answers itself as malformed or with oversized headers"
	plainUnanswered  = "closed before any request reached the mock: net/http answers a malformed request or oversized headers itself, before the mock sees it, and a client that closes partway through its request head leaves this way too"
	unfinishedLine   = "closed before finishing its request line"
	bodyCutOff       = "the request body finished"
	notReady         = "the response was ready"
	bodyUnfinished   = "the body finished"
	unfinishedHead   = "closed before finishing its CONNECT head"
	plainOnly        = "not " + endpoint.API + ", the only host the plain port answers; others are reached through CONNECT"
)

var requestLine = regexp.MustCompile(`^([A-Z]+) (\S+) HTTP/1\.[01]\r$`)

// Start listens and serves until Stop. The leaf certificate covers exactly the hosts the table names,
// the hosts it accepts CONNECT for: [LAW:one-source-of-truth] both are read from the one table.
func Start(o Options) (*Server, error) {
	certs, err := certificate.Ensure(o.CertsDir, o.Table.Hosts())
	if err != nil {
		return nil, err
	}
	leaf, err := tls.LoadX509KeyPair(certs.Leaf.CertPath, certs.Leaf.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("loading the leaf certificate: %w", err)
	}
	front, err := net.Listen("tcp", net.JoinHostPort(loopback, strconv.Itoa(o.Port)))
	if err != nil {
		return nil, err
	}
	s := &Server{
		table:        o.Table,
		handle:       o.Handle,
		emit:         o.Emit,
		certificates: certs,
		// HTTP/1.1 only: every connection is served by net/http's HTTP/1 server, which counts and
		// records its requests.
		tls:    &tls.Config{Certificates: []tls.Certificate{leaf}, NextProtos: []string{"http/1.1"}},
		front:  front,
		served: newQueue(front.Addr()),
		conns:  map[*clientConn]struct{}{},
	}
	s.inner = &http.Server{
		Handler: s,
		// net/http is done with a connection, its last handler returned, once it reports it closed.
		ConnState: func(c net.Conn, state http.ConnState) {
			if state == http.StateClosed {
				close(c.(*servedConn).released)
			}
		},
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, servedKey{}, c.(*servedConn))
		},
	}
	s.units.Add(2)
	go func() {
		defer s.units.Done()
		s.inner.Serve(s.served) //nolint:errcheck // it returns only when Stop closes the queue
	}()
	go func() {
		defer s.units.Done()
		s.accept()
	}()
	return s, nil
}

// Port is the port the server listens on.
func (s *Server) Port() int { return s.front.Addr().(*net.TCPAddr).Port }

// Certificates are the CA and leaf the server presents.
func (s *Server) Certificates() certificate.Certificates { return s.certificates }

// Stop closes the listener and every connection, and returns once every unit of work in flight has
// written its record: each record of a unit Stop ended says so.
func (s *Server) Stop() {
	s.mu.Lock()
	s.stopping = true
	conns := make([]*clientConn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	s.front.Close()
	s.served.Close()
	for _, c := range conns {
		c.Close()
	}
	s.units.Wait()
}

func (s *Server) isStopping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopping
}

func (s *Server) accept() {
	for {
		raw, err := s.front.Accept()
		if err != nil {
			// Only Stop closes the listener; any other accept error ends serving, and is the mock's.
			if !s.isStopping() {
				panic(fmt.Sprintf("accepting connections: %v", err))
			}
			return
		}
		c := &clientConn{Conn: raw, read: raw, server: s, started: time.Now(), done: make(chan struct{})}
		s.mu.Lock()
		stopping := s.stopping
		if !stopping {
			s.conns[c] = struct{}{}
			s.units.Add(1)
		}
		s.mu.Unlock()
		if stopping {
			raw.Close()
			continue
		}
		go func() {
			defer s.units.Done()
			s.open(c)
		}()
	}
}

// What a front connection is served as: the authority every request on it is addressed to, the tunnel
// it is (nil for the plain origin) and the port its CONNECT named, and why a connection that answered
// no request failed.
type kind struct {
	authority  string
	tunnel     *int64
	port       *int
	unanswered string
}

// open reads a connection's opening, only as far as choosing what it is, and serves or refuses it.
func (s *Server) open(c *clientConn) {
	switch o := readOpening(c).(type) {
	case connect:
		s.tunnel(c, o)
	case plain:
		s.plain(c, o)
	case refused:
		s.refuse(c, "400 Bad Request", o.why)
		s.emit(ConnectionEvent{Event: "connection", Host: endpoint.API, Outcome: Refused, Why: o.why})
	case closed:
		// A client that connects and leaves having sent nothing asked for nothing; one that left partway
		// through its first line did not get what it was sending, unless Stop cut it.
		c.Close()
		e := closedRecord(endpoint.API, nil, nil, 0, c)
		if o.read > 0 && c.closedBy != ByStop {
			e.Outcome, e.Why = ConnectionFailed, o.why
		}
		s.emit(e)
	}
}

func (s *Server) tunnel(c *clientConn, o connect) {
	host, port := connectTarget(o.target)
	var why string
	switch {
	case port == nil:
		why = "malformed CONNECT target"
	case !s.table.Knows(host):
		why = "not a host the mock answers"
	case *port != httpsPort:
		why = fmt.Sprintf("not port %d, the only port the mock answers", httpsPort)
	}
	if why != "" {
		s.forbid(c, host, port, "CONNECT "+o.target, why)
		return
	}
	tunnel := s.nextTunnel.Add(1) - 1
	k := kind{authority: host, tunnel: &tunnel, port: port, unanswered: tunnelUnanswered}
	c.read = io.MultiReader(bytes.NewReader(o.rest), c.Conn)
	if _, err := io.WriteString(c, established); err != nil {
		c.fail(fmt.Sprintf("writing the CONNECT response: %v", err))
		s.serve(c, k, nil)
		return
	}
	t := tls.Server(c, s.tls)
	// The handshake is the mock's to run, so a client that does not trust the mock's CA
	// (NODE_EXTRA_CA_CERTS) is named on its connection's record.
	if err := t.Handshake(); err != nil {
		c.fail(fmt.Sprintf("TLS handshake failed: %v", err))
		s.serve(c, k, nil)
		return
	}
	s.serve(c, k, t)
}

// RFC 9112 3.2.2: a request whose target is in absolute form is addressed to the host it names. The
// plain port is the origin for api.anthropic.com alone, so an http:// URL for any other host, which an
// HTTP_PROXY client sends this way, is refused as a CONNECT to an unknown host is. The front judges a
// connection by its first request; ServeHTTP judges each later one on it the same way.
func (s *Server) plain(c *clientConn, o plain) {
	if host := plainHost(o.target); host != endpoint.API {
		s.forbid(c, host, nil, o.target, plainOnly)
		return
	}
	c.read = io.MultiReader(bytes.NewReader(o.read), c.Conn)
	s.serve(c, kind{authority: endpoint.API, unanswered: plainUnanswered}, c)
}

// plainHost is the host a plain request's target addresses: the API for origin form, the host an
// absolute-form target names, or the target's raw text when it names none.
func plainHost(target string) string {
	if strings.HasPrefix(target, "/") {
		return endpoint.API
	}
	if u, err := url.Parse(target); err == nil && u.Hostname() != "" {
		return endpoint.CanonicalHost(u.Host)
	}
	return target
}

// forbid answers a connection addressed to a host or port the mock does not answer with a 403 naming
// it, and records it.
func (s *Server) forbid(c *clientConn, host string, port *int, what, why string) {
	s.refuse(c, "403 Forbidden", what+": "+why)
	s.emit(ConnectionEvent{Event: "connection", Host: host, Port: port, Outcome: Refused, Why: why})
}

// refuse answers the connection and closes it. The client's side is read to its end first, so the
// close does not reset a connection whose unread bytes would discard the answer before the client
// reads it.
func (s *Server) refuse(c *clientConn, status, what string) {
	body := "mock-anthropic REFUSED: " + what + "\n"
	fmt.Fprintf(c.Conn, "HTTP/1.1 %s\r\ncontent-type: text/plain\r\ncontent-length: %d\r\nconnection: close\r\n\r\n%s", status, len(body), body)
	if tcp, ok := c.Conn.(*net.TCPConn); ok {
		tcp.CloseWrite()
	}
	// A client that never ends its side is not waited on past refuseDrain.
	c.Conn.SetReadDeadline(time.Now().Add(refuseDrain)) //nolint:errcheck // a socket that refuses a deadline is closed below anyway
	io.Copy(io.Discard, c.Conn)                         //nolint:errcheck // the answer is sent; how the client leaves changes nothing
	c.Close()
}

// serve hands a connection to net/http, or, when it has nothing to serve (conn is nil: its handshake
// failed), closes it; either way the connection's one record is written once it has closed and every
// request on it has written its own. [LAW:no-ambient-temporal-coupling] The connection's unit of work
// lasts until net/http releases it, so Stop cannot return between a request's head and its handler.
func (s *Server) serve(c *clientConn, k kind, conn net.Conn) {
	released := make(chan struct{})
	if conn == nil {
		c.Close()
		close(released)
	} else {
		s.served.push(&servedConn{Conn: conn, client: c, kind: k, released: released})
	}
	<-released
	<-c.done
	e := closedRecord(k.authority, k.tunnel, k.port, c.requests.Load(), c)
	switch {
	case c.closedBy == ByStop:
	case c.failure != "":
		e.Outcome, e.Why = ConnectionFailed, c.failure
	case *e.Requests == 0:
		e.Outcome, e.Why = ConnectionFailed, k.unanswered
	}
	s.emit(e)
}

// closedRecord is the record of a connection that was served and has closed, as closed; its caller decides
// whether it failed.
func closedRecord(host string, tunnel *int64, port *int, requests int64, c *clientConn) ConnectionEvent {
	duration := c.ended.Sub(c.started).Milliseconds()
	by := c.closedBy
	return ConnectionEvent{Event: "connection", Tunnel: tunnel, Host: host, Port: port, Requests: &requests, DurationMS: &duration, ClosedBy: &by, Outcome: Closed}
}

// What a connection opens with. A CONNECT is read to the end of its head, so the bytes after it, the
// start of the client's TLS, are told apart from it. Anything else is the plain origin, with every byte
// read so far. Bytes that cannot open a request line are refused at once, rather than waited on for a
// line end a TLS handshake may never contain, and so is a first line that is not a request line.
type opening interface{ opening() }

type (
	connect struct {
		target string
		rest   []byte
	}
	plain struct {
		target string
		read   []byte
	}
	refused struct{ why string }
	closed  struct {
		read int
		why  string // what it left unfinished, when it sent anything
	}
)

func (connect) opening() {}
func (plain) opening()   {}
func (refused) opening() {}
func (closed) opening()  {}

func readOpening(r io.Reader) opening {
	var read []byte
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		read = append(read, buf[:n]...)
		if len(read) > 0 {
			if !isToken(read[0]) {
				return refused{notARequest(read[0])}
			}
			if end := bytes.IndexByte(read, '\n'); end == -1 {
				if len(read) > maxLine {
					return refused{fmt.Sprintf("request line longer than %d bytes", maxLine)}
				}
			} else {
				line := read[:end]
				m := requestLine.FindSubmatch(line)
				if m == nil {
					return refused{fmt.Sprintf("malformed request line: %q", line[:min(len(line), 200)])}
				}
				if string(m[1]) != http.MethodConnect {
					return plain{string(m[2]), read}
				}
				if head := bytes.Index(read, []byte("\r\n\r\n")); head != -1 {
					return connect{string(m[2]), read[head+4:]}
				}
				if len(read) > maxHead {
					return refused{fmt.Sprintf("CONNECT head longer than %d bytes", maxHead)}
				}
			}
		}
		if err != nil {
			why := unfinishedLine
			if bytes.IndexByte(read, '\n') != -1 {
				why = unfinishedHead
			}
			return closed{len(read), why}
		}
	}
}

// A method is a token (RFC 9110 5.6.2), so a request line opens with a token character.
func isToken(b byte) bool {
	return b < 0x80 && (b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || strings.IndexByte("!#$%&'*+-.^_`|~", b) >= 0)
}

func notARequest(b byte) string {
	tlsHint := ""
	// 0x16 is a TLS record's handshake type: an https:// URL pointed straight at the port.
	if b == 0x16 {
		tlsHint = ", a TLS handshake: the port speaks plain HTTP, and HTTPS only through CONNECT"
	}
	return fmt.Sprintf("not an HTTP request: it opens with byte 0x%02x%s", b, tlsHint)
}

// connectTarget is the host and port of a CONNECT's authority-form target (RFC 9110 9.3.6). One that
// does not parse, or names no port, keeps its raw text as the host and has a nil port, so its refusal
// can still name it.
func connectTarget(text string) (string, *int) {
	host, digits, err := net.SplitHostPort(text)
	port, ok := parsePort(digits)
	if err != nil || !ok {
		return text, nil
	}
	return endpoint.CanonicalHost(host), &port
}

// [LAW:one-source-of-truth] What a port number is, as text: one to five decimal digits, at most 65535.
func parsePort(text string) (int, bool) {
	if len(text) == 0 || len(text) > 5 || strings.Trim(text, "0123456789") != "" {
		return 0, false
	}
	n, _ := strconv.Atoi(text)
	return n, n <= 65535
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

// clientConn is one accepted connection. It records who ended it: the mock's Stop, the client (its
// side ended before the mock closed it), or otherwise the mock's own server.
type clientConn struct {
	net.Conn
	read     io.Reader // what the connection's server reads: the bytes the front already read, then the socket
	server   *Server
	started  time.Time
	requests atomic.Int64

	mu       sync.Mutex
	peerDone bool
	failure  string // why the mock failed the connection, if it did

	once     sync.Once
	ended    time.Time
	closedBy ClosedBy
	done     chan struct{} // closed once the connection has closed; ended and closedBy are set then
}

func (c *clientConn) Read(p []byte) (int, error) {
	n, err := c.read.Read(p)
	c.peerEnded(err)
	return n, err
}

// peerEnded notes a read's error: any but an expired deadline, which is net/http aborting its own
// pending read before it closes the connection, means the client's side has ended.
func (c *clientConn) peerEnded(err error) {
	if err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.peerDone = true
}

func (c *clientConn) fail(why string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failure = why
}

// endedBy is who is ending the connection now.
func (c *clientConn) endedBy() ClosedBy {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.server.isStopping():
		return ByStop
	case c.peerDone:
		return ByClient
	default:
		return ByServer
	}
}

func (c *clientConn) Close() error {
	c.once.Do(func() {
		c.closedBy = c.endedBy()
		c.Conn.Close()
		c.ended = time.Now()
		c.server.mu.Lock()
		delete(c.server.conns, c)
		c.server.mu.Unlock()
		close(c.done)
	})
	return nil
}

// servedConn is a connection as net/http serves it: the TLS session or the plain socket, and the
// front connection it runs over.
type servedConn struct {
	net.Conn
	client   *clientConn
	kind     kind
	released chan struct{} // closed once net/http is done with the connection
}

// Read marks the client's side ended when it ends: inside a tunnel, a TLS close_notify ends it
// with no error on the socket beneath.
func (c *servedConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.client.peerEnded(err)
	return n, err
}

type servedKey struct{}

// queue is the listener net/http serves: the front pushes each connection it chose to serve.
type queue struct {
	addr   net.Addr
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newQueue(addr net.Addr) *queue {
	return &queue{addr: addr, conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (q *queue) push(c *servedConn) {
	select {
	case q.conns <- c:
	case <-q.closed:
		c.client.Close()
		close(c.released)
	}
}

func (q *queue) Accept() (net.Conn, error) {
	select {
	case c := <-q.conns:
		return c, nil
	case <-q.closed:
		return nil, net.ErrClosed
	}
}

func (q *queue) Close() error {
	q.once.Do(func() { close(q.closed) })
	return nil
}

func (q *queue) Addr() net.Addr { return q.addr }

// ServeHTTP serves one request and writes its one record, once its response has ended one way or
// another. [LAW:no-silent-failure] Whatever goes wrong becomes the record's outcome.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sc := r.Context().Value(servedKey{}).(*servedConn)
	sc.client.requests.Add(1)
	started := time.Now()

	// [LAW:types-are-the-program] Every request in a tunnel is addressed to the tunnel's authority, and
	// every plain one to the host its target names; the URL is built from that, so the Host header
	// cannot choose the host. A plain request for any host but the API is refused, as its connection's
	// first one would have been.
	authority := sc.kind.authority
	if sc.kind.tunnel == nil {
		authority = plainHost(r.RequestURI)
	}
	// An empty path in absolute form is "/" (RFC 9110 4.2.3).
	path := cmp.Or(r.URL.Path, "/")
	u := &url.URL{Scheme: "https", Host: authority, Path: path, RawPath: r.URL.RawPath, RawQuery: r.URL.RawQuery}
	resolution, miss := s.table.Resolve(r.Method, authority, u.EscapedPath())
	var id *endpoint.ID
	if miss == nil {
		e := resolution.Endpoint.ID()
		id = &e
	}
	query := ""
	if u.RawQuery != "" {
		query = "?" + u.RawQuery
	}
	rec := &request{emit: s.emit, event: RequestEvent{Event: "request", Tunnel: sc.kind.tunnel, Host: u.Hostname(), Method: r.Method, Path: u.EscapedPath(), Query: query, Endpoint: id, Facts: generator.Facts{}}, started: started}
	req := generator.Request{Method: r.Method, URL: u, Header: r.Header}
	// status is the one sent once answered: a panic after it is recorded with it, not with a 500.
	status, answered := 0, false
	// A Handler that panics is the mock's own bug: a 500 that names it, or, once the response has
	// begun, a cut connection.
	defer func() {
		p := recover()
		if p == nil || p == http.ErrAbortHandler {
			if p != nil {
				panic(p)
			}
			return
		}
		why := fmt.Sprintf("the mock failed: %v", p)
		if answered {
			rec.record(&status, nil, RequestFailed, why)
			panic(http.ErrAbortHandler)
		}
		failure := dispatch.ErrorResponse(dispatch.Failure, req, why)
		rec.record(&failure.Status, nil, RequestFailed, why)
		for k, vs := range failure.Header {
			w.Header()[k] = vs
		}
		w.WriteHeader(failure.Status)
		io.Copy(w, failure.Body) //nolint:errcheck // the record is written; a client gone now changes nothing
	}()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		rec.record(nil, nil, Aborted, endedEarly(sc.client.endedBy(), bodyCutOff))
		return
	}
	req.Body = body
	d := dispatch.Dispatched{Verdict: dispatch.Verdict{Outcome: dispatch.Unmatched, Why: plainOnly}, Response: dispatch.ErrorResponse(dispatch.Refusal, req, plainOnly), Facts: generator.Facts{}}
	if authority == sc.kind.authority {
		d = s.handle(r.Context(), resolution, miss, req)
	}
	if r.Context().Err() != nil {
		rec.record(nil, d.Facts, Aborted, endedEarly(sc.client.endedBy(), notReady))
		return
	}
	// [LAW:single-enforcer] Every Handler's response passes here, so a response the server cannot
	// write is caught here, before its status is committed: the mock's own bug, a 500 that names it.
	if why := unwritable(d.Response); why != "" {
		why = "the handler gave " + why
		d = dispatch.Dispatched{Verdict: dispatch.Verdict{Outcome: dispatch.Failed, Why: why}, Response: dispatch.ErrorResponse(dispatch.Failure, req, why), Facts: d.Facts}
	}
	status = d.Response.Status
	for k, vs := range d.Response.Header {
		w.Header()[k] = vs
	}
	answered = true
	w.WriteHeader(status)
	// A response to HEAD has no body (RFC 9110 9.3.2): net/http refuses one, so none is delivered.
	if r.Method == http.MethodHead {
		rec.record(&status, d.Facts, RequestOutcome(d.Verdict.Outcome), d.Verdict.Why)
		return
	}
	switch delivery := deliver(r.Context(), w, d.Response.Body); {
	case delivery.refused != nil:
		rec.record(&status, d.Facts, RequestFailed, fmt.Sprintf("net/http refused the response body: %v", delivery.refused))
		panic(http.ErrAbortHandler)
	case delivery.sourceErr != nil:
		rec.record(&status, d.Facts, RequestFailed, fmt.Sprintf("response body failed: %v", delivery.sourceErr))
		// A broken stream, not a truncated one that looks whole: the connection is cut.
		panic(http.ErrAbortHandler)
	case delivery.abandoned:
		rec.record(&status, d.Facts, Aborted, endedEarly(sc.client.endedBy(), bodyUnfinished))
	default:
		rec.record(&status, d.Facts, RequestOutcome(d.Verdict.Outcome), d.Verdict.Why)
	}
}

type delivery struct {
	abandoned bool
	sourceErr error
	refused   error // a body net/http will not write: one on a status that has none, or past its Content-Length
}

// deliver copies body to the client unbuffered, flushing each chunk, so a stream reaches the client as
// it is produced.
func deliver(ctx context.Context, w http.ResponseWriter, body io.Reader) delivery {
	flusher := http.NewResponseController(w)
	buf := make([]byte, 32*1024)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				if errors.Is(werr, http.ErrBodyNotAllowed) || errors.Is(werr, http.ErrContentLength) {
					return delivery{refused: werr}
				}
				return delivery{abandoned: true}
			}
			if ferr := flusher.Flush(); ferr != nil {
				return delivery{abandoned: true}
			}
		}
		switch {
		case err == io.EOF:
			return delivery{}
		case err != nil && ctx.Err() != nil:
			return delivery{abandoned: true}
		case err != nil:
			return delivery{sourceErr: err}
		}
	}
}

// request is one request's record, which each path through ServeHTTP writes once.
type request struct {
	emit    func(Event)
	event   RequestEvent
	started time.Time
}

func (q *request) record(status *int, facts generator.Facts, outcome RequestOutcome, why string) {
	q.event.Status, q.event.Outcome, q.event.Why = status, outcome, why
	q.event.DurationMS = time.Since(q.started).Milliseconds()
	if facts != nil {
		q.event.Facts = facts
	}
	q.emit(q.event)
}

var endedBys = map[ClosedBy]string{ByClient: "client closed", ByStop: "the mock stopped", ByServer: "the mock closed the connection"}

func endedEarly(by ClosedBy, before string) string { return endedBys[by] + " before " + before }

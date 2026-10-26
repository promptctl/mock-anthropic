// Package endpoint is PROJECT.md "Endpoint table": every endpoint Claude Code is known to call, each
// in scope (a generator answers it) or out of scope (refused with its reason), and the resolver from
// a request to its row.
package endpoint

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/promptctl/mock-anthropic/internal/generator"
)

// ID names one row of a table.
type ID string

// Row is one endpoint as written in a table. Built only by InScope and OutOfScope, so an out-of-scope
// row cannot be written without its reason.
type Row struct {
	id     ID
	host   string
	method string
	path   string
	// reason is nil exactly when the row is in scope; Build refuses a blank out-of-scope reason.
	reason *string
	// generate is what answers an in-scope row, nil while it has none: a state `coverage` reports.
	generate generator.Func
}

// InScope is a row a generator answers; generate is nil for an in-scope row with no generator yet.
func InScope(id ID, host, method, path string, generate generator.Func) Row {
	return Row{id: id, host: host, method: method, path: path, generate: generate}
}

// OutOfScope is a row refused with its reason.
func OutOfScope(id ID, host, method, path, reason string) Row {
	return Row{id: id, host: host, method: method, path: path, reason: &reason}
}

func (r Row) ID() ID         { return r.id }
func (r Row) Host() string   { return r.host }
func (r Row) Method() string { return r.method }
func (r Row) Path() string   { return r.path }
func (r Row) InScope() bool  { return r.reason == nil }

// Generator is what answers the row, and false when it is out of scope or has no generator.
func (r Row) Generator() (generator.Func, bool) { return r.generate, r.generate != nil }

// OutOfScopeReason is why the row is refused, and false when it is in scope.
func (r Row) OutOfScopeReason() (string, bool) {
	if r.reason == nil {
		return "", false
	}
	return *r.reason, true
}

// segment is one segment of a path template. A literal matches its text exactly. A param matches any
// one segment that starts with prefix, ends with suffix, and has at least one character between them:
// `{org}` has neither, `{sha}.zip` has the suffix `.zip`.
type segment struct {
	param  bool
	text   string // literal only
	name   string // param only
	prefix string // param only
	suffix string // param only
}

// [LAW:one-source-of-truth] The one matching rule, for the resolver and the overlap check alike,
// applied to a decoded request segment.
func (s segment) matches(text string) bool {
	if !s.param {
		return s.text == text
	}
	return len(text) > len(s.prefix)+len(s.suffix) && strings.HasPrefix(text, s.prefix) && strings.HasSuffix(text, s.suffix)
}

// Whether one request segment could match both. Two params share a match exactly when their prefixes
// agree as far as the shorter one goes, and their suffixes too: the middle can then be made long
// enough for both.
func (s segment) overlaps(o segment) bool {
	switch {
	case !s.param:
		return o.matches(s.text)
	case !o.param:
		return s.matches(o.text)
	}
	prefixes := strings.HasPrefix(s.prefix, o.prefix) || strings.HasPrefix(o.prefix, s.prefix)
	suffixes := strings.HasSuffix(s.suffix, o.suffix) || strings.HasSuffix(o.suffix, s.suffix)
	return prefixes && suffixes
}

// Endpoint is a row whose template has been parsed.
type Endpoint struct {
	Row
	segments []segment
}

// Table exists only as Build's output, so the resolver never meets a table whose templates were not
// parsed or whose rows overlap. Its fields are unexported and nothing hands out its slices, so no row
// can be added or changed after the check.
type Table struct {
	endpoints []Endpoint
	hosts     []string
}

// Default is the mock's endpoint table.
var Default = mustBuild(rows)

func mustBuild(rows []Row) *Table {
	t, err := Build(rows)
	if err != nil {
		panic(err)
	}
	return t
}

var (
	paramSegment = regexp.MustCompile(`^([^{}]*)\{([a-z_]+)\}([^{}]*)$`)
	// A lowercase DNS name of two or more labels whose top-level label starts with a letter, so no IP
	// literal: what a request's Hostname() can equal and a leaf certificate can carry as a DNS SAN.
	hostname = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*\.[a-z]([a-z0-9-]*[a-z0-9])?$`)
	methods  = []string{"GET", "POST", "PUT", "PATCH", "DELETE"}
)

// [LAW:single-enforcer] The one checkpoint a table passes: every row parses, every id is unique, and
// no request can match two rows, which is what lets Resolve return the first match it finds.
func Build(rows []Row) (*Table, error) {
	var problems []error
	endpoints := make([]Endpoint, 0, len(rows))
	seen := map[ID]int{}
	for _, row := range rows {
		if seen[row.id]++; seen[row.id] == 2 {
			problems = append(problems, fmt.Errorf("duplicate endpoint id %s", row.id))
		}
		e, err := parseEndpoint(row)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		endpoints = append(endpoints, e)
	}
	for i, a := range endpoints {
		for _, b := range endpoints[i+1:] {
			if overlap(a, b) {
				problems = append(problems, fmt.Errorf("endpoints %s and %s overlap: %s %s %s and %s", a.id, b.id, a.method, a.host, a.path, b.path))
			}
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("endpoint table:\n%w", errors.Join(problems...))
	}
	var hosts []string
	for _, e := range endpoints {
		if !slices.Contains(hosts, e.host) {
			hosts = append(hosts, e.host)
		}
	}
	return &Table{endpoints: endpoints, hosts: hosts}, nil
}

func parseEndpoint(row Row) (Endpoint, error) {
	fail := func(format string, args ...any) (Endpoint, error) {
		return Endpoint{}, fmt.Errorf("endpoint %s: %s", row.id, fmt.Sprintf(format, args...))
	}
	if row.id == "" {
		return fail("empty id")
	}
	if !hostname.MatchString(row.host) {
		return fail("host %q is not a lowercase DNS name with no port", row.host)
	}
	if !slices.Contains(methods, row.method) {
		return fail("method %q is not one of %v", row.method, methods)
	}
	if row.reason != nil && strings.TrimSpace(*row.reason) == "" {
		return fail("an out-of-scope row needs a reason")
	}
	rest, ok := strings.CutPrefix(row.path, "/")
	if !ok {
		return fail("%s does not start with /", row.path)
	}
	var segments []segment
	names := map[string]bool{}
	for _, text := range strings.Split(rest, "/") {
		s, err := parseSegment(text)
		if err != nil {
			return fail("%s: %v", row.path, err)
		}
		if s.param {
			if names[s.name] {
				return fail("%s names {%s} twice", row.path, s.name)
			}
			names[s.name] = true
		}
		segments = append(segments, s)
	}
	return Endpoint{Row: row, segments: segments}, nil
}

func parseSegment(text string) (segment, error) {
	if text == "" {
		return segment{}, errors.New("empty segment")
	}
	s := segment{text: text}
	if strings.ContainsAny(text, "{}") {
		m := paramSegment.FindStringSubmatch(text)
		if m == nil {
			return segment{}, fmt.Errorf("segment %s is not one {name} of [a-z_] with fixed text around it", text)
		}
		s = segment{param: true, prefix: m[1], name: m[2], suffix: m[3]}
	}
	// A request segment is matched decoded and dot-normalized, and a WHATWG URL parser drops tab, LF
	// and CR and turns a backslash into a slash, so fixed text holding a delimiter, an escape, one of
	// those, or a dot segment is never what a request for it is matched as. Other control characters
	// would survive, but no endpoint Claude Code calls names one, so they are refused with the rest. A param stands for at least one
	// character, so `x` stands in for it here.
	sample := s.text
	if s.param {
		sample = s.prefix + "x" + s.suffix
	}
	if strings.ContainsAny(sample, `%?#\`) || strings.ContainsFunc(sample, unicode.IsControl) || sample == "." || sample == ".." {
		return segment{}, fmt.Errorf("segment %s is not what a request for it is matched as", text)
	}
	return s, nil
}

func overlap(a, b Endpoint) bool {
	if a.host != b.host || a.method != b.method || len(a.segments) != len(b.segments) {
		return false
	}
	for i := range a.segments {
		if !a.segments[i].overlaps(b.segments[i]) {
			return false
		}
	}
	return true
}

// Hosts is every host the table knows, in table order: the hosts a server given that table accepts
// CONNECT for and puts on its leaf certificate.
func (t *Table) Hosts() []string { return slices.Clone(t.hosts) }

// Endpoints is every endpoint of the table, in table order.
func (t *Table) Endpoints() []Endpoint { return slices.Clone(t.endpoints) }

// Resolution is the endpoint a request is addressed to, and each of its params with the value its
// segment gave it, percent-decoded and its prefix and suffix removed.
type Resolution struct {
	Endpoint Endpoint
	Params   map[string]string
}

// MissReason is which way a request missed every endpoint.
type MissReason string

const (
	// NoEndpoint: no endpoint has the request's host and path.
	NoEndpoint MissReason = "no endpoint"
	// WrongMethod: endpoints have the request's host and path, none its method.
	WrongMethod MissReason = "wrong method"
	// Undecodable: a path segment's escapes do not decode to UTF-8.
	Undecodable MissReason = "undecodable escape"
)

// Miss is the error Resolve returns: why a request is addressed to no endpoint, for the 400 that
// answers it. [LAW:one-source-of-truth] The resolver is the one that knows, so nothing re-matches to
// explain its answer.
type Miss struct {
	Reason MissReason
	why    string
}

func (m *Miss) Error() string { return m.why }

// Resolve finds the endpoint a request is addressed to: its method, the host it is addressed to (any
// case, any port, a trailing root dot; the server passes the connection's authority, the CONNECT
// target or API, never a header the client chose), and its escaped path. Each path segment is
// percent-decoded, then dot segments are removed as a WHATWG URL parser removes them, before it is
// matched. The error is a *Miss when no endpoint matches.
func (t *Table) Resolve(method, host, escapedPath string) (Resolution, error) {
	host = strings.TrimSuffix(strings.ToLower((&url.URL{Host: host}).Hostname()), ".")
	rest, ok := strings.CutPrefix(escapedPath, "/")
	if !ok {
		return Resolution{}, &Miss{NoEndpoint, fmt.Sprintf("no endpoint: path %q does not start with /", escapedPath)}
	}
	raw := strings.Split(rest, "/")
	texts := make([]string, 0, len(raw))
	for i, r := range raw {
		text, err := url.PathUnescape(r)
		if err != nil || !utf8.ValidString(text) {
			return Resolution{}, &Miss{Undecodable, fmt.Sprintf("path segment %q does not decode to UTF-8", r)}
		}
		if text != "." && text != ".." {
			texts = append(texts, text)
			continue
		}
		if text == ".." && len(texts) > 0 {
			texts = texts[:len(texts)-1]
		}
		// A trailing dot segment leaves the directory it names: /a/b/.. is /a/.
		if i == len(raw)-1 {
			texts = append(texts, "")
		}
	}
	var methods []string
	for _, e := range t.endpoints {
		if e.host != host || len(e.segments) != len(texts) {
			continue
		}
		params, ok := e.match(texts)
		switch {
		case !ok:
		case e.method == method:
			return Resolution{Endpoint: e, Params: params}, nil
		default:
			methods = append(methods, e.method)
		}
	}
	if len(methods) > 0 {
		return Resolution{}, &Miss{WrongMethod, fmt.Sprintf("no endpoint answers %s here; this path answers %s", method, strings.Join(methods, ", "))}
	}
	return Resolution{}, &Miss{NoEndpoint, "no endpoint"}
}

func (e Endpoint) match(texts []string) (map[string]string, bool) {
	for i, s := range e.segments {
		if !s.matches(texts[i]) {
			return nil, false
		}
	}
	params := map[string]string{}
	for i, s := range e.segments {
		if s.param {
			params[s.name] = texts[i][len(s.prefix) : len(texts[i])-len(s.suffix)]
		}
	}
	return params, true
}

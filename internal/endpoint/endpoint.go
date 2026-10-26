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
	"unicode/utf8"
)

// ID names one row of a table.
type ID string

// Row is one endpoint as written in a table. Built only by inScope and outOfScope, so an out-of-scope
// row cannot be written without its reason.
type Row struct {
	id     ID
	host   string
	method string
	path   string
	// reason is empty exactly when the row is in scope; Build refuses a blank out-of-scope reason.
	reason     string
	outOfScope bool
}

func inScope(id ID, host, method, path string) Row {
	return Row{id: id, host: host, method: method, path: path}
}

func outOfScope(id ID, host, method, path, reason string) Row {
	return Row{id: id, host: host, method: method, path: path, reason: reason, outOfScope: true}
}

func (r Row) ID() ID         { return r.id }
func (r Row) Host() string   { return r.host }
func (r Row) Method() string { return r.method }
func (r Row) Path() string   { return r.path }
func (r Row) InScope() bool  { return !r.outOfScope }

// OutOfScopeReason is why the row is refused, and false when it is in scope.
func (r Row) OutOfScopeReason() (string, bool) { return r.reason, r.outOfScope }

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
	methods      = []string{"GET", "POST", "PUT", "PATCH", "DELETE"}
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
	return &Table{endpoints: endpoints}, nil
}

func parseEndpoint(row Row) (Endpoint, error) {
	fail := func(format string, args ...any) (Endpoint, error) {
		return Endpoint{}, fmt.Errorf("endpoint %s: %s", row.id, fmt.Sprintf(format, args...))
	}
	if row.id == "" {
		return fail("empty id")
	}
	if row.host == "" || strings.ToLower(row.host) != row.host || strings.ContainsAny(row.host, ":/") {
		return fail("host %q is not lowercase with no port", row.host)
	}
	if !slices.Contains(methods, row.method) {
		return fail("method %q is not one of %v", row.method, methods)
	}
	if row.outOfScope && strings.TrimSpace(row.reason) == "" {
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
	// A request segment is matched decoded, so fixed text holding a delimiter or an escape, or a dot
	// segment, is never what a request for it is matched as. A param stands for at least one
	// character, so `x` stands in for it here.
	sample := s.text
	if s.param {
		sample = s.prefix + "x" + s.suffix
	}
	if strings.ContainsAny(sample, "%?#") || sample == "." || sample == ".." {
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
func (t *Table) Hosts() []string {
	var hosts []string
	for _, e := range t.endpoints {
		if !slices.Contains(hosts, e.host) {
			hosts = append(hosts, e.host)
		}
	}
	return hosts
}

// Endpoints is every endpoint of the table, in table order.
func (t *Table) Endpoints() []Endpoint { return slices.Clone(t.endpoints) }

// Resolution is the endpoint a request is addressed to, and each of its params with the value its
// segment gave it, percent-decoded and its prefix and suffix removed.
type Resolution struct {
	Endpoint Endpoint
	Params   map[string]string
}

// Resolve finds the endpoint a request is addressed to: the method, u's host (any case, any port) and
// u's path (its query ignored), each path segment percent-decoded before it is matched. False when
// no endpoint matches, including when a segment's escapes do not decode to UTF-8.
func (t *Table) Resolve(method string, u *url.URL) (Resolution, bool) {
	host := strings.ToLower(u.Hostname())
	rest, ok := strings.CutPrefix(u.EscapedPath(), "/")
	if !ok {
		return Resolution{}, false
	}
	raw := strings.Split(rest, "/")
	texts := make([]string, len(raw))
	for i, r := range raw {
		text, err := url.PathUnescape(r)
		if err != nil || !utf8.ValidString(text) {
			return Resolution{}, false
		}
		texts[i] = text
	}
	for _, e := range t.endpoints {
		if e.host != host || e.method != method || len(e.segments) != len(texts) {
			continue
		}
		if params, ok := e.match(texts); ok {
			return Resolution{Endpoint: e, Params: params}, true
		}
	}
	return Resolution{}, false
}

func (e Endpoint) match(texts []string) (map[string]string, bool) {
	params := map[string]string{}
	for i, s := range e.segments {
		text := texts[i]
		if !s.matches(text) {
			return nil, false
		}
		if s.param {
			params[s.name] = text[len(s.prefix) : len(text)-len(s.suffix)]
		}
	}
	return params, true
}

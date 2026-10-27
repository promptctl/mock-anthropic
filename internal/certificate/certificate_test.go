package certificate

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/promptctl/mock-anthropic/internal/account"
	"github.com/promptctl/mock-anthropic/internal/endpoint"
)

var hosts = endpoint.For(account.Default()).Hosts()

func ensure(t *testing.T, dir string, hosts []string) Certificates {
	t.Helper()
	certs, err := Ensure(dir, hosts)
	if err != nil {
		t.Fatal(err)
	}
	return certs
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func parse(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(read(t, path))
	if block == nil {
		t.Fatalf("%s holds no PEM block", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func verify(t *testing.T, certs Certificates) {
	t.Helper()
	out, err := exec.Command("openssl", "verify", "-CAfile", certs.CA.CertPath, certs.Leaf.CertPath).CombinedOutput()
	if want := certs.Leaf.CertPath + ": OK"; err != nil || strings.TrimSpace(string(out)) != want {
		t.Fatalf("openssl verify: %v: %q, want %q", err, out, want)
	}
}

// handshake is what a client that trusts only the CA, as Claude Code does with NODE_EXTRA_CA_CERTS,
// says of the leaf presented as host: nil when it accepts it.
func handshake(t *testing.T, certs Certificates, host string) error {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(certs.Leaf.CertPath, certs.Leaf.KeyPath)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{pair}})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	// The client's verdict is the result; the server's side of a refused handshake is ignored.
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			conn.(*tls.Conn).Handshake()
			conn.Close()
		}
	}()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(read(t, certs.CA.CertPath))
	conn, err := tls.Dial("tcp", listener.Addr().String(), &tls.Config{RootCAs: pool, ServerName: host})
	if err == nil {
		conn.Close()
	}
	return err
}

func TestFirstAndLaterStarts(t *testing.T) {
	dir := t.TempDir()
	first := ensure(t, dir, hosts)

	t.Run("a first start generates the CA and the leaf", func(t *testing.T) {
		if first.CA.Outcome != Generated || first.Leaf.Outcome != Generated {
			t.Fatalf("outcomes %s, %s", first.CA.Outcome, first.Leaf.Outcome)
		}
	})

	t.Run("openssl verify accepts the leaf against the CA", func(t *testing.T) { verify(t, first) })

	t.Run("the leaf's SANs are exactly the endpoint table's hosts", func(t *testing.T) {
		if got := parse(t, first.Leaf.CertPath).DNSNames; !slices.Equal(got, hosts) {
			t.Fatalf("SANs %v, want %v", got, hosts)
		}
	})

	t.Run("a client trusting only the CA accepts the leaf for every host in the table", func(t *testing.T) {
		for _, host := range hosts {
			if err := handshake(t, first, host); err != nil {
				t.Errorf("%s: %v", host, err)
			}
		}
	})

	t.Run("a client rejects the leaf for a host outside the table", func(t *testing.T) {
		var invalid x509.HostnameError
		if err := handshake(t, first, "example.invalid"); !errors.As(err, &invalid) {
			t.Fatalf("got %v, want a hostname error", err)
		}
	})

	t.Run("a second start reuses both, byte for byte", func(t *testing.T) {
		paths := []string{first.CA.CertPath, first.CA.KeyPath, first.Leaf.CertPath, first.Leaf.KeyPath}
		var before [][]byte
		for _, path := range paths {
			before = append(before, read(t, path))
		}
		second := ensure(t, dir, hosts)
		want := first
		want.CA.Outcome, want.Leaf.Outcome = Reused, Reused
		if second != want {
			t.Fatalf("got %+v, want %+v", second, want)
		}
		for i, path := range paths {
			if !bytes.Equal(read(t, path), before[i]) {
				t.Errorf("%s changed", path)
			}
		}
	})

	t.Run("a changed host set keeps the CA and generates a leaf for the new hosts", func(t *testing.T) {
		changedHosts := []string{"api.anthropic.com", "added.mock-anthropic.invalid"}
		changed := ensure(t, dir, changedHosts)
		wantCA := first.CA
		wantCA.Outcome = Reused
		if changed.CA != wantCA || changed.Leaf.Outcome != Generated || changed.Leaf.CertPath == first.Leaf.CertPath {
			t.Fatalf("got %+v", changed)
		}
		if got := parse(t, changed.Leaf.CertPath).DNSNames; !slices.Equal(got, changedHosts) {
			t.Fatalf("SANs %v, want %v", got, changedHosts)
		}
		verify(t, changed)
	})

	t.Run("the keys, and the directories holding them, are their owner's alone", func(t *testing.T) {
		for _, path := range []string{first.CA.KeyPath, filepath.Dir(first.CA.KeyPath), first.Leaf.KeyPath, filepath.Dir(first.Leaf.KeyPath)} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if mode := info.Mode().Perm() & 0o077; mode != 0 {
				t.Errorf("%s is %v", path, info.Mode().Perm())
			}
		}
	})
}

func TestRelativeAndAbsoluteDirsNameOneLeaf(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	relative := ensure(t, "certs", hosts)
	verify(t, relative)
	absolute := ensure(t, filepath.Join(root, "certs"), hosts)
	if absolute.Leaf.Outcome != Reused {
		t.Fatalf("leaf through the absolute path: %s, want %s", absolute.Leaf.Outcome, Reused)
	}
}

func TestTwoStartsAtOnceAgree(t *testing.T) {
	dir := t.TempDir()
	var results [2]Certificates
	var errs [2]error
	var wg sync.WaitGroup
	for i := range results {
		wg.Go(func() { results[i], errs[i] = Ensure(dir, hosts) })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	a, b := results[0], results[1]
	if a.CA.CertPath != b.CA.CertPath || a.Leaf.CertPath != b.Leaf.CertPath {
		t.Fatalf("disagree: %+v, %+v", a, b)
	}
	// Exactly one of the two published each certificate; the other found it or lost the race to it.
	for _, outcomes := range [][2]Outcome{{a.CA.Outcome, b.CA.Outcome}, {a.Leaf.Outcome, b.Leaf.Outcome}} {
		if (outcomes[0] == Generated) == (outcomes[1] == Generated) {
			t.Errorf("outcomes %v, want exactly one generated", outcomes)
		}
	}
	verify(t, Certificates{CA: a.CA, Leaf: b.Leaf})
	// No scratch directory is left behind.
	want := []string{filepath.Base(filepath.Dir(a.CA.CertPath)), filepath.Base(filepath.Dir(a.Leaf.CertPath))}
	if got := names(t, dir); !slices.Equal(got, want) {
		t.Fatalf("dir holds %v, want %v", got, want)
	}
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestOverlappingBuildsOfOneName(t *testing.T) {
	dir := t.TempDir()
	// Neither build finishes until both have started, so both have found the name unpublished.
	var started sync.WaitGroup
	started.Add(2)
	build := func(text string) func(string) error {
		return func(scratch string) error {
			started.Done()
			started.Wait()
			return os.WriteFile(filepath.Join(scratch, "built-by"), []byte(text), 0o600)
		}
	}
	type result struct {
		path    string
		outcome Outcome
	}
	var results [2]result
	var wg sync.WaitGroup
	for i, text := range []string{"a", "b"} {
		wg.Go(func() {
			path, outcome, err := publish(dir, "x", []byte("recipe"), build(text))
			if err != nil {
				t.Error(err)
			}
			results[i] = result{path, outcome}
		})
	}
	wg.Wait()
	a, b := results[0], results[1]
	outcomes := []Outcome{a.outcome, b.outcome}
	slices.Sort(outcomes)
	if !slices.Equal(outcomes, []Outcome{Generated, LostRace}) || a.path != b.path {
		t.Fatalf("got %+v, %+v", a, b)
	}
	winner := map[Outcome]string{a.outcome: "a", b.outcome: "b"}[Generated]
	if got := string(read(t, filepath.Join(a.path, "built-by"))); got != winner {
		t.Fatalf("published build is %q, want the winner's %q", got, winner)
	}
	if got := names(t, dir); !slices.Equal(got, []string{filepath.Base(a.path)}) {
		t.Fatalf("dir holds %v", got)
	}
}

func TestDifferentRecipesPublishUnderDifferentNames(t *testing.T) {
	dir := t.TempDir()
	build := func(scratch string) error { return os.WriteFile(filepath.Join(scratch, "f"), nil, 0o600) }
	a, _, errA := publish(dir, "x", []byte("one"), build)
	b, _, errB := publish(dir, "x", []byte("two"), build)
	if errA != nil || errB != nil || a == b {
		t.Fatalf("%s %v, %s %v", a, errA, b, errB)
	}
}

func TestFailingOpensslFailsTheStartAndNamesItsCommand(t *testing.T) {
	// A leaf with no hosts has an empty subjectAltName, which openssl refuses.
	_, err := Ensure(t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "openssl x509 -req ") {
		t.Fatalf("got %v", err)
	}
}

// A terminal's Ctrl-C signals the foreground process group; openssl outside it survives the signal
// that asks serve to stop gracefully.
func TestOpensslRunsOutsideTheCallersProcessGroup(t *testing.T) {
	bin, out := t.TempDir(), filepath.Join(t.TempDir(), "pgid")
	script := "#!/bin/sh\nps -o pgid= -p $$ > " + out + "\n"
	if err := os.WriteFile(filepath.Join(bin, "openssl"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := openssl(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(read(t, out)))
	if own := strconv.Itoa(syscall.Getpgrp()); got == "" || got == own {
		t.Fatalf("openssl's process group %q, the caller's %q: want different", got, own)
	}
}

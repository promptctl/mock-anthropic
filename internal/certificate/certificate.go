// Package certificate is PROJECT.md "How a program reaches it": the CA a consumer trusts through
// NODE_EXTRA_CA_CERTS, and the leaf the mock presents for every host it knows, generated with
// `openssl` on first start and reused afterwards.
package certificate

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// Outcome is what one start did for one certificate. [LAW:nothing-unseen] It is reported in the
// start's own record.
type Outcome string

const (
	// Generated: this start built the certificate and published it.
	Generated Outcome = "generated"
	// Reused: the certificate was already published.
	Reused Outcome = "reused"
	// LostRace: this start built it, but a start running at the same moment published first; this
	// start discarded its own and uses the winner's.
	LostRace Outcome = "lost-race"
)

// Issued is one published certificate and its private key.
type Issued struct {
	CertPath string
	KeyPath  string
	Outcome  Outcome
}

// Certificates is the CA and the leaf it signed.
type Certificates struct {
	CA   Issued
	Leaf Issued
}

// recipe is what a certificate is built from, run in its own directory: openssl.cnf holds Config,
// each Prepare command runs, then Issue writes cert.pem and key.pem. Exported fields only so the
// recipe's JSON, which names its directory, is all of it.
type recipe struct {
	Config  string
	Prepare [][]string
	Issue   []string
}

const (
	keySpec = "rsa:2048"
	days    = "3650"
)

// Config files, not `-addext` or `-copy_extensions`: LibreSSL, the openssl macOS ships, has neither.
const caConfig = `[req]
distinguished_name = dn
prompt = no
[dn]
CN = mock-anthropic CA
[ca]
basicConstraints = critical, CA:true
keyUsage = critical, keyCertSign, cRLSign
subjectKeyIdentifier = hash
`

var caRecipe = recipe{
	Config: caConfig,
	Issue:  []string{"req", "-x509", "-newkey", keySpec, "-nodes", "-days", days, "-config", "openssl.cnf", "-extensions", "ca", "-keyout", "key.pem", "-out", "cert.pem"},
}

// leafRecipe is the leaf for hosts, signed by the CA whose files signer names.
func leafRecipe(hosts []string, signer Issued) recipe {
	sans := make([]string, len(hosts))
	for i, host := range hosts {
		sans[i] = "DNS:" + host
	}
	return recipe{
		Config: `[req]
distinguished_name = dn
prompt = no
[dn]
CN = mock-anthropic
[leaf]
basicConstraints = critical, CA:false
keyUsage = critical, digitalSignature, keyEncipherment
extendedKeyUsage = serverAuth
subjectAltName = ` + strings.Join(sans, ", ") + `
subjectKeyIdentifier = hash
authorityKeyIdentifier = keyid
`,
		Prepare: [][]string{{"req", "-new", "-newkey", keySpec, "-nodes", "-config", "openssl.cnf", "-keyout", "key.pem", "-out", "request.csr"}},
		Issue:   []string{"x509", "-req", "-in", "request.csr", "-days", days, "-extfile", "openssl.cnf", "-extensions", "leaf", "-CA", signer.CertPath, "-CAkey", signer.KeyPath, "-out", "cert.pem"},
	}
}

// [LAW:no-silent-failure] A failing openssl is an error carrying what it was asked and what it said.
func openssl(dir string, args ...string) error {
	cmd := exec.Command("openssl", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("openssl %s in %s: %w: %s", strings.Join(args, " "), dir, err, stderr.String())
	}
	return nil
}

// [LAW:no-ambient-temporal-coupling] [LAW:one-type-per-behavior] The one way a certificate reaches
// dir. Its name is kind and a digest of key, everything the build depends on, so a changed recipe
// names a directory not yet built and nothing on disk is ever checked for staleness. It is built in
// a scratch directory, then renamed into place whole, so a published name is always complete; when
// two starts build one name at once the rename decides between them: renaming onto a published,
// non-empty directory fails and leaves it untouched, and the loser uses the winner's.
func publish(dir, kind string, key []byte, build func(scratch string) error) (string, Outcome, error) {
	sum := sha256.Sum256(key)
	path := filepath.Join(dir, kind+"-"+hex.EncodeToString(sum[:])[:16])
	switch _, err := os.Stat(path); {
	case err == nil:
		return path, Reused, nil
	case !errors.Is(err, os.ErrNotExist):
		return "", "", err
	}
	scratch, err := os.MkdirTemp(dir, ".tmp-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(scratch)
	if err := build(scratch); err != nil {
		return "", "", err
	}
	switch err := os.Rename(scratch, path); {
	case err == nil:
		return path, Generated, nil
	case errors.Is(err, syscall.ENOTEMPTY) || errors.Is(err, syscall.EEXIST):
		return path, LostRace, nil
	default:
		return "", "", err
	}
}

// [LAW:one-source-of-truth] The directory is named by the recipe it runs plus basis, whatever else
// the result depends on, so what is built cannot drift from what names it. The serial is the one
// input outside the recipe: every issue gets a fresh one. The key is made owner-only: LibreSSL
// writes it 0644, and a copy of the directory made without its 0700 mode would otherwise publish it.
func issue(dir, kind, basis string, r recipe) (Issued, error) {
	key, err := json.Marshal(r)
	if err != nil {
		return Issued{}, err
	}
	path, outcome, err := publish(dir, kind, append([]byte(basis+"\x00"), key...), func(scratch string) error {
		if err := os.WriteFile(filepath.Join(scratch, "openssl.cnf"), []byte(r.Config), 0o600); err != nil {
			return err
		}
		for _, command := range r.Prepare {
			if err := openssl(scratch, command...); err != nil {
				return err
			}
		}
		serial := make([]byte, 16)
		rand.Read(serial)
		if err := openssl(scratch, append(r.Issue, "-set_serial", "0x"+hex.EncodeToString(serial))...); err != nil {
			return err
		}
		return os.Chmod(filepath.Join(scratch, "key.pem"), 0o600)
	})
	if err != nil {
		return Issued{}, fmt.Errorf("%s certificate: %w", kind, err)
	}
	return Issued{CertPath: filepath.Join(path, "cert.pem"), KeyPath: filepath.Join(path, "key.pem"), Outcome: outcome}, nil
}

// Ensure returns the CA in dir, then a leaf for exactly hosts signed by it, generating whichever is
// not there yet. [LAW:effects-at-boundaries] The leaf's basis is the CA's fingerprint, so a replaced
// CA gets a new leaf too.
func Ensure(dir string, hosts []string) (Certificates, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Certificates{}, err
	}
	ca, err := issue(dir, "ca", "", caRecipe)
	if err != nil {
		return Certificates{}, err
	}
	fingerprint, err := fingerprint(ca.CertPath)
	if err != nil {
		return Certificates{}, err
	}
	leaf, err := issue(dir, "leaf", fingerprint, leafRecipe(hosts, ca))
	if err != nil {
		return Certificates{}, err
	}
	return Certificates{CA: ca, Leaf: leaf}, nil
}

// fingerprint is the SHA-256 of the DER certificate in the PEM file at path.
func fingerprint(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", fmt.Errorf("%s holds no PEM certificate", path)
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:]), nil
}

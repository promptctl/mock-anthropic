// Package login is PROJECT.md "Commands" `login`: it writes a Claude Code config directory that the mock
// accepts as logged in. It is the one definition of a mock login, and a second login into the same
// directory replaces the first whole. The Claude Code behavior cited below is 2.1.88's source, checked
// against the 2.1.289 binary.
package login

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/promptctl/mock-anthropic/internal/account"
	"github.com/promptctl/mock-anthropic/internal/cmdline"
	"github.com/promptctl/mock-anthropic/internal/command"
	"github.com/promptctl/mock-anthropic/internal/oauth"
)

const (
	CredentialsFile = ".credentials.json"
	ConfigFile      = ".claude.json"
	// LegacyConfigFile is an older global config. Claude Code 2.1.289 reads it in place of ConfigFile
	// whenever the config dir holds one (its getGlobalClaudeFile), so a login removes it.
	LegacyConfigFile = ".config.json"
)

// File is one file of a login, its name in the config directory and its content.
type File struct {
	Name  string
	Bytes []byte
}

// Files is [LAW:effects-at-boundaries] pure: the files of a login, given its account, the trust keys of
// its projects (see TrustKey), and the time it is written.
func Files(a account.Account, trusted []string, now time.Time) ([]File, error) {
	// No `theme` in the config: Claude Code lays the file over its defaults, which hold one
	// (utils/config.ts:590, :1439), so `hasCompletedOnboarding` alone ends onboarding
	// (interactiveHelpers.tsx:111).
	projects := make(map[string]map[string]bool, len(trusted))
	for _, key := range trusted {
		projects[key] = map[string]bool{"hasTrustDialogAccepted": true}
	}
	credentials := map[string]any{"claudeAiOauth": map[string]any{
		"accessToken":      oauth.AccessToken,
		"refreshToken":     oauth.RefreshToken,
		"expiresAt":        now.Add(oauth.TokenLifetime).UnixMilli(),
		"scopes":           strings.Fields(oauth.Scopes),
		"subscriptionType": a.SubscriptionType(),
		"rateLimitTier":    nil,
	}}
	config := map[string]any{
		"hasCompletedOnboarding": true,
		// Both keys: 2.1.299 downloads the official marketplace archive on first start unless it
		// finds the install recorded as done, and that download is out of scope (mock-mock-flu.coa.3gi).
		// The attempted flag alone retries it on 2.1.88 (officialMarketplaceStartupCheck.ts:76-117).
		"officialMarketplaceAutoInstallAttempted": true,
		"officialMarketplaceAutoInstalled":        true,
		"oauthAccount": map[string]any{
			"accountUuid":      a.AccountUUID(),
			"emailAddress":     a.EmailAddress(),
			"organizationUuid": a.OrganizationUUID(),
			"organizationName": a.OrganizationName(),
		},
		"projects": projects,
	}
	var files []File
	for _, f := range []struct {
		name  string
		value any
	}{{CredentialsFile, credentials}, {ConfigFile, config}} {
		b, err := json.MarshalIndent(f.value, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("encoding %s: %w", f.name, err)
		}
		files = append(files, File{f.name, append(b, '\n')})
	}
	return files, nil
}

// TrustKey is [LAW:one-source-of-truth] a project's key in `projects`: Claude Code's own name for its
// working directory, the real path, NFC-normalized (bootstrap/state.ts:271, utils/Shell.ts:447). On
// macOS a project under /tmp is keyed /private/tmp/..., and any other spelling is never looked up.
func TrustKey(dir string) (string, error) {
	path, err := cmdline.Directory("--trust", dir)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("--trust %s: %w", path, err)
	}
	return norm.NFC.String(real), nil
}

// KeychainService is the macOS keychain entry Claude Code keeps a config dir's credential in: named for
// the NFC CLAUDE_CONFIG_DIR string (utils/secureStorage/macOsKeychainHelpers.ts:29), and read ahead of
// `.credentials.json` (fallbackStorage.ts).
func KeychainService(configDir string) string {
	sum := sha256.Sum256([]byte(configDir))
	return "Claude Code-credentials-" + hex.EncodeToString(sum[:])[:8]
}

// securityNoEntry is the exit status of `security` when no entry is left.
const securityNoEntry = 44

// removeKeychainEntries removes every keychain entry of service, and returns how many it removed, or nil
// where Claude Code keeps credentials only in the file. Claude Code moves the credential into the
// keychain the first time it saves one (a refresh, an MCP login) and deletes the file, so an entry from
// an earlier session would outrank the file this login writes; removing it is what makes the file the
// login. [LAW:no-silent-failure] Any exit but "none left" fails the login.
func removeKeychainEntries(ctx context.Context, service string) (*int, error) {
	// [LAW:dataflow-not-control-flow] The platform is Claude Code's own discriminator: only on darwin
	// does it read a keychain (utils/secureStorage/index.ts).
	if runtime.GOOS != "darwin" {
		return nil, nil
	}
	for removed := 0; ; removed++ {
		out, err := exec.CommandContext(ctx, "security", "delete-generic-password", "-s", service).CombinedOutput()
		var exit *exec.ExitError
		switch {
		case err == nil:
		case errors.As(err, &exit) && exit.ExitCode() == securityNoEntry:
			return &removed, nil
		default:
			return nil, fmt.Errorf("security delete-generic-password -s %s: %w: %s", service, err, out)
		}
	}
}

// configDirectory makes the config dir if it is not there; a path that names something else is a wrong
// command line. It returns whether it made it.
func configDirectory(path string) (bool, error) {
	notDirectory := &command.UsageError{Why: fmt.Sprintf("<config-dir> %s: not a directory", path)}
	info, err := os.Stat(path)
	switch {
	case err == nil && info.IsDir():
		return false, nil
	case err == nil, errors.Is(err, syscall.ENOTDIR):
		return false, notDirectory
	case !errors.Is(err, fs.ErrNotExist):
		return false, err
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		// EEXIST: a dangling symlink, which Stat reads as absent and MkdirAll cannot replace.
		if errors.Is(err, syscall.ENOTDIR) || errors.Is(err, fs.ErrExist) {
			return false, notDirectory
		}
		return false, err
	}
	return true, nil
}

// Written is what a login did to one file.
type Written string

const (
	Created  Written = "created"
	Replaced Written = "replaced"
)

// writeWhole writes every file whole beside its target before any is renamed over one, so a failed write
// replaces nothing (a failed rename can leave an earlier file replaced: the login fails, and the next
// replaces both), and a Claude Code reading the directory never sees half a file. A replaced file takes
// the mode, not keeps the old file's: 0600, as Claude Code writes both files itself (utils/config.ts:1139,
// plainTextStorage.ts:61).
func writeWhole(dir string, files []File) (map[string]Written, error) {
	type staged struct {
		File
		path, temporary string
	}
	var all []staged
	defer func() {
		// A renamed temporary is gone already; any other is removed, whether or not the login failed.
		for _, s := range all {
			os.Remove(s.temporary)
		}
	}()
	for _, f := range files {
		t, err := os.CreateTemp(dir, f.Name+".*.tmp") // 0600
		if err != nil {
			return nil, err
		}
		all = append(all, staged{f, filepath.Join(dir, f.Name), t.Name()})
		_, werr := t.Write(f.Bytes)
		if cerr := t.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return nil, fmt.Errorf("writing %s: %w", t.Name(), werr)
		}
	}
	outcomes := map[string]Written{}
	for _, s := range all {
		if _, err := os.Lstat(s.path); err == nil {
			outcomes[s.Name] = Replaced
		} else if errors.Is(err, fs.ErrNotExist) {
			outcomes[s.Name] = Created
		} else {
			return nil, err
		}
		if err := os.Rename(s.temporary, s.path); err != nil {
			// Files renamed before this one are already the new login; name them so the failure is not a mystery.
			return nil, fmt.Errorf("replacing %s (already replaced: %v): %w", s.Name, outcomes, err)
		}
	}
	return outcomes, nil
}

func removeLegacyConfig(dir string) (string, error) {
	err := os.Remove(filepath.Join(dir, LegacyConfigFile))
	switch {
	case err == nil:
		return "removed", nil
	case errors.Is(err, fs.ErrNotExist):
		return "absent", nil
	}
	return "", err
}

// Command is login, with the time it writes taken from now.
func Command(now func() time.Time) command.Command {
	return command.Command{
		Kind:  command.Task,
		Usage: "<config-dir> [--trust <project-dir>]... [--account FILE]",
		Run: func(ctx context.Context, args []string, _ func(error), learn func(command.Facts)) (command.Result, error) {
			trust, accountFlag := cmdline.List{}, &cmdline.Once{}
			rest, err := cmdline.Flags("login", func(fs *flag.FlagSet) {
				fs.Var(&trust, "trust", "")
				fs.Var(accountFlag, "account", "")
			}, args)
			if err != nil {
				return command.Result{}, err
			}
			if len(rest) == 0 {
				return command.Result{}, &command.UsageError{Why: "missing <config-dir>"}
			}
			if len(rest) > 1 {
				return command.Result{}, &command.UsageError{Why: "unexpected argument: " + rest[1]}
			}
			// NFC, so the keychain name matches what Claude Code reads.
			configDir, err := cmdline.ConfigPath("<config-dir>", rest[0])
			if err != nil {
				return command.Result{}, err
			}
			// One entry per project, however many spellings of it were given: `projects` holds one key for it.
			seen, trusted := map[string]bool{}, []string{}
			for _, dir := range trust {
				key, err := TrustKey(dir)
				if err != nil {
					return command.Result{}, err
				}
				if !seen[key] {
					seen[key] = true
					trusted = append(trusted, key)
				}
			}
			a, accountPath := account.Default(), (*string)(nil)
			if accountFlag.Value != nil {
				p, err := cmdline.Absolute("--account", *accountFlag.Value)
				if err != nil {
					return command.Result{}, err
				}
				read, err := account.ReadFile(p)
				var input *account.InputError
				if errors.As(err, &input) {
					return command.Result{}, &command.UsageError{Why: input.Error()}
				}
				if err != nil {
					return command.Result{}, err
				}
				a, accountPath = read, &p
			}
			created, err := configDirectory(configDir)
			if err != nil {
				return command.Result{}, err
			}
			files, err := Files(a, trusted, now())
			if err != nil {
				return command.Result{}, err
			}
			written, err := writeWhole(configDir, files)
			if err != nil {
				return command.Result{}, err
			}
			legacy, err := removeLegacyConfig(configDir)
			if err != nil {
				return command.Result{}, err
			}
			// Last: the keychain entry is an earlier login's, removed only once this one is written.
			keychain, err := removeKeychainEntries(ctx, KeychainService(configDir))
			if err != nil {
				return command.Result{}, err
			}
			outcomes := map[string]string{LegacyConfigFile: legacy}
			for name, w := range written {
				outcomes[name] = string(w)
			}
			// [LAW:nothing-unseen] Where the login went and whether it made the directory, which account
			// won (nil: the default), what is trusted, what this login replaced or removed, and how many
			// keychain entries it removed (nil: Claude Code uses none here).
			facts := command.Facts{
				"config_dir":         configDir,
				"config_dir_created": created,
				"account_file":       accountPath,
				"trusted":            trusted,
				"files":              outcomes,
				"keychain_removed":   keychain,
			}
			learn(facts)
			return command.Result{Answer: command.OK, Facts: facts}, nil
		},
	}
}

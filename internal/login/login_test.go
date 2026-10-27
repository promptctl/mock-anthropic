package login

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/promptctl/mock-anthropic/internal/account"
	"github.com/promptctl/mock-anthropic/internal/command"
	"github.com/promptctl/mock-anthropic/internal/oauth"
)

var fixed = time.UnixMilli(1_700_000_000_000)

func login(t *testing.T, args ...string) (command.Result, error) {
	t.Helper()
	return Command(func() time.Time { return fixed }).Run(context.Background(), args, func(error) {}, func(command.Facts) {})
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return v
}

func TestFilesHoldTheTokenTheAccountAndTheTrustedProjects(t *testing.T) {
	a := account.Default()
	files, err := Files(a, []string{"/p/one", "/p/two"}, fixed)
	if err != nil || len(files) != 2 {
		t.Fatalf("%v, %v", files, err)
	}
	var creds map[string]map[string]any
	if err := json.Unmarshal(files[0].Bytes, &creds); err != nil || files[0].Name != CredentialsFile {
		t.Fatalf("%s: %v", files[0].Name, err)
	}
	oa := creds["claudeAiOauth"]
	if oa["accessToken"] != oauth.AccessToken || oa["refreshToken"] != oauth.RefreshToken ||
		oa["expiresAt"] != float64(fixed.Add(oauth.TokenLifetime).UnixMilli()) ||
		oa["subscriptionType"] != string(a.SubscriptionType()) || oa["rateLimitTier"] != nil ||
		strings.Join(toStrings(oa["scopes"]), " ") != oauth.Scopes {
		t.Errorf("credentials %v", oa)
	}
	var top map[string]any
	if err := json.Unmarshal(files[1].Bytes, &top); err != nil || files[1].Name != ConfigFile {
		t.Fatalf("%s: %v", files[1].Name, err)
	}
	for _, key := range []string{"hasCompletedOnboarding", "officialMarketplaceAutoInstallAttempted", "officialMarketplaceAutoInstalled"} {
		if top[key] != true {
			t.Errorf("config %s = %v, want true", key, top[key])
		}
	}
	acct := top["oauthAccount"].(map[string]any)
	if acct["accountUuid"] != a.AccountUUID() || acct["emailAddress"] != a.EmailAddress() ||
		acct["organizationUuid"] != a.OrganizationUUID() || acct["organizationName"] != a.OrganizationName() {
		t.Errorf("oauthAccount %v", acct)
	}
	projects := top["projects"].(map[string]any)
	if len(projects) != 2 || projects["/p/one"].(map[string]any)["hasTrustDialogAccepted"] != true {
		t.Errorf("projects %v", projects)
	}
}

func toStrings(v any) []string {
	var out []string
	for _, s := range v.([]any) {
		out = append(out, s.(string))
	}
	return out
}

func TestALoginCreatesTheDirectoryAndItsFilesAt0600(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	res, err := login(t, dir)
	if err != nil || res.Answer != command.OK {
		t.Fatalf("%v, %v", res, err)
	}
	if res.Facts["config_dir_created"] != true || res.Facts["account_file"] != (*string)(nil) {
		t.Errorf("facts %v", res.Facts)
	}
	files := res.Facts["files"].(map[string]string)
	if files[CredentialsFile] != "created" || files[ConfigFile] != "created" || files[LegacyConfigFile] != "absent" {
		t.Errorf("files %v", files)
	}
	for _, name := range []string{CredentialsFile, ConfigFile} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v, %v", name, info, err)
		}
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(leftovers) > 0 {
		t.Errorf("temporaries left: %v", leftovers)
	}
}

func TestALoginReplacesAPriorLoginWholeAndRemovesTheLegacyConfig(t *testing.T) {
	dir := t.TempDir()
	one, two := t.TempDir(), t.TempDir()
	if _, err := login(t, dir, "--trust", one); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, LegacyConfigFile), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ConfigFile), []byte(`{"someone":"else"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := login(t, dir, "--trust", two)
	if err != nil {
		t.Fatal(err)
	}
	if res.Facts["config_dir_created"] != false {
		t.Errorf("facts %v", res.Facts)
	}
	files := res.Facts["files"].(map[string]string)
	if files[CredentialsFile] != "replaced" || files[ConfigFile] != "replaced" || files[LegacyConfigFile] != "removed" {
		t.Errorf("files %v", files)
	}
	if _, err := os.Stat(filepath.Join(dir, LegacyConfigFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("legacy config survives: %v", err)
	}
	config := readJSON(t, filepath.Join(dir, ConfigFile))
	if _, kept := config["someone"]; kept {
		t.Errorf("old content kept: %v", config)
	}
	twoKey, _ := TrustKey(two)
	oneKey, _ := TrustKey(one)
	projects := config["projects"].(map[string]any)
	if _, ok := projects[twoKey]; !ok || len(projects) != 1 {
		t.Errorf("projects %v, want only %s (not %s)", projects, twoKey, oneKey)
	}
	if info, _ := os.Stat(filepath.Join(dir, ConfigFile)); info.Mode().Perm() != 0o600 {
		t.Errorf("replaced file mode %v", info.Mode())
	}
}

func TestATrustedProjectIsKeyedByItsRealPathOnceHoweverItIsSpelled(t *testing.T) {
	project := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(project, link); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(project)
	if err != nil {
		t.Fatal(err)
	}
	res, err := login(t, t.TempDir(), "--trust", link, "--trust", project, "--trust", project+"/")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Facts["trusted"].([]string); len(got) != 1 || got[0] != real {
		t.Errorf("trusted %v, want [%s]", got, real)
	}
}

func TestAWrongCommandLineIsAUsageError(t *testing.T) {
	notDir := filepath.Join(t.TempDir(), "file")
	os.WriteFile(notDir, nil, 0o600)
	dangling := filepath.Join(t.TempDir(), "dangling")
	os.Symlink(filepath.Join(t.TempDir(), "gone"), dangling)
	for name, args := range map[string][]string{
		"no config dir":         {},
		"an empty config dir":   {""},
		"two config dirs":       {t.TempDir(), t.TempDir()},
		"a file as config dir":  {notDir},
		"a dangling symlink":    {dangling},
		"a file under a file":   {filepath.Join(notDir, "x")},
		"a missing trust dir":   {t.TempDir(), "--trust", filepath.Join(t.TempDir(), "nope")},
		"a trust file":          {t.TempDir(), "--trust", notDir},
		"an unknown flag":       {t.TempDir(), "--nope"},
		"account twice":         {t.TempDir(), "--account", "a", "--account", "b"},
		"an unreadable account": {t.TempDir(), "--account", filepath.Join(t.TempDir(), "nope.json")},
	} {
		_, err := login(t, args...)
		var usage *command.UsageError
		if !errors.As(err, &usage) {
			t.Errorf("%s: %v, want a UsageError", name, err)
		}
	}
}

func TestAnAccountFileReplacesTheDefault(t *testing.T) {
	a := account.Default()
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "account.json")
	os.WriteFile(path, []byte(strings.Replace(string(b), a.EmailAddress(), "someone@example.com", 1)), 0o600)
	dir := t.TempDir()
	res, err := login(t, dir, "--account", path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Facts["account_file"].(*string) == nil || *res.Facts["account_file"].(*string) != path {
		t.Errorf("facts %v", res.Facts)
	}
	got := readJSON(t, filepath.Join(dir, ConfigFile))["oauthAccount"].(map[string]any)
	if got["emailAddress"] != "someone@example.com" {
		t.Errorf("oauthAccount %v", got)
	}
}

func TestAFailedWriteReplacesNothing(t *testing.T) {
	dir := t.TempDir()
	if _, err := login(t, dir); err != nil {
		t.Fatal(err)
	}
	// A directory where a target file is: the rename cannot replace it.
	os.Remove(filepath.Join(dir, ConfigFile))
	os.Mkdir(filepath.Join(dir, ConfigFile), 0o755)
	os.WriteFile(filepath.Join(dir, ConfigFile, "x"), nil, 0o600)
	if _, err := login(t, dir); err == nil {
		t.Fatal("login over a directory succeeded")
	} else if !strings.Contains(err.Error(), ConfigFile) {
		t.Errorf("error does not name the file: %v", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(leftovers) > 0 {
		t.Errorf("temporaries left: %v", leftovers)
	}
}

func TestKeychainServiceIsNamedForTheConfigDir(t *testing.T) {
	// The name is Claude Code's, so the digest is pinned: sha256("/x") begins b3d1db31.
	if got := KeychainService("/x"); got != "Claude Code-credentials-b3d1db31" {
		t.Errorf("%q", got)
	}
}

func TestALoginRemovesTheKeychainEntryOfItsDirectory(t *testing.T) {
	dir := t.TempDir()
	if runtime.GOOS != "darwin" {
		res, err := login(t, dir)
		if err != nil || res.Facts["keychain_removed"] != (*int)(nil) {
			t.Fatalf("%v, %v", res.Facts, err)
		}
		return
	}
	// The directory is the test's own, so its service name is no one else's entry.
	configDir, _ := filepath.EvalSymlinks(dir)
	service := KeychainService(configDir)
	if out, err := exec.Command("security", "add-generic-password", "-s", service, "-a", "mock-anthropic-test", "-w", "x").CombinedOutput(); err != nil {
		t.Skipf("no keychain to add to: %v: %s", err, out)
	}
	t.Cleanup(func() { exec.Command("security", "delete-generic-password", "-s", service).Run() })
	res, err := login(t, configDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Facts["keychain_removed"].(*int); got == nil || *got != 1 {
		t.Errorf("keychain_removed %v, want 1", got)
	}
	if err := exec.Command("security", "find-generic-password", "-s", service).Run(); err == nil {
		t.Error("the entry survives the login")
	}
}

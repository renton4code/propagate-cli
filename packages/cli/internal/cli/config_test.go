package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"propagate/cli/internal/apiclient"
	"propagate/cli/internal/config"
	"propagate/cli/internal/identity"
)

func TestResolveAPIURLUsesBakedDefault(t *testing.T) {
	repo := initGitRepo(t)
	unsetEnvForTest(t, "PROPAGATE_API_URL")
	withBakedDefaultAPIURL(t, "https://api.propagatecli.com/")

	got := resolveAPIURL("", repo)
	if got != "https://api.propagatecli.com/" {
		t.Fatalf("resolveAPIURL() = %q, want https://api.propagatecli.com/", got)
	}
}

func TestResolveAPIURLReadsProfileBeforeBakedDefault(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	unsetEnvForTest(t, "PROPAGATE_API_URL")
	withBakedDefaultAPIURL(t, "https://api.propagatecli.com/")

	profileDir := filepath.Join(home, ".propagate")
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	profile := []byte("{\"format_version\":1,\"handle\":\"alice@example.com\",\"default_api_url\":\"http://profile.test\"}\n")
	if err := os.WriteFile(filepath.Join(profileDir, identity.ProfileFile), profile, 0o600); err != nil {
		t.Fatal(err)
	}

	if got := resolveAPIURL("", repo); got != "http://profile.test" {
		t.Fatalf("profile resolveAPIURL() = %q", got)
	}
}

func TestResolveAPIURLPrecedence(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	withBakedDefaultAPIURL(t, "https://api.propagatecli.com/")

	profileDir := filepath.Join(home, ".propagate")
	if err := os.MkdirAll(profileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	profile := []byte("{\"format_version\":1,\"handle\":\"alice@example.com\",\"default_api_url\":\"http://profile.test\"}\n")
	if err := os.WriteFile(filepath.Join(profileDir, identity.ProfileFile), profile, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PROPAGATE_API_URL", "http://env.test")
	if got := resolveAPIURL("http://flag.test", repo); got != "http://flag.test" {
		t.Fatalf("flag resolveAPIURL() = %q", got)
	}
	if got := resolveAPIURL("", repo); got != "http://env.test" {
		t.Fatalf("env resolveAPIURL() = %q", got)
	}
	unsetEnvForTest(t, "PROPAGATE_API_URL")
	if got := resolveAPIURL("", repo); got != "http://profile.test" {
		t.Fatalf("profile resolveAPIURL() = %q", got)
	}
}

func TestLoadLocalDotenvCanProvideEnvOverride(t *testing.T) {
	repo := initGitRepo(t)
	backendDir := filepath.Join(repo, "packages", "backend")
	if err := os.MkdirAll(backendDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backendDir, ".env"), []byte("PROPAGATE_API_URL=http://localhost:8080\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unsetEnvForTest(t, "PROPAGATE_API_URL")
	withBakedDefaultAPIURL(t, "https://api.propagatecli.com/")

	loadLocalDotenv(repo)

	if got := resolveAPIURL("", repo); got != "http://localhost:8080" {
		t.Fatalf("resolveAPIURL() = %q, want http://localhost:8080", got)
	}
}

func TestLoadLocalDotenvLoadsPropagateVarsOnly(t *testing.T) {
	repo := initGitRepo(t)
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("PROPAGATE_API_URL=http://dotenv.test\nDATABASE_URL=postgres://app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	unsetEnvForTest(t, "PROPAGATE_API_URL")
	unsetEnvForTest(t, "DATABASE_URL")

	loadLocalDotenv(repo)

	if got := os.Getenv("PROPAGATE_API_URL"); got != "http://dotenv.test" {
		t.Fatalf("PROPAGATE_API_URL = %q", got)
	}
	if got := os.Getenv("DATABASE_URL"); got != "" {
		t.Fatalf("DATABASE_URL should not be loaded by CLI dotenv, got %q", got)
	}
}

func unsetEnvForTest(t *testing.T, name string) {
	t.Helper()
	old, hadOld := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadOld {
			_ = os.Setenv(name, old)
			return
		}
		_ = os.Unsetenv(name)
	})
}

func withBakedDefaultAPIURL(t *testing.T, value string) {
	t.Helper()
	old := BakedDefaultAPIURL
	oldProfileDefault := identity.DefaultAPIURL
	BakedDefaultAPIURL = value
	identity.DefaultAPIURL = value
	t.Cleanup(func() {
		BakedDefaultAPIURL = old
		identity.DefaultAPIURL = oldProfileDefault
	})
}

func TestConfigPullUpdatesLocalConfigFromCloud(t *testing.T) {
	repo := initGitRepo(t)
	adminHome := t.TempDir()
	t.Setenv("HOME", adminHome)

	var initStdout, initStderr bytes.Buffer
	code := Run([]string{
		"init",
		"--handle", "alice@example.com",
		"--team-name", "Acme API",
		"--yes",
		"--non-interactive",
		"--skip-agent-guidance",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &initStdout,
		Err:     &initStderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("init exit = %d, stderr:\n%s", code, initStderr.String())
	}

	localProject, err := config.ReadProject(filepath.Join(repo, "propagate.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	bobIdent := generateTestIdentity(t)
	bobMember := config.Member{
		Handle:              "bob@example.com",
		PublicKeySHA:        bobIdent.PublicKeySHA,
		SigningPublicKey:    bobIdent.SigningPublicKey,
		EncryptionPublicKey: bobIdent.EncryptionPublicKey,
	}

	cloudProject := localProject
	cloudProject.CloudRevision = "rev_00002"
	cloudProject.SyncStatus = "synced"
	cloudProject.Members = append(cloudProject.Members, bobMember)
	cloudSnapshot, err := config.SnapshotJSON(cloudProject)
	if err != nil {
		t.Fatal(err)
	}
	cloudHash, err := config.ConfigHash(cloudProject)
	if err != nil {
		t.Fatal(err)
	}

	var handlerErr error
	previousClient := configPushHTTPClient
	t.Cleanup(func() { configPushHTTPClient = previousClient })
	configPushHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get(apiclient.HeaderPublicKeySHA) == "" || r.Header.Get(apiclient.HeaderSignature) == "" {
			handlerErr = fmt.Errorf("request missing signing headers")
			return nil, handlerErr
		}
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/config") {
			handlerErr = fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			return nil, handlerErr
		}
		return testResponse(t, http.StatusOK, map[string]any{
			"config_revision": "rev_00002",
			"config_hash":     cloudHash,
			"config_snapshot": json.RawMessage(cloudSnapshot),
			"server_time":     time.Now().UTC().Format(time.RFC3339),
		}), nil
	}), Timeout: 2 * time.Second}

	t.Setenv("HOME", adminHome)
	var blockedStdout, blockedStderr bytes.Buffer
	code = Run([]string{
		"config", "pull",
		"--api-url", "http://propagate.test",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &blockedStdout,
		Err:     &blockedStderr,
		WorkDir: repo,
	})
	if code != ExitConfirmationRequired {
		t.Fatalf("config pull without --yes exit = %d, want %d; stderr:\n%s", code, ExitConfirmationRequired, blockedStderr.String())
	}

	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"config", "pull",
		"--api-url", "http://propagate.test",
		"--yes",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("config pull exit = %d, stderr:\n%s", code, stderr.String())
	}
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	pulled, err := config.ReadProject(filepath.Join(repo, "propagate.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if pulled.CloudRevision != "rev_00002" {
		t.Fatalf("cloud revision = %s", pulled.CloudRevision)
	}
	if findMember(pulled.Members, bobMember.PublicKeySHA) == nil {
		t.Fatalf("cloud member was not pulled into propagate.yaml:\n%s", readConfig(t, repo))
	}
	if !strings.Contains(stdout.String(), "Would overwrite local changes: true") {
		t.Fatalf("output missing overwrite summary:\n%s", stdout.String())
	}
}

func TestConfigStatusReportsLocalAhead(t *testing.T) {
	repo := initGitRepo(t)
	adminHome := t.TempDir()
	t.Setenv("HOME", adminHome)

	var initStdout, initStderr bytes.Buffer
	code := Run([]string{
		"init",
		"--handle", "alice@example.com",
		"--team-name", "Acme API",
		"--yes",
		"--non-interactive",
		"--skip-agent-guidance",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &initStdout,
		Err:     &initStderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("init exit = %d, stderr:\n%s", code, initStderr.String())
	}
	setConfigRevision(t, repo, "rev_00001")

	localProject, err := config.ReadProject(filepath.Join(repo, "propagate.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	bobIdent := generateTestIdentity(t)
	localProject.Members = append(localProject.Members, config.Member{
		Handle:              "bob@example.com",
		PublicKeySHA:        bobIdent.PublicKeySHA,
		SigningPublicKey:    bobIdent.SigningPublicKey,
		EncryptionPublicKey: bobIdent.EncryptionPublicKey,
	})
	rendered, err := config.RenderParsed(localProject)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "propagate.yaml"), []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}

	var handlerErr error
	previousClient := configPushHTTPClient
	t.Cleanup(func() { configPushHTTPClient = previousClient })
	configPushHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get(apiclient.HeaderPublicKeySHA) == "" || r.Header.Get(apiclient.HeaderSignature) == "" {
			handlerErr = fmt.Errorf("request missing signing headers")
			return nil, handlerErr
		}
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/config/status") {
			handlerErr = fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			return nil, handlerErr
		}
		if got := r.URL.Query().Get("local_revision"); got != "rev_00001" {
			handlerErr = fmt.Errorf("local_revision = %q", got)
			return nil, handlerErr
		}
		if got := r.URL.Query().Get("local_config_hash"); got == "" {
			handlerErr = fmt.Errorf("local_config_hash was empty")
			return nil, handlerErr
		}
		return testResponse(t, http.StatusOK, map[string]any{
			"local_revision":     "rev_00001",
			"cloud_revision":     "rev_00001",
			"local_config_hash":  r.URL.Query().Get("local_config_hash"),
			"cloud_config_hash":  "sha256:cloud",
			"state":              "local_ahead",
			"recommended_action": "push",
			"safe_summary":       map[string]any{"members_count": float64(1), "scopes_count": float64(1)},
		}), nil
	}), Timeout: 2 * time.Second}

	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"config", "status",
		"--api-url", "http://propagate.test",
		"--json",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("config status exit = %d, stderr:\n%s", code, stderr.String())
	}
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	var result ConfigStatusResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("parse status JSON: %v\n%s", err, stdout.String())
	}
	if result.State != "local_ahead" || result.RecommendedAction != "push" {
		t.Fatalf("unexpected status result: %+v", result)
	}
	if len(result.LocalOnlyChanges) == 0 {
		t.Fatalf("expected local-only changes, got none")
	}
	if result.CloudConfigHash != "sha256:cloud" {
		t.Fatalf("cloud hash = %s", result.CloudConfigHash)
	}
}

func TestConfigStatusShowsLocalFactsWhenCloudURLMissing(t *testing.T) {
	repo := initGitRepo(t)
	adminHome := t.TempDir()
	t.Setenv("HOME", adminHome)

	var initStdout, initStderr bytes.Buffer
	code := Run([]string{
		"init",
		"--handle", "alice@example.com",
		"--team-name", "Acme API",
		"--yes",
		"--non-interactive",
		"--skip-agent-guidance",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &initStdout,
		Err:     &initStderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("init exit = %d, stderr:\n%s", code, initStderr.String())
	}
	setConfigRevision(t, repo, "rev_00001")

	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"config", "status",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitCloudUnavailable {
		t.Fatalf("config status exit = %d, want %d; stderr:\n%s", code, ExitCloudUnavailable, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Local revision: rev_00001") {
		t.Fatalf("stdout missing local revision:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Local config hash: sha256:") {
		t.Fatalf("stdout missing local hash:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Propagate API URL is required for config status") {
		t.Fatalf("stderr missing cloud URL error:\n%s", stderr.String())
	}
}

func TestConfigEditUpdatesVariableDeclarations(t *testing.T) {
	repo := initGitRepo(t)
	adminHome := t.TempDir()
	t.Setenv("HOME", adminHome)

	var initStdout, initStderr bytes.Buffer
	code := Run([]string{
		"init",
		"--handle", "alice@example.com",
		"--team-name", "Acme API",
		"--yes",
		"--non-interactive",
		"--skip-agent-guidance",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &initStdout,
		Err:     &initStderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("init exit = %d, stderr:\n%s", code, initStderr.String())
	}
	writeConfigEditFixture(t, repo)

	input := strings.Join([]string{
		"3", "t", "b",
		"1", "m", "2",
		"1", "r", "y",
		"s",
	}, "\n") + "\n"
	var stdout, stderr bytes.Buffer
	code = Run([]string{"config", "edit"}, Streams{
		In:      strings.NewReader(input),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("config edit exit = %d, stderr:\n%s\nstdout:\n%s", code, stderr.String(), stdout.String())
	}

	edited, err := config.ReadProject(filepath.Join(repo, "propagate.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	publicURL, ok := findConfigVariable(edited, "dev", ".env", "PUBLIC_URL")
	if !ok {
		t.Fatalf("PUBLIC_URL was not found in dev:\n%s", readConfig(t, repo))
	}
	if publicURL.Sensitivity != config.SensitivityNonSensitive {
		t.Fatalf("PUBLIC_URL sensitivity = %s", publicURL.Sensitivity)
	}
	if publicURL.Literal != "https://example.test" || publicURL.Digest != "" || publicURL.Preview != "" {
		t.Fatalf("PUBLIC_URL was not stored as a non-sensitive literal: %+v", publicURL)
	}
	if _, ok := findConfigVariable(edited, "dev", ".env", "FEATURE_FLAG"); ok {
		t.Fatalf("FEATURE_FLAG should have moved out of dev:\n%s", readConfig(t, repo))
	}
	if _, ok := findConfigVariable(edited, "prod", ".env", "FEATURE_FLAG"); !ok {
		t.Fatalf("FEATURE_FLAG was not moved to prod:\n%s", readConfig(t, repo))
	}
	if _, ok := findConfigVariable(edited, "dev", ".env", "OLD_KEY"); ok {
		t.Fatalf("OLD_KEY should have been removed:\n%s", readConfig(t, repo))
	}
	if !scopeHasEnvFile(edited, "prod", ".env") {
		t.Fatalf("prod scope did not receive the moved env file mapping:\n%s", readConfig(t, repo))
	}
	if !strings.Contains(stdout.String(), "Sensitivity changes: 1") || !strings.Contains(stdout.String(), "Scope changes: 1") || !strings.Contains(stdout.String(), "Removed variables: 1") {
		t.Fatalf("output missing edit summary:\n%s", stdout.String())
	}
}

func TestConfigEditDryRunDoesNotWrite(t *testing.T) {
	repo := initGitRepo(t)
	adminHome := t.TempDir()
	t.Setenv("HOME", adminHome)

	var initStdout, initStderr bytes.Buffer
	code := Run([]string{
		"init",
		"--handle", "alice@example.com",
		"--team-name", "Acme API",
		"--yes",
		"--non-interactive",
		"--skip-agent-guidance",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &initStdout,
		Err:     &initStderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("init exit = %d, stderr:\n%s", code, initStderr.String())
	}
	writeConfigEditFixture(t, repo)
	before := readConfig(t, repo)

	input := strings.Join([]string{"1", "t", "b", "s"}, "\n") + "\n"
	var stdout, stderr bytes.Buffer
	code = Run([]string{"config", "edit", "--dry-run"}, Streams{
		In:      strings.NewReader(input),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("config edit dry-run exit = %d, stderr:\n%s\nstdout:\n%s", code, stderr.String(), stdout.String())
	}
	if after := readConfig(t, repo); after != before {
		t.Fatalf("dry run wrote propagate.yaml\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if !strings.Contains(stdout.String(), "Config edit dry run complete.") {
		t.Fatalf("output missing dry-run summary:\n%s", stdout.String())
	}
}

func setConfigRevision(t *testing.T, repo string, revision string) {
	t.Helper()
	path := filepath.Join(repo, "propagate.yaml")
	project, err := config.ReadProject(path)
	if err != nil {
		t.Fatal(err)
	}
	project.CloudRevision = revision
	project.SyncStatus = "synced"
	rendered, err := config.RenderParsed(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeConfigEditFixture(t *testing.T, repo string) {
	t.Helper()
	path := filepath.Join(repo, "propagate.yaml")
	project, err := config.ReadProject(path)
	if err != nil {
		t.Fatal(err)
	}
	project.Scopes[0].EnvFiles = []string{".env"}
	project.Scopes[0].Variables = []config.VariableDeclaration{
		{
			Name:        "PUBLIC_URL",
			EnvFilePath: ".env",
			Sensitivity: config.SensitivitySensitive,
			Digest:      "hmac-sha-256:v1:aaaaaaaa",
		},
		{
			Name:        "FEATURE_FLAG",
			EnvFilePath: ".env",
			Sensitivity: config.SensitivitySensitive,
			Digest:      "hmac-sha-256:v1:bbbbbbbb",
		},
		{
			Name:        "OLD_KEY",
			EnvFilePath: ".env",
			Sensitivity: config.SensitivitySensitive,
			Digest:      "hmac-sha-256:v1:cccccccc",
		},
	}
	project.Scopes = append(project.Scopes, config.ScopeSummary{
		Name:     "prod",
		EnvFiles: []string{".env.prod"},
	})
	rendered, err := config.RenderParsed(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.WriteRaw(path, rendered); err != nil {
		t.Fatal(err)
	}
	envPayload := strings.Join([]string{
		"PUBLIC_URL=https://example.test",
		"FEATURE_FLAG=true",
		"OLD_KEY=unused",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte(envPayload), 0o600); err != nil {
		t.Fatal(err)
	}
}

func findConfigVariable(project config.ParsedProject, scopeName string, envFilePath string, name string) (config.VariableDeclaration, bool) {
	for _, scope := range project.Scopes {
		if scope.Name != scopeName {
			continue
		}
		for _, variable := range scope.Variables {
			if variable.Name == name && variable.EnvFilePath == envFilePath {
				return variable, true
			}
		}
	}
	return config.VariableDeclaration{}, false
}

func scopeHasEnvFile(project config.ParsedProject, scopeName string, envFilePath string) bool {
	for _, scope := range project.Scopes {
		if scope.Name != scopeName {
			continue
		}
		for _, path := range scope.EnvFiles {
			if path == envFilePath {
				return true
			}
		}
	}
	return false
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func testResponse(t *testing.T, status int, data any) *http.Response {
	t.Helper()
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(map[string]any{
		"ok":      status >= 200 && status < 300,
		"request": "req_test",
		"data":    data,
	}); err != nil {
		t.Fatal(err)
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body.Bytes())),
	}
}

func generateTestIdentity(t *testing.T) identity.Identity {
	t.Helper()
	ident, err := identity.New("test@example.com", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return ident
}

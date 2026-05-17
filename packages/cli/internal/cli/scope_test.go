package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"propagate/cli/internal/apiclient"
	"propagate/cli/internal/config"
	"propagate/cli/internal/identity"
)

func TestScopeCreateAddsEmptyScope(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

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

	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"scope", "create", "staging",
		"--env-file", ".env.staging",
		"--non-interactive",
		"--no-color",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("scope create exit = %d, stderr:\n%s", code, stderr.String())
	}

	project, err := config.ReadProject(filepath.Join(repo, "propagate.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	scope := findScopeSummary(project.Scopes, "staging")
	if scope == nil {
		t.Fatalf("staging scope was not created:\n%s", readConfig(t, repo))
	}
	if len(scope.Variables) != 0 {
		t.Fatalf("staging variables = %d, want empty", len(scope.Variables))
	}
	if len(scope.EnvFiles) != 1 || scope.EnvFiles[0] != ".env.staging" {
		t.Fatalf("staging env files = %+v", scope.EnvFiles)
	}
	if !strings.Contains(readConfig(t, repo), "staging: write") {
		t.Fatalf("management member was not granted staging write:\n%s", readConfig(t, repo))
	}
	for _, want := range []string{
		"Scope created.",
		"propagate config push",
		"propagate env push --scope staging --dry-run",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestScopeCreateDoesNotCopyFromExistingScope(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

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
	setScopeCreateSourceTemplate(t, repo)

	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"scope", "create", "prod",
		"--no-color",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("scope create exit = %d, stderr:\n%s", code, stderr.String())
	}

	project, err := config.ReadProject(filepath.Join(repo, "propagate.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	scope := findScopeSummary(project.Scopes, "prod")
	if scope == nil {
		t.Fatalf("prod scope was not created:\n%s", readConfig(t, repo))
	}
	if len(scope.EnvFiles) != 0 {
		t.Fatalf("scope env files = %+v, want empty until config edit", scope.EnvFiles)
	}
	if len(scope.Variables) != 0 {
		t.Fatalf("scope variables = %+v, want empty until config edit", scope.Variables)
	}
	for _, want := range []string{
		"propagate config edit",
		"propagate env push --scope prod --dry-run",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, stdout.String())
		}
	}
	for _, unexpected := range []string{"Clone metadata", "Suggested clone source", "Cloned variable declarations", ".env.prod", "propagate env clone"} {
		if strings.Contains(stdout.String(), unexpected) {
			t.Fatalf("output contained unexpected %q:\n%s", unexpected, stdout.String())
		}
	}
}

func setScopeCreateSourceTemplate(t *testing.T, repo string) {
	t.Helper()
	project, err := config.ReadProject(filepath.Join(repo, "propagate.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	dev := findScopeSummary(project.Scopes, "dev")
	if dev == nil {
		t.Fatalf("dev scope missing:\n%s", readConfig(t, repo))
	}
	dev.EnvFiles = []string{".env"}
	dev.Variables = []config.VariableDeclaration{
		{Name: "API_TOKEN", EnvFilePath: ".env", Sensitivity: config.SensitivitySensitive, Digest: "hmac-sha-256:v1:source-digest"},
		{Name: "DATABASE_URL", EnvFilePath: ".env", Sensitivity: config.SensitivitySensitive, Digest: "hmac-sha-256:v1:source-digest-2"},
	}
	rendered, err := config.RenderParsed(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := config.WriteRaw(filepath.Join(repo, "propagate.yaml"), rendered); err != nil {
		t.Fatal(err)
	}
}

func TestScopeCreateDryRunDoesNotWrite(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

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
	before := readConfig(t, repo)

	var stdout, stderr bytes.Buffer
	code = Run([]string{"scope", "create", "staging", "--dry-run", "--non-interactive", "--no-color"}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("scope create dry-run exit = %d, stderr:\n%s", code, stderr.String())
	}
	if after := readConfig(t, repo); after != before {
		t.Fatalf("dry run wrote propagate.yaml\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if !strings.Contains(stdout.String(), "Scope create dry run complete.") {
		t.Fatalf("output missing dry-run summary:\n%s", stdout.String())
	}
}

func TestScopeCreateRejectsDuplicate(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

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
	before := readConfig(t, repo)

	var stdout, stderr bytes.Buffer
	code = Run([]string{"scope", "create", "dev", "--non-interactive"}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitConflict {
		t.Fatalf("scope create duplicate exit = %d, want %d; stderr:\n%s", code, ExitConflict, stderr.String())
	}
	if after := readConfig(t, repo); after != before {
		t.Fatalf("duplicate scope create wrote propagate.yaml\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestConfigPushUploadsEnvelopeForCreatedScope(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

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
	cloudProject, err := config.ReadProject(filepath.Join(repo, "propagate.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cloudSnapshot, err := config.SnapshotJSON(cloudProject)
	if err != nil {
		t.Fatal(err)
	}
	adminIdent, err := identity.Load()
	if err != nil {
		t.Fatal(err)
	}

	var scopeStdout, scopeStderr bytes.Buffer
	code = Run([]string{
		"scope", "create", "staging",
		"--env-file", ".env.staging",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &scopeStdout,
		Err:     &scopeStderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("scope create exit = %d, stderr:\n%s", code, scopeStderr.String())
	}

	var sawPush bool
	var handlerErr error
	previousClient := configPushHTTPClient
	t.Cleanup(func() { configPushHTTPClient = previousClient })
	configPushHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get(apiclient.HeaderPublicKeySHA) == "" || r.Header.Get(apiclient.HeaderSignature) == "" {
			handlerErr = fmt.Errorf("request missing signing headers")
			return nil, handlerErr
		}
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config/status"):
			return testResponse(t, http.StatusOK, map[string]any{
				"local_revision":     "rev_00001",
				"cloud_revision":     "rev_00001",
				"local_config_hash":  r.URL.Query().Get("local_config_hash"),
				"cloud_config_hash":  "sha256:cloud",
				"state":              "local_ahead",
				"recommended_action": "push",
				"safe_summary": map[string]any{
					"members_count": float64(1),
					"scopes_count":  float64(1),
				},
			}), nil
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config"):
			return testResponse(t, http.StatusOK, map[string]any{
				"config_revision": "rev_00001",
				"config_hash":     "sha256:cloud",
				"config_snapshot": json.RawMessage(cloudSnapshot),
				"server_time":     time.Now().UTC().Format(time.RFC3339),
			}), nil
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/config/push"):
			sawPush = true
			body, err := io.ReadAll(r.Body)
			if err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			var request struct {
				ScopeKeyEnvelopes    []apiclient.ScopeKeyEnvelope `json:"scope_key_envelopes"`
				TargetConfigSnapshot json.RawMessage              `json:"target_config_snapshot"`
			}
			if err := json.Unmarshal(body, &request); err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			if len(request.ScopeKeyEnvelopes) != 1 {
				handlerErr = fmt.Errorf("scope key envelopes = %d, want 1", len(request.ScopeKeyEnvelopes))
				return nil, handlerErr
			}
			envelope := request.ScopeKeyEnvelopes[0]
			if envelope.Scope != "staging" || envelope.RecipientKeySHA != adminIdent.PublicKeySHA || envelope.EncryptedScopeKey == "" {
				handlerErr = fmt.Errorf("unexpected new scope envelope: %+v", envelope)
				return nil, handlerErr
			}
			var snapshot struct {
				Scopes map[string]struct {
					EnvFiles []string `json:"env_files"`
				} `json:"scopes"`
			}
			if err := json.Unmarshal(request.TargetConfigSnapshot, &snapshot); err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			if _, ok := snapshot.Scopes["staging"]; !ok {
				handlerErr = fmt.Errorf("target snapshot missing staging scope: %s", request.TargetConfigSnapshot)
				return nil, handlerErr
			}
			return testResponse(t, http.StatusOK, map[string]any{
				"old_revision":       "rev_00001",
				"new_revision":       "rev_00002",
				"config_hash":        "sha256:accepted",
				"envelopes_count":    float64(1),
				"audit_events_count": float64(1),
			}), nil
		default:
			handlerErr = fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			return nil, handlerErr
		}
	}), Timeout: 2 * time.Second}

	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"config", "push",
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
		t.Fatalf("config push exit = %d, stderr:\n%s", code, stderr.String())
	}
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	if !sawPush {
		t.Fatalf("fake API did not receive config push")
	}
	if !strings.Contains(stdout.String(), "Encrypted access envelopes uploaded: 1") {
		t.Fatalf("output missing envelope count:\n%s", stdout.String())
	}
}

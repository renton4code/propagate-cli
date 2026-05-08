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
	"propagate/cli/internal/secretcrypto"
)

func TestEnvPushDiffsEncryptsAndUploadsChanges(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	localSecret := "new-local-token-value"
	addedSecret := "brand-new-secret-value"
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("API_TOKEN="+localSecret+"\nNEW_SECRET="+addedSecret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

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

	ident, err := identity.Load()
	if err != nil {
		t.Fatal(err)
	}
	project, err := config.ReadProject(filepath.Join(repo, "propagate.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	bundle := encryptedPullBundle(t, ident, project.TeamID, map[string]string{
		"API_TOKEN":   "old-cloud-token-value",
		"REMOVED_VAR": "removed-cloud-value",
	})

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
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/scopes/dev/pull-bundle"):
			return testResponse(t, http.StatusOK, bundle), nil
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/scopes/dev/env/push"):
			sawPush = true
			body, err := io.ReadAll(r.Body)
			if err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			for _, forbidden := range []string{localSecret, addedSecret, "old-cloud-token-value", "removed-cloud-value"} {
				if bytes.Contains(body, []byte(forbidden)) {
					handlerErr = fmt.Errorf("env push leaked plaintext %q in request: %s", forbidden, body)
					return nil, handlerErr
				}
			}
			var request apiclient.EnvPushRequest
			if err := json.Unmarshal(body, &request); err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			if request.OperationID == "" {
				handlerErr = fmt.Errorf("operation_id was empty")
				return nil, handlerErr
			}
			if request.ExpectedConfigRevision != "rev_00001" {
				handlerErr = fmt.Errorf("expected revision = %s", request.ExpectedConfigRevision)
				return nil, handlerErr
			}
			if request.SafeCounts.Added != 1 || request.SafeCounts.Changed != 1 || request.SafeCounts.Removed != 1 {
				handlerErr = fmt.Errorf("unexpected safe counts: %+v", request.SafeCounts)
				return nil, handlerErr
			}
			if len(request.TargetConfigSnapshot) == 0 || !bytes.Contains(request.TargetConfigSnapshot, []byte(`hmac-sha-256:v1:`)) || bytes.Contains(request.TargetConfigSnapshot, []byte(`REMOVED_VAR`)) {
				handlerErr = fmt.Errorf("target config snapshot missing updated variable declarations: %s", request.TargetConfigSnapshot)
				return nil, handlerErr
			}
			if len(request.Upserts) != 2 || len(request.Removals) != 1 {
				handlerErr = fmt.Errorf("unexpected payload counts: %d upserts, %d removals", len(request.Upserts), len(request.Removals))
				return nil, handlerErr
			}
			upsertsByName := map[string]apiclient.EnvPushUpsert{}
			for _, upsert := range request.Upserts {
				upsertsByName[upsert.Name] = upsert
				if upsert.Algorithm != secretcrypto.ValueAlgorithm || upsert.Ciphertext == "" || upsert.Nonce == "" {
					handlerErr = fmt.Errorf("invalid encrypted upsert: %+v", upsert)
					return nil, handlerErr
				}
			}
			if upsertsByName["API_TOKEN"].ExpectedVersionID == "" {
				handlerErr = fmt.Errorf("changed variable missing expected version: %+v", upsertsByName["API_TOKEN"])
				return nil, handlerErr
			}
			if upsertsByName["NEW_SECRET"].ExpectedVersionID != "" {
				handlerErr = fmt.Errorf("new variable had expected version: %+v", upsertsByName["NEW_SECRET"])
				return nil, handlerErr
			}
			if request.Removals[0].Name != "REMOVED_VAR" || request.Removals[0].ExpectedVersionID == "" {
				handlerErr = fmt.Errorf("invalid removal: %+v", request.Removals[0])
				return nil, handlerErr
			}
			return testResponse(t, http.StatusOK, map[string]any{
				"created_versions": []any{
					map[string]any{"env_file_path": ".env", "name": "API_TOKEN", "version_id": "ver_00003"},
					map[string]any{"env_file_path": ".env", "name": "NEW_SECRET", "version_id": "ver_00001"},
				},
				"removed_variables": []any{
					map[string]any{"env_file_path": ".env", "name": "REMOVED_VAR"},
				},
				"config_revision":    "rev_00002",
				"config_hash":        "sha256:envpush",
				"audit_events_count": float64(1),
			}), nil
		default:
			handlerErr = fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			return nil, handlerErr
		}
	}), Timeout: 2 * time.Second}

	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"env", "push",
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
		t.Fatalf("env push exit = %d, stderr:\n%s", code, stderr.String())
	}
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	if !sawPush {
		t.Fatalf("fake API did not receive env push")
	}
	for _, forbidden := range []string{localSecret, addedSecret, "old-cloud-token-value", "removed-cloud-value"} {
		if strings.Contains(stdout.String(), forbidden) || strings.Contains(stderr.String(), forbidden) {
			t.Fatalf("env push output leaked plaintext %q\nstdout:\n%s\nstderr:\n%s", forbidden, stdout.String(), stderr.String())
		}
	}
	for _, want := range []string{"Variables added: 1", "Variables changed: 1", "Variables removed: 1", "Encrypted versions uploaded: 2"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestEnvPushDryRunDoesNotUpload(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("API_TOKEN=new-local-token-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}

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

	ident, err := identity.Load()
	if err != nil {
		t.Fatal(err)
	}
	project, err := config.ReadProject(filepath.Join(repo, "propagate.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	bundle := encryptedPullBundle(t, ident, project.TeamID, map[string]string{"API_TOKEN": "old-cloud-token-value"})

	var sawPush bool
	var handlerErr error
	previousClient := configPushHTTPClient
	t.Cleanup(func() { configPushHTTPClient = previousClient })
	configPushHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/scopes/dev/pull-bundle") {
			return testResponse(t, http.StatusOK, bundle), nil
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/scopes/dev/env/push") {
			sawPush = true
			return testResponse(t, http.StatusOK, map[string]any{"audit_events_count": float64(1)}), nil
		}
		handlerErr = fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		return nil, handlerErr
	}), Timeout: 2 * time.Second}

	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"env", "push",
		"--api-url", "http://propagate.test",
		"--dry-run",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("env push dry run exit = %d, stderr:\n%s", code, stderr.String())
	}
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	if sawPush {
		t.Fatalf("dry run uploaded env changes")
	}
	if !strings.Contains(stdout.String(), "Env push dry run complete.") || !strings.Contains(stdout.String(), "Variables changed: 1") {
		t.Fatalf("dry-run output missing plan:\n%s", stdout.String())
	}
}

func TestEnvPushNonInteractiveRequiresYes(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("API_TOKEN=new-local-token-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}

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

	ident, err := identity.Load()
	if err != nil {
		t.Fatal(err)
	}
	project, err := config.ReadProject(filepath.Join(repo, "propagate.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	bundle := encryptedPullBundle(t, ident, project.TeamID, map[string]string{"API_TOKEN": "old-cloud-token-value"})

	var sawPush bool
	previousClient := configPushHTTPClient
	t.Cleanup(func() { configPushHTTPClient = previousClient })
	configPushHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/scopes/dev/pull-bundle") {
			return testResponse(t, http.StatusOK, bundle), nil
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/scopes/dev/env/push") {
			sawPush = true
			return testResponse(t, http.StatusOK, map[string]any{"audit_events_count": float64(1)}), nil
		}
		return nil, fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}), Timeout: 2 * time.Second}

	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"env", "push",
		"--api-url", "http://propagate.test",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitConfirmationRequired {
		t.Fatalf("env push exit = %d, want %d; stderr:\n%s", code, ExitConfirmationRequired, stderr.String())
	}
	if sawPush {
		t.Fatalf("blocked env push uploaded changes")
	}
	if !strings.Contains(stderr.String(), "requires --yes") {
		t.Fatalf("stderr missing --yes guidance:\n%s", stderr.String())
	}
}

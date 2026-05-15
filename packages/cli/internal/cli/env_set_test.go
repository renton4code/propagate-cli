package cli

import (
	"bufio"
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

func TestEnvSetValueStdinEncryptsAndUploadsSingleValue(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("API_TOKEN=local-placeholder\n"), 0o600); err != nil {
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
	newSecret := "new-value-from-prompt"
	bundle := encryptedPullBundle(t, ident, project.TeamID, map[string]string{"API_TOKEN": "old-cloud-value"})

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
			for _, forbidden := range []string{newSecret, "old-cloud-value"} {
				if bytes.Contains(body, []byte(forbidden)) {
					handlerErr = fmt.Errorf("env set leaked plaintext %q in request: %s", forbidden, body)
					return nil, handlerErr
				}
			}
			var request apiclient.EnvPushRequest
			if err := json.Unmarshal(body, &request); err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			if request.OperationID == "" || !strings.Contains(request.OperationID, "env_set") {
				handlerErr = fmt.Errorf("operation_id was not an env_set id: %q", request.OperationID)
				return nil, handlerErr
			}
			if request.ExpectedConfigRevision != "rev_00001" {
				handlerErr = fmt.Errorf("expected revision = %s", request.ExpectedConfigRevision)
				return nil, handlerErr
			}
			if len(request.Upserts) != 1 || len(request.Removals) != 0 {
				handlerErr = fmt.Errorf("unexpected payload counts: %d upserts, %d removals", len(request.Upserts), len(request.Removals))
				return nil, handlerErr
			}
			upsert := request.Upserts[0]
			if upsert.Name != "API_TOKEN" || upsert.EnvFilePath != ".env" {
				handlerErr = fmt.Errorf("unexpected upsert target: %+v", upsert)
				return nil, handlerErr
			}
			if upsert.ExpectedVersionID == "" {
				handlerErr = fmt.Errorf("changed variable missing expected version: %+v", upsert)
				return nil, handlerErr
			}
			if upsert.Algorithm != secretcrypto.ValueAlgorithm || upsert.Ciphertext == "" || upsert.Nonce == "" {
				handlerErr = fmt.Errorf("invalid encrypted upsert: %+v", upsert)
				return nil, handlerErr
			}
			if request.SafeCounts.Changed != 1 || request.SafeCounts.Added != 0 {
				handlerErr = fmt.Errorf("unexpected safe counts: %+v", request.SafeCounts)
				return nil, handlerErr
			}
			return testResponse(t, http.StatusOK, map[string]any{
				"created_versions": []any{
					map[string]any{"env_file_path": ".env", "name": "API_TOKEN", "version_id": "ver_00002"},
				},
				"audit_events_count": float64(1),
			}), nil
		default:
			handlerErr = fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			return nil, handlerErr
		}
	}), Timeout: 2 * time.Second}

	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"env", "set", "API_TOKEN",
		"--api-url", "http://propagate.test",
		"--yes",
		"--value-stdin",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(newSecret + "\n"),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("env set exit = %d, stderr:\n%s", code, stderr.String())
	}
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	if !sawPush {
		t.Fatalf("fake API did not receive env set push")
	}
	for _, forbidden := range []string{newSecret, "old-cloud-value"} {
		if strings.Contains(stdout.String(), forbidden) || strings.Contains(stderr.String(), forbidden) {
			t.Fatalf("env set output leaked plaintext %q\nstdout:\n%s\nstderr:\n%s", forbidden, stdout.String(), stderr.String())
		}
	}
	if strings.Contains(stdout.String(), "Value for API_TOKEN") {
		t.Fatalf("value stdin mode should not render a value prompt:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Change: changed") || !strings.Contains(stdout.String(), "New versions uploaded: 1") {
		t.Fatalf("output missing env set summary:\n%s", stdout.String())
	}
}

func TestEnvSetRejectsPositionalPlaintextValue(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"env", "set", "API_TOKEN", "plaintext-value",
		"--api-url", "http://propagate.test",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitUsageError {
		t.Fatalf("env set exit = %d, want %d; stderr:\n%s", code, ExitUsageError, stderr.String())
	}
	if !strings.Contains(stderr.String(), "never accepts the value as an argument") {
		t.Fatalf("stderr missing positional value rejection:\n%s", stderr.String())
	}
	if strings.Contains(stdout.String(), "plaintext-value") {
		t.Fatalf("stdout echoed positional plaintext:\n%s", stdout.String())
	}
}

func TestEnvSetNonInteractiveRequiresPrompt(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"env", "set", "API_TOKEN",
		"--api-url", "http://propagate.test",
		"--yes",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitConfirmationRequired {
		t.Fatalf("env set exit = %d, want %d; stderr:\n%s", code, ExitConfirmationRequired, stderr.String())
	}
	if !strings.Contains(stderr.String(), "requires --value-stdin") {
		t.Fatalf("stderr missing stdin value guidance:\n%s", stderr.String())
	}
}

func TestEnvSetValueStdinRequiresInput(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"env", "set", "API_TOKEN",
		"--dry-run",
		"--value-stdin",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitValidationError {
		t.Fatalf("env set exit = %d, want %d; stderr:\n%s", code, ExitValidationError, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--value-stdin requires input") {
		t.Fatalf("stderr missing missing-input guidance:\n%s", stderr.String())
	}
}

func TestEnvSetPromptsForScopeWhenMultipleScopes(t *testing.T) {
	input := strings.NewReader("2\n")
	var out bytes.Buffer
	project := config.ParsedProject{Scopes: []config.ScopeSummary{
		{Name: "dev", EnvFiles: []string{".env"}},
		{Name: "staging", EnvFiles: []string{".env.staging"}},
	}}

	scopeName, scope, err := resolveEnvSetScope(bufio.NewReader(input), input, &out, envSetOptions{}, project)
	if err != nil {
		t.Fatalf("resolve scope: %v", err)
	}
	if scopeName != "staging" || scope == nil || scope.Name != "staging" {
		t.Fatalf("scope = %q %+v, want staging", scopeName, scope)
	}
	if !strings.Contains(out.String(), "Scopes:") || !strings.Contains(out.String(), "Choose scope number or name") {
		t.Fatalf("output missing scope prompt:\n%s", out.String())
	}
}

func TestEnvSetUsesOnlyScopeWhenScopeOmitted(t *testing.T) {
	project := config.ParsedProject{Scopes: []config.ScopeSummary{
		{Name: "staging", EnvFiles: []string{".env.staging"}},
	}}

	scopeName, scope, err := resolveEnvSetScope(bufio.NewReader(strings.NewReader("")), strings.NewReader(""), io.Discard, envSetOptions{}, project)
	if err != nil {
		t.Fatalf("resolve scope: %v", err)
	}
	if scopeName != "staging" || scope == nil || scope.Name != "staging" {
		t.Fatalf("scope = %q %+v, want staging", scopeName, scope)
	}
}

func TestEnvSetNonInteractiveRequiresScopeWhenMultipleScopes(t *testing.T) {
	project := config.ParsedProject{Scopes: []config.ScopeSummary{
		{Name: "dev", EnvFiles: []string{".env"}},
		{Name: "staging", EnvFiles: []string{".env.staging"}},
	}}

	_, _, err := resolveEnvSetScope(
		bufio.NewReader(strings.NewReader("")),
		strings.NewReader(""),
		io.Discard,
		envSetOptions{globalOptions: globalOptions{NonInteractive: true}},
		project,
	)
	if err == nil {
		t.Fatalf("resolve scope succeeded, want error")
	}
	cmdErr, ok := err.(*CommandError)
	if !ok {
		t.Fatalf("error = %T %v, want CommandError", err, err)
	}
	if cmdErr.Code != ExitConfirmationRequired || !strings.Contains(cmdErr.Error(), "--scope") {
		t.Fatalf("error = %+v, want --scope confirmation required", cmdErr)
	}
}

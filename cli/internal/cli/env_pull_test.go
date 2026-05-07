package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"propagate/cli/internal/apiclient"
	"propagate/cli/internal/config"
	"propagate/cli/internal/identity"
	"propagate/cli/internal/secretcrypto"
)

func TestEnvPullDecryptsAndMergesEnvFile(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("API_TOKEN=old-token\nLOCAL_ONLY=yes\n"), 0o600); err != nil {
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
		"API_TOKEN":    "cloud-token",
		"DATABASE_URL": "postgres://db",
	})

	var sawEvent bool
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
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/events/pull"):
			sawEvent = true
			body, err := io.ReadAll(r.Body)
			if err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			if bytes.Contains(body, []byte("cloud-token")) || bytes.Contains(body, []byte("postgres://db")) {
				handlerErr = fmt.Errorf("pull event leaked plaintext: %s", body)
				return nil, handlerErr
			}
			var request apiclient.PullEventRequest
			if err := json.Unmarshal(body, &request); err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			if request.Scope != "dev" || request.ConfigRevision != "rev_00001" || request.VariablesCount != 2 {
				handlerErr = fmt.Errorf("unexpected pull event: %+v", request)
				return nil, handlerErr
			}
			return testResponse(t, http.StatusOK, map[string]any{"event_id": "audit_00001", "recorded_count": float64(1)}), nil
		default:
			handlerErr = fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			return nil, handlerErr
		}
	}), Timeout: 2 * time.Second}

	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"env", "pull",
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
		t.Fatalf("env pull exit = %d, stderr:\n%s", code, stderr.String())
	}
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	if !sawEvent {
		t.Fatalf("fake API did not receive pull event")
	}
	envText := readEnv(t, repo)
	for _, want := range []string{"API_TOKEN=cloud-token", "DATABASE_URL=postgres://db", "LOCAL_ONLY=yes"} {
		if !strings.Contains(envText, want) {
			t.Fatalf(".env missing %q:\n%s", want, envText)
		}
	}
	if strings.Contains(stdout.String(), "cloud-token") || strings.Contains(stderr.String(), "cloud-token") {
		t.Fatalf("env pull output leaked plaintext\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Variables written: 2") || !strings.Contains(stdout.String(), "Pull event recorded: true") {
		t.Fatalf("output missing pull summary:\n%s", stdout.String())
	}
}

func TestEnvPullDryRunDoesNotWriteOrRecordEvent(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	original := "API_TOKEN=old-token\nLOCAL_ONLY=yes\n"
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte(original), 0o600); err != nil {
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
	bundle := encryptedPullBundle(t, ident, project.TeamID, map[string]string{"API_TOKEN": "cloud-token"})

	var sawEvent bool
	var handlerErr error
	previousClient := configPushHTTPClient
	t.Cleanup(func() { configPushHTTPClient = previousClient })
	configPushHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/scopes/dev/pull-bundle") {
			return testResponse(t, http.StatusOK, bundle), nil
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/events/pull") {
			sawEvent = true
			return testResponse(t, http.StatusOK, map[string]any{"recorded_count": float64(1)}), nil
		}
		handlerErr = fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		return nil, handlerErr
	}), Timeout: 2 * time.Second}

	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"env", "pull",
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
		t.Fatalf("env pull dry run exit = %d, stderr:\n%s", code, stderr.String())
	}
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	if sawEvent {
		t.Fatalf("dry run recorded a pull event")
	}
	if got := readEnv(t, repo); got != original {
		t.Fatalf("dry run changed .env:\n%s", got)
	}
	if !strings.Contains(stdout.String(), "Env pull dry run complete.") || !strings.Contains(stdout.String(), "would update") {
		t.Fatalf("dry-run output missing plan:\n%s", stdout.String())
	}
}

func TestEnvPullNonInteractiveOverwriteRequiresYes(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	original := "API_TOKEN=old-token\n"
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte(original), 0o600); err != nil {
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
	bundle := encryptedPullBundle(t, ident, project.TeamID, map[string]string{"API_TOKEN": "cloud-token"})

	var sawEvent bool
	previousClient := configPushHTTPClient
	t.Cleanup(func() { configPushHTTPClient = previousClient })
	configPushHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/scopes/dev/pull-bundle") {
			return testResponse(t, http.StatusOK, bundle), nil
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/events/pull") {
			sawEvent = true
			return testResponse(t, http.StatusOK, map[string]any{"recorded_count": float64(1)}), nil
		}
		return nil, fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}), Timeout: 2 * time.Second}

	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"env", "pull",
		"--api-url", "http://propagate.test",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitConfirmationRequired {
		t.Fatalf("env pull exit = %d, want %d; stderr:\n%s", code, ExitConfirmationRequired, stderr.String())
	}
	if sawEvent {
		t.Fatalf("blocked env pull recorded a pull event")
	}
	if got := readEnv(t, repo); got != original {
		t.Fatalf("blocked env pull changed .env:\n%s", got)
	}
	if !strings.Contains(stderr.String(), "requires --yes") {
		t.Fatalf("stderr missing --yes guidance:\n%s", stderr.String())
	}
}

func encryptedPullBundle(t *testing.T, ident identity.Identity, teamID string, values map[string]string) apiclient.PullBundleData {
	t.Helper()
	bundle, _ := encryptedPullBundleWithScopeKey(t, ident, teamID, values)
	return bundle
}

func encryptedPullBundleWithScopeKey(t *testing.T, ident identity.Identity, teamID string, values map[string]string) (apiclient.PullBundleData, []byte) {
	t.Helper()
	scopeKey, err := secretcrypto.GenerateScopeKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptedScopeKey, err := secretcrypto.EncryptScopeKey(scopeKey, ident.EncryptionPublicKey, "dev", ident.PublicKeySHA, 1)
	if err != nil {
		t.Fatal(err)
	}
	var versions []apiclient.SecretVersionRecord
	for _, name := range sortedTestNames(values) {
		ciphertext, nonce, err := secretcrypto.EncryptValue(scopeKey, teamID, "dev", ".env", name, 1, values[name])
		if err != nil {
			t.Fatal(err)
		}
		versions = append(versions, apiclient.SecretVersionRecord{
			Name:             name,
			EnvFilePath:      ".env",
			CurrentVersionID: "ver_" + name,
			Ciphertext:       ciphertext,
			Nonce:            nonce,
			Algorithm:        secretcrypto.ValueAlgorithm,
			ScopeKeyVersion:  1,
		})
	}
	return apiclient.PullBundleData{
		Scope:           apiclient.ScopeRef{Name: "dev"},
		ConfigRevision:  "rev_00001",
		EnvFileMappings: []string{".env"},
		ScopeKeyEnvelope: apiclient.ScopeKeyEnvelope{
			Scope:             "dev",
			RecipientKeySHA:   ident.PublicKeySHA,
			ScopeKeyVersion:   1,
			EncryptedScopeKey: encryptedScopeKey,
			Algorithm:         secretcrypto.EnvelopeAlgorithm,
		},
		SecretVersions: versions,
	}, scopeKey
}

func sortedTestNames(values map[string]string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func readEnv(t *testing.T, repo string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

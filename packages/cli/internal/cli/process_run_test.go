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
	"strconv"
	"strings"
	"testing"
	"time"

	"propagate/cli/internal/apiclient"
	"propagate/cli/internal/config"
	"propagate/cli/internal/identity"
	"propagate/cli/internal/secretcrypto"
)

func TestProcessRunInjectsValuesIntoChildProcess(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("API_TOKEN", "local-token")
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("API_TOKEN=local-token\nLOCAL_ONLY=yes\n"), 0o600); err != nil {
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
				handlerErr = fmt.Errorf("process injection event leaked plaintext: %s", body)
				return nil, handlerErr
			}
			var request apiclient.PullEventRequest
			if err := json.Unmarshal(body, &request); err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			if request.Scope != "dev" || request.ConfigRevision != "rev_00001" || request.VariablesCount != 2 || request.Client.ClientKind != "cli_run" {
				handlerErr = fmt.Errorf("unexpected process injection event: %+v", request)
				return nil, handlerErr
			}
			return testResponse(t, http.StatusOK, map[string]any{"event_id": "audit_00001", "recorded_count": float64(1)}), nil
		default:
			handlerErr = fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			return nil, handlerErr
		}
	}), Timeout: 2 * time.Second}

	t.Setenv("PROPAGATE_TEST_PROCESS_RUN_HELPER", "1")
	t.Setenv("PROPAGATE_TEST_EXPECT_API_TOKEN", "cloud-token")
	t.Setenv("PROPAGATE_TEST_EXPECT_DATABASE_URL", "postgres://db")
	expectedCWD, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROPAGATE_TEST_EXPECT_CWD", expectedCWD)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"run",
		"--api-url", "http://propagate.test",
		"--scope", "dev",
		"--",
		exe, "-test.run=TestProcessRunChildHelper",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("process run exit = %d, stdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	if !sawEvent {
		t.Fatalf("fake API did not receive process injection event")
	}
	if strings.TrimSpace(stdout.String()) != "child-ok" {
		t.Fatalf("child stdout = %q", stdout.String())
	}
	if strings.Contains(stderr.String(), "cloud-token") || strings.Contains(stderr.String(), "postgres://db") {
		t.Fatalf("process run stderr leaked plaintext:\n%s", stderr.String())
	}
	if got := readEnv(t, repo); got != "API_TOKEN=local-token\nLOCAL_ONLY=yes\n" {
		t.Fatalf("process run changed .env:\n%s", got)
	}
}

func TestProcessRunReturnsChildExitCode(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("API_TOKEN=local-token\n"), 0o600); err != nil {
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
	}, Streams{In: strings.NewReader(""), Out: &initStdout, Err: &initStderr, WorkDir: repo})
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

	previousClient := configPushHTTPClient
	t.Cleanup(func() { configPushHTTPClient = previousClient })
	configPushHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/scopes/dev/pull-bundle"):
			return testResponse(t, http.StatusOK, bundle), nil
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/events/pull"):
			return testResponse(t, http.StatusOK, map[string]any{"event_id": "audit_00001", "recorded_count": float64(1)}), nil
		default:
			return nil, fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}), Timeout: 2 * time.Second}

	t.Setenv("PROPAGATE_TEST_PROCESS_RUN_HELPER", "1")
	t.Setenv("PROPAGATE_TEST_EXIT_CODE", "17")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code = Run([]string{"run", "--api-url", "http://propagate.test", "--scope", "dev", "--", exe, "-test.run=TestProcessRunChildHelper"}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != 17 {
		t.Fatalf("process run exit = %d, want child exit 17\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
}

func TestProcessRunRejectsMissingCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"run", "--scope", "dev", "--"}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: t.TempDir(),
	})
	if code != ExitUsageError {
		t.Fatalf("process run exit = %d, want %d; stderr:\n%s", code, ExitUsageError, stderr.String())
	}
	if !strings.Contains(stderr.String(), "requires a command after") {
		t.Fatalf("stderr missing command guidance:\n%s", stderr.String())
	}
}

func TestProcessRunRejectsDuplicateVariableNamesAcrossFiles(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("API_TOKEN=local-token\n"), 0o600); err != nil {
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
	}, Streams{In: strings.NewReader(""), Out: &initStdout, Err: &initStderr, WorkDir: repo})
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
	bundle := encryptedPullBundleForFiles(t, ident, project.TeamID, map[envVarKey]string{
		{Path: ".env", Name: "API_TOKEN"}:       "first-secret",
		{Path: ".env.local", Name: "API_TOKEN"}: "second-secret",
	})

	var sawEvent bool
	previousClient := configPushHTTPClient
	t.Cleanup(func() { configPushHTTPClient = previousClient })
	configPushHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/scopes/dev/pull-bundle") {
			return testResponse(t, http.StatusOK, bundle), nil
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/events/pull") {
			sawEvent = true
			return testResponse(t, http.StatusOK, map[string]any{"event_id": "audit_00001", "recorded_count": float64(1)}), nil
		}
		return nil, fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}), Timeout: 2 * time.Second}

	t.Setenv("PROPAGATE_TEST_PROCESS_RUN_HELPER", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code = Run([]string{"run", "--api-url", "http://propagate.test", "--scope", "dev", "--", exe, "-test.run=TestProcessRunChildHelper"}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitValidationError {
		t.Fatalf("process run exit = %d, want %d; stdout:\n%s\nstderr:\n%s", code, ExitValidationError, stdout.String(), stderr.String())
	}
	if sawEvent {
		t.Fatalf("process run recorded event despite duplicate-name rejection")
	}
	if !strings.Contains(stderr.String(), "appears in multiple env files") {
		t.Fatalf("stderr missing duplicate-name guidance:\n%s", stderr.String())
	}
	if strings.Contains(stderr.String(), "first-secret") || strings.Contains(stderr.String(), "second-secret") {
		t.Fatalf("duplicate-name error leaked plaintext:\n%s", stderr.String())
	}
}

func TestProcessRunChildHelper(t *testing.T) {
	if os.Getenv("PROPAGATE_TEST_PROCESS_RUN_HELPER") != "1" {
		return
	}
	if code := os.Getenv("PROPAGATE_TEST_EXIT_CODE"); code != "" {
		parsed, err := strconv.Atoi(code)
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid requested child exit code")
			os.Exit(2)
		}
		os.Exit(parsed)
	}
	if want := os.Getenv("PROPAGATE_TEST_EXPECT_API_TOKEN"); want != "" && os.Getenv("API_TOKEN") != want {
		fmt.Fprintln(os.Stderr, "API_TOKEN mismatch")
		os.Exit(40)
	}
	if want := os.Getenv("PROPAGATE_TEST_EXPECT_DATABASE_URL"); want != "" && os.Getenv("DATABASE_URL") != want {
		fmt.Fprintln(os.Stderr, "DATABASE_URL mismatch")
		os.Exit(41)
	}
	if want := os.Getenv("PROPAGATE_TEST_EXPECT_CWD"); want != "" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "cwd lookup failed")
			os.Exit(42)
		}
		if cwd != want {
			fmt.Fprintln(os.Stderr, "cwd mismatch")
			os.Exit(43)
		}
	}
	fmt.Fprintln(os.Stdout, "child-ok")
	os.Exit(0)
}

func encryptedPullBundleForFiles(t *testing.T, ident identity.Identity, teamID string, values map[envVarKey]string) apiclient.PullBundleData {
	t.Helper()
	scopeKey, err := secretcrypto.GenerateScopeKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptedScopeKey, err := secretcrypto.EncryptScopeKey(scopeKey, ident.EncryptionPublicKey, "dev", ident.PublicKeySHA, 1)
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]envVarKey, 0, len(values))
	mappedFiles := map[string]bool{}
	for key := range values {
		keys = append(keys, key)
		mappedFiles[key.Path] = true
	}
	sortEnvKeys(keys)
	var versions []apiclient.SecretVersionRecord
	for _, key := range keys {
		ciphertext, nonce, err := secretcrypto.EncryptValue(scopeKey, teamID, "dev", key.Path, key.Name, 1, values[key])
		if err != nil {
			t.Fatal(err)
		}
		versions = append(versions, apiclient.SecretVersionRecord{
			Name:             key.Name,
			EnvFilePath:      key.Path,
			CurrentVersionID: "ver_" + strings.ReplaceAll(key.Path, ".", "_") + "_" + key.Name,
			Ciphertext:       ciphertext,
			Nonce:            nonce,
			Algorithm:        secretcrypto.ValueAlgorithm,
			ScopeKeyVersion:  1,
		})
	}
	var envFiles []string
	for path := range mappedFiles {
		envFiles = append(envFiles, path)
	}
	sort.Strings(envFiles)
	return apiclient.PullBundleData{
		Scope:           apiclient.ScopeRef{Name: "dev"},
		ConfigRevision:  "rev_00001",
		EnvFileMappings: envFiles,
		ScopeKeyEnvelope: apiclient.ScopeKeyEnvelope{
			Scope:             "dev",
			RecipientKeySHA:   ident.PublicKeySHA,
			ScopeKeyVersion:   1,
			EncryptedScopeKey: encryptedScopeKey,
			Algorithm:         secretcrypto.EnvelopeAlgorithm,
		},
		SecretVersions: versions,
	}
}

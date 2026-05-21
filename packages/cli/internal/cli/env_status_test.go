package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
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

func TestEnvStatusShowsMaskedValuesAndMetadata(t *testing.T) {
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
	cloudConfig := testConfigData(t, project)
	secret := "super-sensitive-token"
	status := encryptedEnvStatus(t, ident, project.TeamID, map[string]string{"API_TOKEN": secret})

	var handlerErr error
	previousClient := configPushHTTPClient
	t.Cleanup(func() { configPushHTTPClient = previousClient })
	configPushHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get(apiclient.HeaderPublicKeySHA) == "" || r.Header.Get(apiclient.HeaderSignature) == "" {
			handlerErr = fmt.Errorf("request missing signing headers")
			return nil, handlerErr
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/scopes/dev/env/status") {
			return testResponse(t, http.StatusOK, status), nil
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config") {
			return testResponse(t, http.StatusOK, cloudConfig), nil
		}
		handlerErr = fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		return nil, handlerErr
	}), Timeout: 2 * time.Second}

	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"env", "status",
		"--api-url", "http://propagate.test",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("env status exit = %d, stderr:\n%s", code, stderr.String())
	}
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
		t.Fatalf("env status leaked plaintext\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	for _, want := range []string{"API_TOKEN=s**n", "Last updated:", "Can read: true"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, stdout.String())
		}
	}
}

func TestEnvStatusJSONOmitsMaskedValues(t *testing.T) {
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
	cloudConfig := testConfigData(t, project)
	secret := "super-sensitive-token"
	status := encryptedEnvStatus(t, ident, project.TeamID, map[string]string{"API_TOKEN": secret})

	previousClient := configPushHTTPClient
	t.Cleanup(func() { configPushHTTPClient = previousClient })
	configPushHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config") {
			return testResponse(t, http.StatusOK, cloudConfig), nil
		}
		return testResponse(t, http.StatusOK, status), nil
	}), Timeout: 2 * time.Second}

	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"env", "status",
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
		t.Fatalf("env status JSON exit = %d, stderr:\n%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), secret) || strings.Contains(stdout.String(), "s**n") {
		t.Fatalf("JSON output leaked plaintext or masked value:\n%s", stdout.String())
	}
	var result EnvStatusResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("parse env status JSON: %v\n%s", err, stdout.String())
	}
	if len(result.Variables) != 1 || result.Variables[0].Name != "API_TOKEN" {
		t.Fatalf("unexpected JSON variables: %+v", result.Variables)
	}
}

func TestEnvStatusComparesLocalValuesAgainstLatestCloudConfig(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("API_TOKEN=local-old\n"), 0o600); err != nil {
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
	status, scopeKey := encryptedEnvStatusWithScopeKey(t, ident, project.TeamID, map[string]string{
		"API_TOKEN": "cloud-new",
		"NEW_TOKEN": "new-secret",
	})
	cloudProject := project
	cloudProject.CloudRevision = "rev_00002"
	for idx := range cloudProject.Scopes {
		if cloudProject.Scopes[idx].Name != "dev" {
			continue
		}
		cloudProject.Scopes[idx].Variables = []config.VariableDeclaration{
			{
				Name:        "API_TOKEN",
				EnvFilePath: ".env",
				Sensitivity: config.SensitivitySensitive,
				Digest:      secretcrypto.FingerprintValue(scopeKey, project.TeamID, "dev", ".env", "API_TOKEN", 1, "cloud-new"),
			},
			{
				Name:        "NEW_TOKEN",
				EnvFilePath: ".env",
				Sensitivity: config.SensitivitySensitive,
				Digest:      secretcrypto.FingerprintValue(scopeKey, project.TeamID, "dev", ".env", "NEW_TOKEN", 1, "new-secret"),
			},
		}
	}
	cloudConfig := testConfigData(t, cloudProject)

	previousClient := configPushHTTPClient
	t.Cleanup(func() { configPushHTTPClient = previousClient })
	configPushHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config") {
			return testResponse(t, http.StatusOK, cloudConfig), nil
		}
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/scopes/dev/env/status") {
			return testResponse(t, http.StatusOK, status), nil
		}
		return nil, fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}), Timeout: 2 * time.Second}

	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"env", "status",
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
		t.Fatalf("env status JSON exit = %d, stderr:\n%s", code, stderr.String())
	}
	var result EnvStatusResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("parse env status JSON: %v\n%s", err, stdout.String())
	}
	if !result.ConfigStale || result.LocalRevision != "rev_00001" || result.ConfigRevision != "rev_00002" {
		t.Fatalf("expected stale config comparison, got local=%s cloud=%s stale=%t", result.LocalRevision, result.ConfigRevision, result.ConfigStale)
	}
	states := map[string]string{}
	for _, item := range result.Variables {
		states[item.Name] = item.LocalState
	}
	if states["API_TOKEN"] != "different" || states["NEW_TOKEN"] != "missing" {
		t.Fatalf("unexpected local states: %+v", states)
	}
	joinedSteps := strings.Join(result.NextSteps, "\n")
	if !strings.Contains(joinedSteps, "propagate config pull") || !strings.Contains(joinedSteps, "propagate env pull --scope dev") {
		t.Fatalf("expected config/env pull suggestions, got: %+v", result.NextSteps)
	}
}

func testConfigData(t *testing.T, project config.ParsedProject) apiclient.ConfigData {
	t.Helper()
	snapshot, err := config.SnapshotJSON(project)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := config.ConfigHash(project)
	if err != nil {
		t.Fatal(err)
	}
	return apiclient.ConfigData{
		ConfigRevision: project.CloudRevision,
		ConfigHash:     hash,
		ConfigSnapshot: snapshot,
	}
}

func encryptedEnvStatus(t *testing.T, ident identity.Identity, teamID string, values map[string]string) apiclient.EnvStatusData {
	t.Helper()
	bundle, _ := encryptedPullBundleWithScopeKey(t, ident, teamID, values)
	return envStatusFromPullBundle(bundle)
}

func encryptedEnvStatusWithScopeKey(t *testing.T, ident identity.Identity, teamID string, values map[string]string) (apiclient.EnvStatusData, []byte) {
	t.Helper()
	bundle, scopeKey := encryptedPullBundleWithScopeKey(t, ident, teamID, values)
	return envStatusFromPullBundle(bundle), scopeKey
}

func envStatusFromPullBundle(bundle apiclient.PullBundleData) apiclient.EnvStatusData {
	var variables []apiclient.VariableMetadata
	for _, version := range bundle.SecretVersions {
		variables = append(variables, apiclient.VariableMetadata{
			Name:             version.Name,
			EnvFilePath:      version.EnvFilePath,
			CurrentVersionID: version.CurrentVersionID,
			LastUpdatedBy:    "alice@example.com",
			LastUpdatedAt:    "2026-04-30T10:24:00Z",
		})
	}
	return apiclient.EnvStatusData{
		Scope:            bundle.Scope,
		ConfigRevision:   bundle.ConfigRevision,
		Variables:        variables,
		EncryptedValues:  bundle.SecretVersions,
		ScopeKeyEnvelope: &bundle.ScopeKeyEnvelope,
		CanRead:          true,
	}
}

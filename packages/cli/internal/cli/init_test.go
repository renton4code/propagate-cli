package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"propagate/cli/internal/apiclient"
	"propagate/cli/internal/config"
	"propagate/cli/internal/identity"
	"propagate/cli/internal/secretcrypto"
)

func TestInitCreatesMetadataOnlyConfig(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	secret := "super-secret-value"
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("DATABASE_URL="+secret+"\nPUBLIC_FLAG=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"init",
		"--handle", "alice@example.com",
		"--team-name", "Acme API",
		"--yes",
		"--non-interactive",
		"--skip-agent-guidance",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("Run() exit = %d, stderr:\n%s", code, stderr.String())
	}

	configPath := filepath.Join(repo, "propagate.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	configText := string(data)
	if strings.Contains(configText, secret) {
		t.Fatalf("propagate.yaml leaked env value:\n%s", configText)
	}
	if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
		t.Fatalf("command output leaked env value\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	for _, want := range []string{
		`name: "Acme API"`,
		`env_files:`,
		`- ".env"`,
		`role: admins`,
		`pending:`,
	} {
		if !strings.Contains(configText, want) {
			t.Fatalf("propagate.yaml missing %q:\n%s", want, configText)
		}
	}

	identityPath := filepath.Join(home, ".propagate", "identity")
	info, err := os.Stat(identityPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("identity permissions = %o, want 600", got)
	}
}

func TestInitUploadsEncryptedSetupWhenAPIURLConfigured(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	databaseURL := "postgres://user:pass@example.invalid/app"
	publicFlag := "public-looking-but-still-confidential"
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("DATABASE_URL="+databaseURL+"\nPUBLIC_FLAG="+publicFlag+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var sawSetup bool
	var setupTeamID string
	var handlerErr error
	previousClient := configPushHTTPClient
	t.Cleanup(func() { configPushHTTPClient = previousClient })
	configPushHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/teams/setup" {
			handlerErr = fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			return nil, handlerErr
		}
		if r.Header.Get(apiclient.HeaderPublicKeySHA) == "" || r.Header.Get(apiclient.HeaderSignature) == "" || r.Header.Get(apiclient.HeaderOperationID) == "" {
			handlerErr = fmt.Errorf("setup request missing signing headers")
			return nil, handlerErr
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			handlerErr = err
			return nil, handlerErr
		}
		if bytes.Contains(body, []byte(databaseURL)) || bytes.Contains(body, []byte(publicFlag)) {
			handlerErr = fmt.Errorf("setup request leaked plaintext env value")
			return nil, handlerErr
		}

		var request apiclient.TeamSetupRequest
		if err := json.Unmarshal(body, &request); err != nil {
			handlerErr = err
			return nil, handlerErr
		}
		if request.OperationID == "" {
			handlerErr = fmt.Errorf("operation_id was empty")
			return nil, handlerErr
		}
		if request.TeamName != "Acme API" {
			handlerErr = fmt.Errorf("team_name = %q", request.TeamName)
			return nil, handlerErr
		}
		if len(request.Scopes) != 1 || request.Scopes[0].Name != "dev" {
			handlerErr = fmt.Errorf("scopes = %+v", request.Scopes)
			return nil, handlerErr
		}
		if len(request.ScopeKeyEnvelopes) != 1 {
			handlerErr = fmt.Errorf("scope key envelopes = %d, want 1", len(request.ScopeKeyEnvelopes))
			return nil, handlerErr
		}
		if len(request.EncryptedSecretVersions) != 2 {
			handlerErr = fmt.Errorf("encrypted secret versions = %d, want 2", len(request.EncryptedSecretVersions))
			return nil, handlerErr
		}
		if len(request.Scopes[0].Variables) != 2 {
			handlerErr = fmt.Errorf("scope variable declarations = %d, want 2", len(request.Scopes[0].Variables))
			return nil, handlerErr
		}
		for _, declaration := range request.Scopes[0].Variables {
			if declaration.Sensitivity != config.SensitivitySensitive || !strings.HasPrefix(declaration.Digest, "hmac-sha-256:v1:") {
				handlerErr = fmt.Errorf("unexpected declaration: %+v", declaration)
				return nil, handlerErr
			}
		}

		var snapshot struct {
			Team struct {
				ID string `json:"id"`
			} `json:"team"`
			Pending struct {
				Joins         []any `json:"joins"`
				AccessChanges []any `json:"access_changes"`
			} `json:"pending"`
		}
		if err := json.Unmarshal(request.ConfigSnapshot, &snapshot); err != nil {
			handlerErr = err
			return nil, handlerErr
		}
		if snapshot.Team.ID == "" {
			handlerErr = fmt.Errorf("snapshot team id was empty")
			return nil, handlerErr
		}
		if bytes.Contains(request.ConfigSnapshot, []byte(databaseURL)) || bytes.Contains(request.ConfigSnapshot, []byte(publicFlag)) {
			handlerErr = fmt.Errorf("config snapshot leaked plaintext env value")
			return nil, handlerErr
		}
		if len(snapshot.Pending.Joins) != 0 || len(snapshot.Pending.AccessChanges) != 0 {
			handlerErr = fmt.Errorf("initial snapshot had pending changes: %s", request.ConfigSnapshot)
			return nil, handlerErr
		}

		adminIdent, err := identity.Load()
		if err != nil {
			handlerErr = err
			return nil, handlerErr
		}
		envelope := request.ScopeKeyEnvelopes[0]
		scopeKey, err := secretcrypto.DecryptScopeKey(
			adminIdent.EncryptionPrivateKey,
			envelope.EncryptedScopeKey,
			envelope.Algorithm,
			envelope.Scope,
			envelope.RecipientKeySHA,
			envelope.ScopeKeyVersion,
		)
		if err != nil {
			handlerErr = err
			return nil, handlerErr
		}
		decrypted := map[string]string{}
		for _, version := range request.EncryptedSecretVersions {
			value, err := secretcrypto.DecryptValue(
				scopeKey,
				snapshot.Team.ID,
				version.Scope,
				version.EnvFilePath,
				version.Name,
				version.ScopeKeyVersion,
				version.Ciphertext,
				version.Nonce,
				version.Algorithm,
			)
			if err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			decrypted[version.Name] = value
		}
		if decrypted["DATABASE_URL"] != databaseURL || decrypted["PUBLIC_FLAG"] != publicFlag {
			handlerErr = fmt.Errorf("encrypted setup values were not decryptable with setup envelope")
			return nil, handlerErr
		}

		sawSetup = true
		setupTeamID = snapshot.Team.ID
		return testResponse(t, http.StatusCreated, map[string]any{
			"team_id":                   setupTeamID,
			"config_revision":           "rev_00001",
			"config_hash":               "sha256:setup",
			"scopes_created":            []string{"dev"},
			"encrypted_variables_count": float64(2),
			"envelopes_count":           float64(1),
		}), nil
	}), Timeout: 2 * time.Second}

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"init",
		"--handle", "alice@example.com",
		"--team-name", "Acme API",
		"--api-url", "http://propagate.test",
		"--yes",
		"--non-interactive",
		"--skip-agent-guidance",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("Run() exit = %d, stderr:\n%s", code, stderr.String())
	}
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	if !sawSetup {
		t.Fatalf("fake API did not receive team setup")
	}

	project, err := config.ReadProject(filepath.Join(repo, "propagate.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if project.TeamID != setupTeamID {
		t.Fatalf("team id = %s, want setup id %s", project.TeamID, setupTeamID)
	}
	if project.CloudRevision != "rev_00001" || project.SyncStatus != "synced" {
		t.Fatalf("cloud sync fields = %s/%s", project.CloudRevision, project.SyncStatus)
	}
	configText := readConfig(t, repo)
	if strings.Contains(configText, databaseURL) || strings.Contains(configText, publicFlag) {
		t.Fatalf("propagate.yaml leaked env value:\n%s", configText)
	}
	if !strings.Contains(configText, `digest: "hmac-sha-256:v1:`) || !strings.Contains(configText, `name: "DATABASE_URL"`) {
		t.Fatalf("propagate.yaml missing variable digest declarations:\n%s", configText)
	}
	if !strings.Contains(stdout.String(), "Variables encrypted/uploaded: 2") {
		t.Fatalf("stdout missing encrypted upload count:\n%s", stdout.String())
	}
}

func TestInitDoesNotOverwriteExistingConfig(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	existing := "version: 1\nteam:\n  name: Existing\n"
	configPath := filepath.Join(repo, "propagate.yaml")
	if err := os.WriteFile(configPath, []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"init",
		"--handle", "bob@example.com",
		"--yes",
		"--non-interactive",
		"--skip-agent-guidance",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("Run() exit = %d, stderr:\n%s", code, stderr.String())
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != existing {
		t.Fatalf("existing config was overwritten:\n%s", data)
	}
	if !strings.Contains(stdout.String(), "Project config already exists") {
		t.Fatalf("expected existing config output, got:\n%s", stdout.String())
	}
}

func TestInitNonInteractiveRequiresHandleForNewIdentity(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"init",
		"--team-name", "Acme API",
		"--yes",
		"--non-interactive",
		"--skip-agent-guidance",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitConfirmationRequired {
		t.Fatalf("Run() exit = %d, want %d; stderr:\n%s", code, ExitConfirmationRequired, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Handle") {
		t.Fatalf("expected handle error, got:\n%s", stderr.String())
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("git", "init")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
	return dir
}

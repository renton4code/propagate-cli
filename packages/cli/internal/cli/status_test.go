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
)

func TestStatusJSONCombinesSeparateStatuses(t *testing.T) {
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
	envStatus := encryptedEnvStatus(t, ident, project.TeamID, map[string]string{"API_TOKEN": secret})
	teamStatus := apiclient.TeamStatusData{
		Team: apiclient.TeamSummary{
			ID:             project.TeamID,
			Name:           project.TeamName,
			ConfigRevision: "rev_00001",
			ConfigHash:     "sha256:cloud",
		},
		Actor: apiclient.Member{
			PublicIdentity: apiclient.PublicIdentity{
				Handle:              ident.Handle,
				PublicKeySHA:        ident.PublicKeySHA,
				SigningPublicKey:    "raw-signing-public-key",
				EncryptionPublicKey: "raw-encryption-public-key",
			},
			Management: true,
			Status:     "active",
		},
		Members: map[string][]apiclient.Member{
			"management": {
				{
					PublicIdentity: apiclient.PublicIdentity{
						Handle:              ident.Handle,
						PublicKeySHA:        ident.PublicKeySHA,
						SigningPublicKey:    "raw-signing-public-key",
						EncryptionPublicKey: "raw-encryption-public-key",
					},
					Management: true,
					Status:     "active",
				},
			},
		},
	}

	requests := map[string]int{}
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
			requests["config"]++
			return testResponse(t, http.StatusOK, map[string]any{
				"local_revision":     "rev_00001",
				"cloud_revision":     "rev_00001",
				"local_config_hash":  r.URL.Query().Get("local_config_hash"),
				"cloud_config_hash":  "sha256:cloud",
				"state":              "equal",
				"recommended_action": "none",
			}), nil
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/scopes/dev/env/status"):
			requests["env"]++
			return testResponse(t, http.StatusOK, envStatus), nil
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/status"):
			requests["team"]++
			return testResponse(t, http.StatusOK, teamStatus), nil
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config"):
			requests["cloud_config"]++
			return testResponse(t, http.StatusOK, cloudConfig), nil
		default:
			handlerErr = fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			return nil, handlerErr
		}
	}), Timeout: 2 * time.Second}

	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"status",
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
		t.Fatalf("status exit = %d, stderr:\n%s", code, stderr.String())
	}
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	var result StatusResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("parse status JSON: %v\n%s", err, stdout.String())
	}
	if !result.OK || result.Status != "success" {
		t.Fatalf("unexpected unified status: %+v", result)
	}
	if result.Config == nil || result.Config.State != "equal" {
		t.Fatalf("config status missing or unexpected: %+v", result.Config)
	}
	if result.Team == nil || !result.Team.CurrentManagement {
		t.Fatalf("team status missing or unexpected: %+v", result.Team)
	}
	if result.Env == nil || result.Env.VariablesCount != 1 {
		t.Fatalf("env status missing or unexpected: %+v", result.Env)
	}
	if strings.Contains(stdout.String(), secret) || strings.Contains(stdout.String(), "raw-signing-public-key") || strings.Contains(stdout.String(), "raw-encryption-public-key") {
		t.Fatalf("status JSON leaked secret or raw public key material:\n%s", stdout.String())
	}
	for _, key := range []string{"config", "team", "env", "cloud_config"} {
		if requests[key] != 1 {
			t.Fatalf("%s requests = %d, want 1", key, requests[key])
		}
	}
}

func TestStatusShowsPartialLocalFactsWithoutAPIURL(t *testing.T) {
	repo, _, _ := initTeamStatusProject(t)

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"status",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitCloudUnavailable {
		t.Fatalf("status exit = %d, want %d; stderr:\n%s", code, ExitCloudUnavailable, stderr.String())
	}
	for _, want := range []string{
		"Unified status incomplete",
		"Config status",
		"Local revision: rev_00001",
		"Team status",
		"Members:",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout missing %q:\n%s", want, stdout.String())
		}
	}
	for _, want := range []string{
		"config status failed: Propagate API URL is required for config status",
		"team status failed: Propagate API URL is required for team status",
		"env status failed: Propagate API URL is required for env status",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr missing %q:\n%s", want, stderr.String())
		}
	}
}

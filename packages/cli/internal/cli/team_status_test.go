package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"propagate/cli/internal/apiclient"
	"propagate/cli/internal/config"
	"propagate/cli/internal/identity"
)

func TestTeamStatusShowsCloudActivity(t *testing.T) {
	repo, ident, project := initTeamStatusProject(t)

	rawSigningKey := "raw-cloud-signing-public-key"
	rawEncryptionKey := "raw-cloud-encryption-public-key"
	cloudStatus := apiclient.TeamStatusData{
		Team: apiclient.TeamSummary{
			ID:             project.TeamID,
			Name:           "Acme API",
			ConfigRevision: "rev_00002",
			ConfigHash:     "sha256:cloud",
		},
		Actor: apiclient.Member{
			PublicIdentity: apiclient.PublicIdentity{
				Handle:              ident.Handle,
				PublicKeySHA:        ident.PublicKeySHA,
				SigningPublicKey:    rawSigningKey,
				EncryptionPublicKey: rawEncryptionKey,
			},
			Role:   "admins",
			Status: "active",
		},
		Members: map[string][]apiclient.Member{
			"admins": {
				{
					PublicIdentity: apiclient.PublicIdentity{
						Handle:              ident.Handle,
						PublicKeySHA:        ident.PublicKeySHA,
						SigningPublicKey:    rawSigningKey,
						EncryptionPublicKey: rawEncryptionKey,
					},
					Role:   "admins",
					Status: "active",
				},
			},
			"developers": {
				{
					PublicIdentity: apiclient.PublicIdentity{
						Handle:              "bob@example.com",
						PublicKeySHA:        "sha256:bob",
						SigningPublicKey:    "bob-signing-public-key",
						EncryptionPublicKey: "bob-encryption-public-key",
					},
					Role:   "developers",
					Status: "active",
				},
			},
		},
		PendingOrRecentAccess: json.RawMessage(`{"pending_role_changes":1}`),
		LastPulls: []apiclient.PullActivity{
			{
				MemberPublicKeySHA: ident.PublicKeySHA,
				Handle:             ident.Handle,
				Scope:              "dev",
				LastPulledAt:       "2026-05-04T10:00:00Z",
			},
		},
		NeverPulled: []apiclient.Member{
			{
				PublicIdentity: apiclient.PublicIdentity{
					Handle:              "bob@example.com",
					PublicKeySHA:        "sha256:bob",
					SigningPublicKey:    "bob-signing-public-key",
					EncryptionPublicKey: "bob-encryption-public-key",
				},
				Role:   "developers",
				Status: "active",
			},
		},
	}

	var handlerErr error
	previousClient := configPushHTTPClient
	t.Cleanup(func() { configPushHTTPClient = previousClient })
	configPushHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get(apiclient.HeaderPublicKeySHA) == "" || r.Header.Get(apiclient.HeaderSignature) == "" {
			handlerErr = fmt.Errorf("request missing signing headers")
			return nil, handlerErr
		}
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/status") {
			handlerErr = fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			return nil, handlerErr
		}
		return testResponse(t, http.StatusOK, cloudStatus), nil
	}), Timeout: 2 * time.Second}

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"team", "status",
		"--api-url", "http://propagate.test",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("team status exit = %d, stderr:\n%s", code, stderr.String())
	}
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	for _, want := range []string{"Team status complete.", "Current access: management", "Members:", "alice@example.com", "bob@example.com", "Last pulls:", "Never pulled:", "Cloud pending/recent access:"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, stdout.String())
		}
	}
	for _, forbidden := range []string{rawSigningKey, rawEncryptionKey, "bob-signing-public-key", "bob-encryption-public-key", "signing_public_key", "encryption_public_key"} {
		if strings.Contains(stdout.String(), forbidden) || strings.Contains(stderr.String(), forbidden) {
			t.Fatalf("team status leaked raw key field/value %q\nstdout:\n%s\nstderr:\n%s", forbidden, stdout.String(), stderr.String())
		}
	}
}

func TestTeamStatusJSONOmitsRawPublicKeys(t *testing.T) {
	repo, ident, project := initTeamStatusProject(t)

	previousClient := configPushHTTPClient
	t.Cleanup(func() { configPushHTTPClient = previousClient })
	configPushHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return testResponse(t, http.StatusOK, apiclient.TeamStatusData{
			Team: apiclient.TeamSummary{
				ID:             project.TeamID,
				Name:           project.TeamName,
				ConfigRevision: "rev_00002",
				ConfigHash:     "sha256:cloud",
			},
			Actor: apiclient.Member{
				PublicIdentity: apiclient.PublicIdentity{
					Handle:              ident.Handle,
					PublicKeySHA:        ident.PublicKeySHA,
					SigningPublicKey:    "json-signing-public-key",
					EncryptionPublicKey: "json-encryption-public-key",
				},
				Role:   "admins",
				Status: "active",
			},
			Members: map[string][]apiclient.Member{
				"admins": {
					{
						PublicIdentity: apiclient.PublicIdentity{
							Handle:              ident.Handle,
							PublicKeySHA:        ident.PublicKeySHA,
							SigningPublicKey:    "json-signing-public-key",
							EncryptionPublicKey: "json-encryption-public-key",
						},
						Role:   "admins",
						Status: "active",
					},
				},
			},
		}), nil
	}), Timeout: 2 * time.Second}

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"team", "status",
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
		t.Fatalf("team status exit = %d, stderr:\n%s", code, stderr.String())
	}
	for _, forbidden := range []string{"json-signing-public-key", "json-encryption-public-key", "signing_public_key", "encryption_public_key"} {
		if strings.Contains(stdout.String(), forbidden) || strings.Contains(stderr.String(), forbidden) {
			t.Fatalf("team status JSON leaked raw key field/value %q\nstdout:\n%s\nstderr:\n%s", forbidden, stdout.String(), stderr.String())
		}
	}
	if !strings.Contains(stdout.String(), `"current_role": "management"`) {
		t.Fatalf("JSON output missing current role:\n%s", stdout.String())
	}
}

func TestTeamStatusShowsLocalFactsWithoutAPIURL(t *testing.T) {
	repo, _, _ := initTeamStatusProject(t)

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"team", "status",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitCloudUnavailable {
		t.Fatalf("team status exit = %d, want %d; stderr:\n%s", code, ExitCloudUnavailable, stderr.String())
	}
	for _, want := range []string{"Team local status available", "Team: Acme API", "Current access: management", "Members:", "Audit available: false"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("local output missing %q:\n%s", want, stdout.String())
		}
	}
	if !strings.Contains(stderr.String(), "Propagate API URL is required for team status") {
		t.Fatalf("stderr missing API URL guidance:\n%s", stderr.String())
	}
}

func initTeamStatusProject(t *testing.T) (string, identity.Identity, config.ParsedProject) {
	t.Helper()
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

	ident, err := identity.Load()
	if err != nil {
		t.Fatal(err)
	}
	project, err := config.ReadProject(filepath.Join(repo, "propagate.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return repo, ident, project
}

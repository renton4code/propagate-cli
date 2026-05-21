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
)

func TestQuickstartSetsUpProjectAndCreatesInvite(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	unsetEnvForTest(t, "PROPAGATE_API_URL")

	secret := "postgres://user:pass@example.invalid/app"
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("DATABASE_URL="+secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var sawSetup bool
	var sawInvite bool
	var setupTeamID string
	var handlerErr error
	previousClient := configPushHTTPClient
	t.Cleanup(func() { configPushHTTPClient = previousClient })
	configPushHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/teams/setup":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			if bytes.Contains(body, []byte(secret)) {
				handlerErr = fmt.Errorf("quickstart setup request leaked plaintext env value")
				return nil, handlerErr
			}
			var request apiclient.TeamSetupRequest
			if err := json.Unmarshal(body, &request); err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			if request.TeamName != "Acme API" {
				handlerErr = fmt.Errorf("team_name = %q", request.TeamName)
				return nil, handlerErr
			}
			if len(request.EncryptedSecretVersions) != 1 {
				handlerErr = fmt.Errorf("encrypted secret versions = %d, want 1", len(request.EncryptedSecretVersions))
				return nil, handlerErr
			}
			var snapshot struct {
				Team struct {
					ID string `json:"id"`
				} `json:"team"`
			}
			if err := json.Unmarshal(request.ConfigSnapshot, &snapshot); err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			setupTeamID = snapshot.Team.ID
			sawSetup = true
			return testResponse(t, http.StatusCreated, map[string]any{
				"team_id":                   setupTeamID,
				"config_revision":           "rev_00001",
				"config_hash":               "sha256:setup",
				"scopes_created":            []string{"dev"},
				"encrypted_variables_count": float64(1),
				"envelopes_count":           float64(1),
			}), nil
		case r.Method == http.MethodGet && r.URL.Path == "/v1/relay-public-key":
			return testResponse(t, http.StatusOK, apiclient.RelayPublicKeyData{}), nil
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/invites"):
			if setupTeamID == "" || !strings.Contains(r.URL.Path, setupTeamID) {
				handlerErr = fmt.Errorf("invite path %q did not include setup team id %q", r.URL.Path, setupTeamID)
				return nil, handlerErr
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			var request apiclient.CreateTeamInviteRequest
			if err := json.Unmarshal(body, &request); err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			if request.Label != "Bob onboarding" {
				handlerErr = fmt.Errorf("invite label = %q", request.Label)
				return nil, handlerErr
			}
			if got := request.RequestedScopes["dev"]; got != "read" {
				handlerErr = fmt.Errorf("requested dev scope = %q, want read", got)
				return nil, handlerErr
			}
			sawInvite = true
			return testResponse(t, http.StatusCreated, apiclient.CreateTeamInviteResult{
				InviteID: "inv_test",
				PIN:      "4821-F",
				Label:    "Bob onboarding",
			}), nil
		default:
			handlerErr = fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			return nil, handlerErr
		}
	}), Timeout: 2 * time.Second}

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"quickstart",
		"--handle", "alice@example.com",
		"--team-name", "Acme API",
		"--invite-label", "Bob onboarding",
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
		t.Fatalf("quickstart exit = %d, stderr:\n%s", code, stderr.String())
	}
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	if !sawSetup || !sawInvite {
		t.Fatalf("saw setup=%v invite=%v", sawSetup, sawInvite)
	}
	if _, err := os.Stat(filepath.Join(repo, "propagate.yaml")); err != nil {
		t.Fatal(err)
	}
	output := stdout.String()
	for _, want := range []string{
		"Project setup & team invites",
		"Project setup complete.",
		"Team member invite created.",
		"PIN: 4821-F",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("quickstart output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, secret) || strings.Contains(stderr.String(), secret) {
		t.Fatalf("quickstart output leaked env value\nstdout:\n%s\nstderr:\n%s", output, stderr.String())
	}
}

func TestQuickstartCanCreateMultipleInvitesWithAccessPrompts(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	unsetEnvForTest(t, "PROPAGATE_API_URL")

	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("SECRET=val\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	type inviteRequest struct {
		label      string
		management bool
		scopes     map[string]string
	}
	var inviteRequests []inviteRequest
	var setupTeamID string
	var handlerErr error
	previousClient := configPushHTTPClient
	t.Cleanup(func() { configPushHTTPClient = previousClient })
	configPushHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/teams/setup":
			var request apiclient.TeamSetupRequest
			body, err := io.ReadAll(r.Body)
			if err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			if err := json.Unmarshal(body, &request); err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			var snapshot struct {
				Team struct {
					ID string `json:"id"`
				} `json:"team"`
			}
			if err := json.Unmarshal(request.ConfigSnapshot, &snapshot); err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			setupTeamID = snapshot.Team.ID
			return testResponse(t, http.StatusCreated, map[string]any{
				"team_id":                   setupTeamID,
				"config_revision":           "rev_00001",
				"config_hash":               "sha256:setup",
				"scopes_created":            []string{"dev"},
				"encrypted_variables_count": float64(1),
				"envelopes_count":           float64(1),
			}), nil
		case r.Method == http.MethodGet && r.URL.Path == "/v1/relay-public-key":
			return testResponse(t, http.StatusOK, apiclient.RelayPublicKeyData{}), nil
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/invites"):
			if setupTeamID == "" || !strings.Contains(r.URL.Path, setupTeamID) {
				handlerErr = fmt.Errorf("invite path %q did not include setup team id %q", r.URL.Path, setupTeamID)
				return nil, handlerErr
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			var request apiclient.CreateTeamInviteRequest
			if err := json.Unmarshal(body, &request); err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			inviteRequests = append(inviteRequests, inviteRequest{
				label:      request.Label,
				management: request.RequestedManagement,
				scopes:     request.RequestedScopes,
			})
			if len(inviteRequests) == 1 {
				return testResponse(t, http.StatusCreated, apiclient.CreateTeamInviteResult{
					InviteID: "inv_bob",
					PIN:      "4821-F",
					Label:    request.Label,
				}), nil
			}
			return testResponse(t, http.StatusCreated, apiclient.CreateTeamInviteResult{
				InviteID: "inv_carol",
				PIN:      "9138-Q",
				Label:    request.Label,
			}), nil
		default:
			handlerErr = fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			return nil, handlerErr
		}
	}), Timeout: 2 * time.Second}

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"quickstart",
		"--handle", "alice@example.com",
		"--team-name", "Acme API",
		"--api-url", "http://propagate.test",
		"--yes",
		"--skip-agent-guidance",
	}, Streams{
		In:      strings.NewReader("Bob onboarding\nn\ndev=write\ny\nCarol manager\ny\n\nn\n"),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("quickstart exit = %d, stderr:\n%s", code, stderr.String())
	}
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	if len(inviteRequests) != 2 {
		t.Fatalf("invite requests = %d, want 2", len(inviteRequests))
	}
	if inviteRequests[0].label != "Bob onboarding" || inviteRequests[0].management {
		t.Fatalf("first invite access = %#v", inviteRequests[0])
	}
	if got := inviteRequests[0].scopes["dev"]; got != "write" {
		t.Fatalf("first invite dev scope = %q, want write", got)
	}
	if inviteRequests[1].label != "Carol manager" || !inviteRequests[1].management {
		t.Fatalf("second invite access = %#v", inviteRequests[1])
	}
	if got := inviteRequests[1].scopes["dev"]; got != "write" {
		t.Fatalf("second invite dev scope = %q, want write", got)
	}

	output := stdout.String()
	for _, want := range []string{
		"[1/2] Setting up project",
		"[2/2] Inviting team",
		"2 team member invites created.",
		"PIN: 4821-F",
		"PIN: 9138-Q",
		"Management: true",
		"dev: write",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("quickstart output missing %q:\n%s", want, output)
		}
	}
}

func TestQuickstartRequiresInviteLabelAfterInit(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("SECRET=val\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	previousClient := configPushHTTPClient
	t.Cleanup(func() { configPushHTTPClient = previousClient })
	configPushHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/teams/setup":
			return testResponse(t, http.StatusCreated, map[string]any{
				"team_id":                   "team_test",
				"config_revision":           "rev_00001",
				"config_hash":               "sha256:test",
				"scopes_created":            []string{"dev"},
				"encrypted_variables_count": float64(1),
				"envelopes_count":           float64(1),
			}), nil
		case r.Method == http.MethodGet && r.URL.Path == "/v1/relay-public-key":
			return testResponse(t, http.StatusOK, apiclient.RelayPublicKeyData{}), nil
		default:
			return nil, fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	})}

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"quickstart",
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
	if code != ExitConfirmationRequired {
		t.Fatalf("quickstart exit = %d, want %d; stderr:\n%s", code, ExitConfirmationRequired, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(repo, "propagate.yaml")); os.IsNotExist(err) {
		t.Fatal("propagate.yaml should be written before invite label is prompted")
	}
	if !strings.Contains(stderr.String(), "Team member invite label is required") {
		t.Fatalf("stderr missing label requirement:\n%s", stderr.String())
	}
}

func TestQuickstartWithExistingConfigRunsTeamJoinInit(t *testing.T) {
	repo := initGitRepo(t)
	adminHome := t.TempDir()
	t.Setenv("HOME", adminHome)
	unsetEnvForTest(t, "PROPAGATE_API_URL")

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

	var sawJoinRequest bool
	var sawInviteList bool
	var handlerErr error
	previousClient := configPushHTTPClient
	t.Cleanup(func() { configPushHTTPClient = previousClient })
	configPushHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/join/invites") && r.Method == http.MethodGet:
			sawInviteList = true
			return testResponse(t, http.StatusOK, apiclient.JoinerInvitesData{}), nil
		case strings.HasSuffix(r.URL.Path, "/join/requests") && r.Method == http.MethodPost:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			var request apiclient.JoinRequestSubmission
			if err := json.Unmarshal(body, &request); err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			if request.Joiner.Handle != "bob@example.com" {
				handlerErr = fmt.Errorf("joiner handle = %q", request.Joiner.Handle)
				return nil, handlerErr
			}
			if got := request.RequestedScopes["dev"]; got != "read" {
				handlerErr = fmt.Errorf("requested dev scope = %q, want read", got)
				return nil, handlerErr
			}
			sawJoinRequest = true
			return testResponse(t, http.StatusCreated, map[string]any{"status": "pending"}), nil
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/invites"):
			handlerErr = fmt.Errorf("quickstart existing-config path should not create an invite")
			return nil, handlerErr
		case r.Method == http.MethodPost && r.URL.Path == "/v1/teams/setup":
			handlerErr = fmt.Errorf("quickstart existing-config path should not run team setup")
			return nil, handlerErr
		default:
			handlerErr = fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			return nil, handlerErr
		}
	}), Timeout: 2 * time.Second}

	devHome := t.TempDir()
	t.Setenv("HOME", devHome)
	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"quickstart",
		"--handle", "bob@example.com",
		"--api-url", "http://propagate.test",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("quickstart existing config exit = %d, stderr:\n%s", code, stderr.String())
	}
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	if !sawInviteList || !sawJoinRequest {
		t.Fatalf("saw invite list=%v join request=%v", sawInviteList, sawJoinRequest)
	}
	output := stdout.String()
	for _, want := range []string{
		"Joining team",
		"Init completed before join.",
		"Project config already existed.",
		"Join request submitted to the server.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("quickstart existing-config output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "Developer invite created") || strings.Contains(output, "PIN:") {
		t.Fatalf("quickstart existing-config output should not render invite creation:\n%s", output)
	}
	if strings.Contains(output, "Add Propagate guidance to AGENTS.md?") || strings.Contains(output, "Agent guidance:") {
		t.Fatalf("quickstart existing-config output should not prompt for AGENTS.md guidance:\n%s", output)
	}
	if _, err := os.Stat(filepath.Join(repo, "AGENTS.md")); !os.IsNotExist(err) {
		t.Fatalf("quickstart existing-config should not create AGENTS.md by default: %v", err)
	}
}

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

func TestConfigPushApprovesPendingJoin(t *testing.T) {
	repo := initGitRepo(t)
	adminHome := t.TempDir()
	t.Setenv("HOME", adminHome)

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
	adminIdent, err := identity.Load()
	if err != nil {
		t.Fatal(err)
	}
	setConfigRevision(t, repo, "rev_00001")

	devHome := t.TempDir()
	t.Setenv("HOME", devHome)
	var joinStdout, joinStderr bytes.Buffer
	code = Run([]string{
		"team", "join",
		"--handle", "bob@example.com",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &joinStdout,
		Err:     &joinStderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("team join exit = %d, stderr:\n%s", code, joinStderr.String())
	}
	pendingProject, err := config.ReadProject(filepath.Join(repo, "propagate.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(pendingProject.PendingJoins) != 1 {
		t.Fatalf("pending joins = %d, want 1", len(pendingProject.PendingJoins))
	}
	pending := pendingProject.PendingJoins[0]
	scopeKey, err := secretcrypto.GenerateScopeKey()
	if err != nil {
		t.Fatal(err)
	}
	adminEnvelope, err := secretcrypto.EncryptScopeKey(scopeKey, adminIdent.EncryptionPublicKey, "dev", adminIdent.PublicKeySHA, 1)
	if err != nil {
		t.Fatal(err)
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
			if got := r.URL.Query().Get("local_revision"); got != "rev_00001" {
				handlerErr = fmt.Errorf("local_revision = %q", got)
				return nil, handlerErr
			}
			return testResponse(t, http.StatusOK, map[string]any{
				"local_revision":     "rev_00001",
				"cloud_revision":     "rev_00001",
				"local_config_hash":  r.URL.Query().Get("local_config_hash"),
				"cloud_config_hash":  "sha256:cloud",
				"state":              "local_ahead",
				"recommended_action": "push",
				"safe_summary":       map[string]any{"members_count": float64(1)},
			}), nil
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/scopes/dev/key-envelope"):
			return testResponse(t, http.StatusOK, apiclient.ScopeEnvelopeData{
				Scope:           apiclient.ScopeRef{Name: "dev"},
				ConfigRevision:  "rev_00001",
				ScopeKeyVersion: 1,
				ScopeKeyEnvelope: apiclient.ScopeKeyEnvelope{
					Scope:             "dev",
					RecipientKeySHA:   adminIdent.PublicKeySHA,
					ScopeKeyVersion:   1,
					EncryptedScopeKey: adminEnvelope,
					Algorithm:         secretcrypto.EnvelopeAlgorithm,
				},
				Algorithm: secretcrypto.EnvelopeAlgorithm,
			}), nil
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/config/push"):
			sawPush = true
			body, err := io.ReadAll(r.Body)
			if err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			var request struct {
				OperationID            string          `json:"operation_id"`
				ExpectedConfigRevision string          `json:"expected_config_revision"`
				TargetConfigSnapshot   json.RawMessage `json:"target_config_snapshot"`
				Decisions              struct {
					Approved []apiclient.ConfigDecision `json:"approved"`
				} `json:"decisions"`
				ScopeKeyEnvelopes []apiclient.ScopeKeyEnvelope `json:"scope_key_envelopes"`
			}
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
			if len(request.Decisions.Approved) != 2 || request.Decisions.Approved[0].PublicKeySHA != pending.PublicKeySHA {
				handlerErr = fmt.Errorf("approved decisions did not include pending join: %+v", request.Decisions.Approved)
				return nil, handlerErr
			}
			accessDecision := request.Decisions.Approved[1]
			if accessDecision.Type != "scope_access_change" || accessDecision.PublicKeySHA != pending.PublicKeySHA || accessDecision.Scope != "dev" || accessDecision.Permission != "read" {
				handlerErr = fmt.Errorf("approved decisions did not include scope access grant: %+v", request.Decisions.Approved)
				return nil, handlerErr
			}
			var snapshot struct {
				Members []config.Member `json:"members"`
				Pending struct {
					Joins []config.JoinRequest `json:"joins"`
				} `json:"pending"`
			}
			if err := json.Unmarshal(request.TargetConfigSnapshot, &snapshot); err != nil {
				handlerErr = err
				return nil, handlerErr
			}
			if len(snapshot.Pending.Joins) != 0 {
				handlerErr = fmt.Errorf("cloud target snapshot included pending joins: %s", request.TargetConfigSnapshot)
				return nil, handlerErr
			}
			if findMember(snapshot.Members, pending.PublicKeySHA) == nil {
				handlerErr = fmt.Errorf("cloud target snapshot missing approved member: %s", request.TargetConfigSnapshot)
				return nil, handlerErr
			}
			if len(request.ScopeKeyEnvelopes) != 1 {
				handlerErr = fmt.Errorf("scope key envelopes = %d, want 1", len(request.ScopeKeyEnvelopes))
				return nil, handlerErr
			}
			envelope := request.ScopeKeyEnvelopes[0]
			if envelope.Scope != "dev" || envelope.RecipientKeySHA != pending.PublicKeySHA || envelope.EncryptedScopeKey == "" {
				handlerErr = fmt.Errorf("unexpected approved member envelope: %+v", envelope)
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

	t.Setenv("HOME", adminHome)
	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"config", "push",
		"--api-url", "http://propagate.test",
		"--approve-join", pending.PublicKeySHA,
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
	accepted, err := config.ReadProject(filepath.Join(repo, "propagate.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if accepted.CloudRevision != "rev_00002" {
		t.Fatalf("cloud revision = %s", accepted.CloudRevision)
	}
	if len(accepted.PendingJoins) != 0 {
		t.Fatalf("pending joins remain after approval: %+v", accepted.PendingJoins)
	}
	if findMember(accepted.Members, pending.PublicKeySHA) == nil {
		t.Fatalf("approved join was not promoted to active member:\n%s", readConfig(t, repo))
	}
	if !strings.Contains(stdout.String(), "Approved joins: 1") {
		t.Fatalf("output missing decision summary:\n%s", stdout.String())
	}
}

func TestConfigPullUpdatesLocalConfigFromCloud(t *testing.T) {
	repo := initGitRepo(t)
	adminHome := t.TempDir()
	t.Setenv("HOME", adminHome)

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

	devHome := t.TempDir()
	t.Setenv("HOME", devHome)
	var joinStdout, joinStderr bytes.Buffer
	code = Run([]string{
		"team", "join",
		"--handle", "bob@example.com",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &joinStdout,
		Err:     &joinStderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("team join exit = %d, stderr:\n%s", code, joinStderr.String())
	}
	localWithPending, err := config.ReadProject(filepath.Join(repo, "propagate.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(localWithPending.PendingJoins) != 1 {
		t.Fatalf("pending joins = %d, want 1", len(localWithPending.PendingJoins))
	}
	pending := localWithPending.PendingJoins[0]

	cloudProject := localWithPending
	cloudProject.CloudRevision = "rev_00002"
	cloudProject.SyncStatus = "synced"
	cloudProject.PendingJoins = nil
	cloudProject.Members = append(cloudProject.Members, config.Member{
		Handle:              pending.Handle,
		PublicKeySHA:        pending.PublicKeySHA,
		SigningPublicKey:    pending.SigningPublicKey,
		EncryptionPublicKey: pending.EncryptionPublicKey,
		Role:                pending.RequestedRole,
	})
	cloudSnapshot, err := config.SnapshotJSON(cloudProject)
	if err != nil {
		t.Fatal(err)
	}
	cloudHash, err := config.ConfigHash(cloudProject)
	if err != nil {
		t.Fatal(err)
	}

	var handlerErr error
	previousClient := configPushHTTPClient
	t.Cleanup(func() { configPushHTTPClient = previousClient })
	configPushHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get(apiclient.HeaderPublicKeySHA) == "" || r.Header.Get(apiclient.HeaderSignature) == "" {
			handlerErr = fmt.Errorf("request missing signing headers")
			return nil, handlerErr
		}
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/config") {
			handlerErr = fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			return nil, handlerErr
		}
		return testResponse(t, http.StatusOK, map[string]any{
			"config_revision": "rev_00002",
			"config_hash":     cloudHash,
			"config_snapshot": json.RawMessage(cloudSnapshot),
			"server_time":     time.Now().UTC().Format(time.RFC3339),
		}), nil
	}), Timeout: 2 * time.Second}

	t.Setenv("HOME", adminHome)
	var blockedStdout, blockedStderr bytes.Buffer
	code = Run([]string{
		"config", "pull",
		"--api-url", "http://propagate.test",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &blockedStdout,
		Err:     &blockedStderr,
		WorkDir: repo,
	})
	if code != ExitConfirmationRequired {
		t.Fatalf("config pull without --yes exit = %d, want %d; stderr:\n%s", code, ExitConfirmationRequired, blockedStderr.String())
	}

	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"config", "pull",
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
		t.Fatalf("config pull exit = %d, stderr:\n%s", code, stderr.String())
	}
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	pulled, err := config.ReadProject(filepath.Join(repo, "propagate.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if pulled.CloudRevision != "rev_00002" {
		t.Fatalf("cloud revision = %s", pulled.CloudRevision)
	}
	if len(pulled.PendingJoins) != 0 {
		t.Fatalf("pending joins remain after pull: %+v", pulled.PendingJoins)
	}
	if findMember(pulled.Members, pending.PublicKeySHA) == nil {
		t.Fatalf("cloud member was not pulled into propagate.yaml:\n%s", readConfig(t, repo))
	}
	if !strings.Contains(stdout.String(), "Would overwrite local changes: true") {
		t.Fatalf("output missing overwrite summary:\n%s", stdout.String())
	}
}

func TestConfigStatusReportsLocalAhead(t *testing.T) {
	repo := initGitRepo(t)
	adminHome := t.TempDir()
	t.Setenv("HOME", adminHome)

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

	devHome := t.TempDir()
	t.Setenv("HOME", devHome)
	var joinStdout, joinStderr bytes.Buffer
	code = Run([]string{
		"team", "join",
		"--handle", "bob@example.com",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &joinStdout,
		Err:     &joinStderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("team join exit = %d, stderr:\n%s", code, joinStderr.String())
	}

	var handlerErr error
	previousClient := configPushHTTPClient
	t.Cleanup(func() { configPushHTTPClient = previousClient })
	configPushHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get(apiclient.HeaderPublicKeySHA) == "" || r.Header.Get(apiclient.HeaderSignature) == "" {
			handlerErr = fmt.Errorf("request missing signing headers")
			return nil, handlerErr
		}
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/config/status") {
			handlerErr = fmt.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			return nil, handlerErr
		}
		if got := r.URL.Query().Get("local_revision"); got != "rev_00001" {
			handlerErr = fmt.Errorf("local_revision = %q", got)
			return nil, handlerErr
		}
		if got := r.URL.Query().Get("local_config_hash"); got == "" {
			handlerErr = fmt.Errorf("local_config_hash was empty")
			return nil, handlerErr
		}
		return testResponse(t, http.StatusOK, map[string]any{
			"local_revision":     "rev_00001",
			"cloud_revision":     "rev_00001",
			"local_config_hash":  r.URL.Query().Get("local_config_hash"),
			"cloud_config_hash":  "sha256:cloud",
			"state":              "local_ahead",
			"recommended_action": "push",
			"safe_summary":       map[string]any{"members_count": float64(1), "scopes_count": float64(1)},
		}), nil
	}), Timeout: 2 * time.Second}

	t.Setenv("HOME", adminHome)
	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"config", "status",
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
		t.Fatalf("config status exit = %d, stderr:\n%s", code, stderr.String())
	}
	if handlerErr != nil {
		t.Fatal(handlerErr)
	}
	var result ConfigStatusResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("parse status JSON: %v\n%s", err, stdout.String())
	}
	if result.State != "local_ahead" || result.RecommendedAction != "push" {
		t.Fatalf("unexpected status result: %+v", result)
	}
	if !strings.Contains(strings.Join(result.LocalOnlyChanges, "\n"), "pending joins: bob@example.com") {
		t.Fatalf("local-only summary missing pending join: %+v", result.LocalOnlyChanges)
	}
	if result.CloudConfigHash != "sha256:cloud" {
		t.Fatalf("cloud hash = %s", result.CloudConfigHash)
	}
}

func TestConfigStatusShowsLocalFactsWhenCloudURLMissing(t *testing.T) {
	repo := initGitRepo(t)
	adminHome := t.TempDir()
	t.Setenv("HOME", adminHome)

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

	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"config", "status",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitCloudUnavailable {
		t.Fatalf("config status exit = %d, want %d; stderr:\n%s", code, ExitCloudUnavailable, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Local revision: rev_00001") {
		t.Fatalf("stdout missing local revision:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Local config hash: sha256:") {
		t.Fatalf("stdout missing local hash:\n%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Propagate API URL is required for config status") {
		t.Fatalf("stderr missing cloud URL error:\n%s", stderr.String())
	}
}

func setConfigRevision(t *testing.T, repo string, revision string) {
	t.Helper()
	path := filepath.Join(repo, "propagate.yaml")
	project, err := config.ReadProject(path)
	if err != nil {
		t.Fatal(err)
	}
	project.CloudRevision = revision
	project.SyncStatus = "synced"
	rendered, err := config.RenderParsed(project)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func testResponse(t *testing.T, status int, data any) *http.Response {
	t.Helper()
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(map[string]any{
		"ok":      status >= 200 && status < 300,
		"request": "req_test",
		"data":    data,
	}); err != nil {
		t.Fatal(err)
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body.Bytes())),
	}
}

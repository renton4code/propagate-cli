package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestTeamJoinSubmitsToServer(t *testing.T) {
	var mu sync.Mutex
	var received []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/join/requests") && r.Method == http.MethodPost {
			body := make([]byte, r.ContentLength)
			r.Body.Read(body)
			mu.Lock()
			received = body
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "request_id": "req_test", "data": map[string]any{"status": "pending"}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/join/invites") {
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "request_id": "req_test", "data": map[string]any{"invites": []any{}}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	unsetEnvForTest(t, "PROPAGATE_API_URL")
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

	t.Setenv("PROPAGATE_API_URL", srv.URL)
	devHome := t.TempDir()
	t.Setenv("HOME", devHome)
	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"team", "join",
		"--handle", "bob@example.com",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("team join exit = %d, stderr:\n%s", code, stderr.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if received == nil {
		t.Fatal("server did not receive join request")
	}
	var body map[string]any
	if err := json.Unmarshal(received, &body); err != nil {
		t.Fatalf("invalid request body: %s", received)
	}
	joiner, ok := body["joiner"].(map[string]any)
	if !ok {
		t.Fatal("request missing joiner field")
	}
	if joiner["handle"] != "bob@example.com" {
		t.Fatalf("joiner handle = %v, want bob@example.com", joiner["handle"])
	}
	if !strings.Contains(stdout.String(), "Join request submitted") {
		t.Fatalf("expected submission output, got:\n%s", stdout.String())
	}
}

func TestTeamJoinWithInitRunsExistingProjectInit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/join/requests") && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "request_id": "req_test", "data": map[string]any{"status": "pending"}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/join/invites") {
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "request_id": "req_test", "data": map[string]any{"invites": []any{}}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	unsetEnvForTest(t, "PROPAGATE_API_URL")
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

	t.Setenv("PROPAGATE_API_URL", srv.URL)
	devHome := t.TempDir()
	t.Setenv("HOME", devHome)
	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"team", "join",
		"--init",
		"--handle", "bob@example.com",
		"--scope", "dev=read",
		"--agent-guidance",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("team join --init exit = %d, stderr:\n%s", code, stderr.String())
	}

	if _, err := os.Stat(filepath.Join(devHome, ".propagate", "identity")); err != nil {
		t.Fatalf("team join --init did not create identity: %v", err)
	}
	agentGuidance, err := os.ReadFile(filepath.Join(repo, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(agentGuidance), "Template: propagate-agent-guidance-v1") {
		t.Fatalf("AGENTS.md missing Propagate guidance:\n%s", agentGuidance)
	}
	output := stdout.String()
	for _, want := range []string{
		"Init completed before join.",
		"Agent guidance: created.",
		"Join request submitted",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("team join --init output missing %q:\n%s", want, output)
		}
	}
}

func TestTeamJoinGuidanceFlagsRequireInit(t *testing.T) {
	repo := initGitRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"team", "join",
		"--handle", "bob@example.com",
		"--agent-guidance",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitUsageError {
		t.Fatalf("team join exit = %d, want %d; stderr:\n%s", code, ExitUsageError, stderr.String())
	}
	if !strings.Contains(stderr.String(), "require --init") {
		t.Fatalf("expected --init requirement error, got:\n%s", stderr.String())
	}
}

func TestTeamRoleFlagIsRemoved(t *testing.T) {
	unsetEnvForTest(t, "PROPAGATE_API_URL")
	workDir := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"team", "join",
		"--role", "developers",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: workDir,
	})
	if code != ExitUsageError {
		t.Fatalf("team join --role exit = %d, want %d; stderr:\n%s", code, ExitUsageError, stderr.String())
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("expected removed --role flag error, got:\n%s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = Run([]string{
		"team", "invite",
		"--label", "Bob",
		"--role", "developers",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: workDir,
	})
	if code != ExitUsageError {
		t.Fatalf("team invite --role exit = %d, want %d; stderr:\n%s", code, ExitUsageError, stderr.String())
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("expected removed invite --role flag error, got:\n%s", stderr.String())
	}
}

func TestTeamJoinRejectsDuplicateRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/join/requests") && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(map[string]any{"ok": false, "request_id": "req_test", "error": map[string]any{"code": "duplicate_join_request", "message": "a join request already exists for this identity"}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/join/invites") {
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "request_id": "req_test", "data": map[string]any{"invites": []any{}}})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	unsetEnvForTest(t, "PROPAGATE_API_URL")
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

	t.Setenv("PROPAGATE_API_URL", srv.URL)
	devHome := t.TempDir()
	t.Setenv("HOME", devHome)
	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"team", "join",
		"--handle", "bob@example.com",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitConflict {
		t.Fatalf("team join exit = %d, want %d; stderr:\n%s", code, ExitConflict, stderr.String())
	}
	if !strings.Contains(stderr.String(), "already has a pending join request") {
		t.Fatalf("expected duplicate pending error, got:\n%s", stderr.String())
	}
}

func TestTeamJoinRejectsActiveMember(t *testing.T) {
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

	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"team", "join",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitValidationError {
		t.Fatalf("team join exit = %d, want %d; stderr:\n%s", code, ExitValidationError, stderr.String())
	}
	if !strings.Contains(stderr.String(), "already an active team member") {
		t.Fatalf("expected active member error, got:\n%s", stderr.String())
	}
}

func TestTeamJoinDryRunDoesNotWriteFiles(t *testing.T) {
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
	before := readConfig(t, repo)

	devHome := t.TempDir()
	t.Setenv("HOME", devHome)
	var stdout, stderr bytes.Buffer
	code = Run([]string{
		"team", "join",
		"--handle", "bob@example.com",
		"--dry-run",
		"--non-interactive",
	}, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: repo,
	})
	if code != ExitSuccess {
		t.Fatalf("team join --dry-run exit = %d, stderr:\n%s", code, stderr.String())
	}
	if after := readConfig(t, repo); after != before {
		t.Fatalf("dry run modified propagate.yaml:\n%s", after)
	}
	if _, err := os.Stat(filepath.Join(devHome, ".propagate", "identity")); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote identity file, stat error: %v", err)
	}
	if !strings.Contains(stdout.String(), "Would submit join request") {
		t.Fatalf("expected dry-run output, got:\n%s", stdout.String())
	}
}


func readConfig(t *testing.T, repo string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, "propagate.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

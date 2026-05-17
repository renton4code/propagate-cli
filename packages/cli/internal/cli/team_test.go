package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTeamJoinAddsPendingRequest(t *testing.T) {
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

	configText := readConfig(t, repo)
	for _, want := range []string{
		`handle: "bob@example.com"`,
		`public_key_sha: "sha256:`,
		`signing_public_key: "ssh-ed25519 `,
		`encryption_public_key: "x25519:`,
		`requested_scopes:`,
		`dev: read`,
	} {
		if !strings.Contains(configText, want) {
			t.Fatalf("propagate.yaml missing %q:\n%s", want, configText)
		}
	}
	if strings.Contains(configText, "signing_private_key") || strings.Contains(configText, "encryption_private_key") {
		t.Fatalf("propagate.yaml contains private key material:\n%s", configText)
	}
	if !strings.Contains(stdout.String(), "You do not have secret access yet.") {
		t.Fatalf("join output did not explain pending access:\n%s", stdout.String())
	}
}

func TestTeamJoinWithInitRunsExistingProjectInit(t *testing.T) {
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

	configText := readConfig(t, repo)
	for _, want := range []string{
		`handle: "bob@example.com"`,
		`requested_scopes:`,
		`dev: read`,
	} {
		if !strings.Contains(configText, want) {
			t.Fatalf("propagate.yaml missing %q:\n%s", want, configText)
		}
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
		"Join request added to propagate.yaml.",
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

func TestTeamJoinRejectsDuplicatePendingRequest(t *testing.T) {
	repo := initRepoAndJoinOnce(t)

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"team", "join",
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
	if !strings.Contains(stdout.String(), "Would add join request") {
		t.Fatalf("expected dry-run output, got:\n%s", stdout.String())
	}
}

func initRepoAndJoinOnce(t *testing.T) string {
	t.Helper()
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
	return repo
}

func readConfig(t *testing.T, repo string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repo, "propagate.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

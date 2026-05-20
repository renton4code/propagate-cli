//go:build e2e

package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestSequentialCLIAPIWithSupabaseDB(t *testing.T) {
	h := newE2EHarness(t)

	const (
		databaseURL     = "postgres://app:secret@example.invalid/service"
		initialAPIToken = "setup-token-secret"
		rotatedToken    = "rotated-secret"
		removedSecret   = "remove-me-secret"
		pushedAPIToken  = "api-token-from-env-push"
		pushedOnly      = "from-env-push"
	)
	plaintextValues := []string{databaseURL, initialAPIToken, rotatedToken, removedSecret, pushedAPIToken, pushedOnly}

	admin := h.newActor("alice@example.com")
	bob := h.newActor("bob@example.com")
	eve := h.newActor("eve@example.com")
	charlie := h.newActor("charlie@example.com")

	repo := h.newRepo()
	writeFile(t, filepath.Join(repo, ".env"), "DATABASE_URL="+databaseURL+"\nAPI_TOKEN="+initialAPIToken+"\n", 0o600)

	var staleRepo string

	t.Run("init uploads encrypted setup and metadata only", func(t *testing.T) {
		result := h.run(repo, admin, "", "init",
			"--json",
			"--handle", admin.Handle,
			"--team-name", "Acme API",
			"--yes",
			"--non-interactive",
			"--skip-agent-guidance",
		)
		requireNoPlaintext(t, result.Combined(), plaintextValues...)

		var out initResult
		decodeJSON(t, result.Stdout, &out)
		if out.BackendStatus != "created" || out.VariablesUploadedCount != 2 {
			t.Fatalf("unexpected init result: %+v\nstdout:\n%s", out, result.Stdout)
		}

		configText := readFile(t, filepath.Join(repo, "propagate.yaml"))
		requireContains(t, configText, `cloud_revision: "rev_00001"`)
		requireContains(t, configText, `sync_status: "synced"`)
		requireContains(t, configText, `variables:`)
		requireContains(t, configText, `name: "DATABASE_URL"`)
		requireContains(t, configText, `digest: "hmac-sha-256:v1:`)
		requireNoPlaintext(t, configText, plaintextValues...)

		staleRepo = h.newRepo()
		copyFile(t, filepath.Join(repo, "propagate.yaml"), filepath.Join(staleRepo, "propagate.yaml"), 0o600)
	})

	t.Run("status commands read synced config without leaking values", func(t *testing.T) {
		result := h.run(repo, admin, "", "config", "status", "--json")
		requireNoPlaintext(t, result.Combined(), plaintextValues...)
		status := decodeCLIJSON[configStatusResult](t, result.Stdout)
		if status.State != "equal" || status.RecommendedAction != "none" {
			t.Fatalf("unexpected config status: %+v\nstdout:\n%s", status, result.Stdout)
		}

		envStatus := h.envStatus(repo, admin, plaintextValues...)
		if envStatus.VariablesCount != 2 {
			t.Fatalf("env status variables_count = %d, want 2", envStatus.VariablesCount)
		}
		requireVariableNames(t, envStatus.Variables, "DATABASE_URL", "API_TOKEN")
	})

	t.Run("env set and env push cover encrypted add change remove", func(t *testing.T) {
		result := h.run(repo, admin, rotatedToken+"\n", "env", "set", "ROTATED_TOKEN", "--scope", "dev", "--yes", "--json")
		requireNoPlaintext(t, result.Combined(), plaintextValues...)

		result = h.run(repo, admin, removedSecret+"\n", "env", "set", "REMOVE_ME", "--scope", "dev", "--yes", "--json")
		requireNoPlaintext(t, result.Combined(), plaintextValues...)

		envStatus := h.envStatus(repo, admin, plaintextValues...)
		if envStatus.VariablesCount != 4 {
			t.Fatalf("env status variables_count = %d, want 4", envStatus.VariablesCount)
		}
		requireVariableNames(t, envStatus.Variables, "DATABASE_URL", "API_TOKEN", "ROTATED_TOKEN", "REMOVE_ME")

		writeFile(t, filepath.Join(repo, ".env"), strings.Join([]string{
			"DATABASE_URL=" + databaseURL,
			"API_TOKEN=" + pushedAPIToken,
			"ROTATED_TOKEN=" + rotatedToken,
			"PUSHED_ONLY=" + pushedOnly,
			"",
		}, "\n"), 0o600)

		result = h.run(repo, admin, "", "env", "push", "--scope", "dev", "--yes", "--non-interactive", "--json")
		requireNoPlaintext(t, result.Combined(), plaintextValues...)

		var push envPushResult
		decodeJSON(t, result.Stdout, &push)
		if push.BackendStatus != "pushed" ||
			push.VariablesAddedCount != 1 ||
			push.VariablesChangedCount != 1 ||
			push.VariablesRemovedCount != 1 ||
			push.CreatedVersionsCount != 2 ||
			push.RemovedVariablesCount != 1 {
			t.Fatalf("unexpected env push result: %+v\nstdout:\n%s", push, result.Stdout)
		}

		envStatus = h.envStatus(repo, admin, plaintextValues...)
		if envStatus.VariablesCount != 4 {
			t.Fatalf("env status variables_count after push = %d, want 4", envStatus.VariablesCount)
		}
		requireVariableNames(t, envStatus.Variables, "DATABASE_URL", "API_TOKEN", "ROTATED_TOKEN", "PUSHED_ONLY")
		requireNoVariable(t, envStatus.Variables, "REMOVE_ME")
	})

	t.Run("process injection passes cloud values to child without writing env file", func(t *testing.T) {
		writeFile(t, filepath.Join(repo, ".env"), "LOCAL_ONLY=yes\n", 0o600)
		t.Setenv("PROPAGATE_EXPECT_DATABASE_URL", databaseURL)
		t.Setenv("PROPAGATE_EXPECT_API_TOKEN", pushedAPIToken)
		t.Setenv("PROPAGATE_EXPECT_ROTATED_TOKEN", rotatedToken)
		t.Setenv("PROPAGATE_EXPECT_PUSHED_ONLY", pushedOnly)
		script := strings.Join([]string{
			`test "$DATABASE_URL" = "$PROPAGATE_EXPECT_DATABASE_URL" || exit 21`,
			`test "$API_TOKEN" = "$PROPAGATE_EXPECT_API_TOKEN" || exit 22`,
			`test "$ROTATED_TOKEN" = "$PROPAGATE_EXPECT_ROTATED_TOKEN" || exit 23`,
			`test "$PUSHED_ONLY" = "$PROPAGATE_EXPECT_PUSHED_ONLY" || exit 24`,
			`printf injected-ok`,
		}, "\n")

		result := h.run(repo, admin, "", "run", "--scope", "dev", "--", "sh", "-c", script)
		requireNoPlaintext(t, result.Combined(), plaintextValues...)
		if strings.TrimSpace(result.Stdout) != "injected-ok" {
			t.Fatalf("unexpected child stdout:\n%s\nstderr:\n%s", result.Stdout, result.Stderr)
		}
		if got := readFile(t, filepath.Join(repo, ".env")); got != "LOCAL_ONLY=yes\n" {
			t.Fatalf("process injection changed .env:\n%s", got)
		}
	})

	t.Run("env pull preserves unrelated local variables", func(t *testing.T) {
		writeFile(t, filepath.Join(repo, ".env"), "LOCAL_ONLY=yes\n", 0o600)
		result := h.run(repo, admin, "", "env", "pull", "--json", "--yes", "--non-interactive")
		requireNoPlaintext(t, result.Combined(), plaintextValues...)

		envText := readFile(t, filepath.Join(repo, ".env"))
		requireContainsAll(t, envText,
			"DATABASE_URL="+databaseURL,
			"API_TOKEN="+pushedAPIToken,
			"ROTATED_TOKEN="+rotatedToken,
			"PUSHED_ONLY="+pushedOnly,
			"LOCAL_ONLY=yes",
		)
		requireNotContains(t, envText, "REMOVE_ME="+removedSecret)
	})

	t.Run("pending join cannot pull before approval", func(t *testing.T) {
		bob = h.join(repo, bob, "dev=read")

		status := h.teamStatus(repo, admin)
		if status.PendingJoinsCount != 1 || !pendingJoinExists(status.PendingJoins, bob.PublicKeySHA) {
			t.Fatalf("team status did not expose Bob as pending:\n%+v", status)
		}

		writeFile(t, filepath.Join(repo, ".env"), "", 0o600)
		result := h.runExpectFailure(repo, bob, "", "env", "pull", "--json", "--yes", "--non-interactive")
		requireNoPlaintext(t, result.Combined(), plaintextValues...)
		errorJSON := decodeCLIJSON[commandErrorResult](t, result.Stderr)
		if result.ExitCode != exitPermissionDenied || errorJSON.Error.Code != "permission_denied" {
			t.Fatalf("env pull before approval failed with %+v and exit %d, want permission_denied/%d", errorJSON, result.ExitCode, exitPermissionDenied)
		}
		envText := readFile(t, filepath.Join(repo, ".env"))
		requireNotContains(t, envText, "DATABASE_URL="+databaseURL)
		requireNotContains(t, envText, "API_TOKEN="+pushedAPIToken)
	})

	t.Run("config push applies approve decline skip decisions", func(t *testing.T) {
		eve = h.join(repo, eve, "dev=read")
		charlie = h.join(repo, charlie, "dev=read")

		result := h.run(repo, admin, "", "config", "push",
			"--json",
			"--approve-join", bob.PublicKeySHA,
			"--decline-join", eve.PublicKeySHA,
			"--skip-join", charlie.PublicKeySHA,
			"--yes",
			"--non-interactive",
		)
		requireNoPlaintext(t, result.Combined(), plaintextValues...)

		var push configPushResult
		decodeJSON(t, result.Stdout, &push)
		if push.NewRevision != "rev_00005" ||
			push.EnvelopesUploadedCount != 1 ||
			!decisionExists(push.ApprovedJoins, bob.PublicKeySHA) ||
			!decisionExists(push.DeclinedJoins, eve.PublicKeySHA) ||
			!decisionExists(push.SkippedJoins, charlie.PublicKeySHA) {
			t.Fatalf("unexpected config push decisions: %+v\nstdout:\n%s", push, result.Stdout)
		}

		configText := readFile(t, filepath.Join(repo, "propagate.yaml"))
		requireContains(t, configText, `cloud_revision: "rev_00005"`)
		requireContains(t, configText, bob.PublicKeySHA)
		requireNotContains(t, configText, charlie.PublicKeySHA)
		requireContains(t, configText, `name: "PUSHED_ONLY"`)
		requireNotContains(t, configText, `name: "REMOVE_ME"`)
		requireNotContains(t, configText, eve.PublicKeySHA)

		status := h.teamStatus(repo, admin)
		if !memberInRole(status.Members, "members", bob.PublicKeySHA) {
			t.Fatalf("team status did not show approved member:\n%+v", status)
		}
		if status.PendingJoinsCount != 1 || !pendingJoinExists(status.PendingJoins, charlie.PublicKeySHA) {
			t.Fatalf("team status did not retain skipped join locally:\n%+v", status)
		}
	})

	t.Run("config status and pull recover stale checkout", func(t *testing.T) {
		result := h.run(staleRepo, admin, "", "config", "status", "--json")
		requireNoPlaintext(t, result.Combined(), plaintextValues...)
		status := decodeCLIJSON[configStatusResult](t, result.Stdout)
		if status.State != "cloud_ahead" || status.RecommendedAction != "pull" {
			t.Fatalf("stale repo status = %+v, want cloud_ahead/pull\nstdout:\n%s", status, result.Stdout)
		}

		result = h.run(staleRepo, admin, "", "config", "pull", "--json", "--yes", "--non-interactive")
		requireNoPlaintext(t, result.Combined(), plaintextValues...)

		var pull configPullResult
		decodeJSON(t, result.Stdout, &pull)
		if !pull.Updated || pull.OldRevision != "rev_00001" || pull.NewRevision != "rev_00005" {
			t.Fatalf("unexpected config pull result: %+v\nstdout:\n%s", pull, result.Stdout)
		}

		configText := readFile(t, filepath.Join(staleRepo, "propagate.yaml"))
		requireContains(t, configText, `cloud_revision: "rev_00005"`)
		requireContains(t, configText, bob.PublicKeySHA)
		requireNotContains(t, configText, eve.PublicKeySHA)
		requireNotContains(t, configText, charlie.PublicKeySHA)
	})

	t.Run("approved developer can pull and team status records activity", func(t *testing.T) {
		writeFile(t, filepath.Join(repo, ".env"), "", 0o600)
		result := h.run(repo, bob, "", "env", "pull", "--json", "--yes", "--non-interactive")
		requireNoPlaintext(t, result.Combined(), plaintextValues...)

		envText := readFile(t, filepath.Join(repo, ".env"))
		requireContainsAll(t, envText,
			"DATABASE_URL="+databaseURL,
			"API_TOKEN="+pushedAPIToken,
			"ROTATED_TOKEN="+rotatedToken,
			"PUSHED_ONLY="+pushedOnly,
		)
		requireNotContains(t, envText, "REMOVE_ME="+removedSecret)

		status := h.teamStatus(repo, admin)
		if !memberInRole(status.Members, "members", bob.PublicKeySHA) {
			t.Fatalf("team status did not retain approved member:\n%+v", status)
		}
		if !pullActivityExists(status.LastPulls, bob.PublicKeySHA, "dev") {
			t.Fatalf("team status did not record Bob's dev pull:\n%+v", status)
		}
		if neverPulledExists(status.NeverPulled, bob.PublicKeySHA) {
			t.Fatalf("team status still reported Bob as never pulled:\n%+v", status)
		}
	})

	t.Run("request join still works while unused invite codes exist", func(t *testing.T) {
		result := h.run(repo, admin, "", "team", "invite", "--json", "--label", "e2e-request-with-invite", "--scope", "dev=read")
		var inv teamInviteCreateJSON
		decodeJSON(t, result.Stdout, &inv)
		if !inv.OK || inv.InviteID == "" {
			t.Fatalf("unexpected team invite result: %+v\nstdout:\n%s", inv, result.Stdout)
		}

		earl := h.newActor("earl@example.com")
		earl = h.join(repo, earl, "dev=read")

		status := h.teamStatus(repo, admin)
		if !pendingJoinExists(status.PendingJoins, earl.PublicKeySHA) {
			t.Fatalf("team status missing Earl after request join:\n%+v", status)
		}
		_ = readFile(t, filepath.Join(repo, "propagate.yaml"))
	})

	t.Run("pre-approved invite join grants immediate env pull", func(t *testing.T) {
		result := h.run(repo, admin, "", "team", "invite", "--json", "--label", "e2e-invite", "--scope", "dev=read")
		var inv teamInviteCreateJSON
		decodeJSON(t, result.Stdout, &inv)
		if !inv.OK || inv.InviteID == "" || inv.PIN == "" {
			t.Fatalf("unexpected team invite result: %+v\nstdout:\n%s", inv, result.Stdout)
		}

		dana := h.newActor("dana@example.com")
		dana = h.joinByInvite(repo, dana, inv.InviteID, inv.PIN, "dev=read")

		configText := readFile(t, filepath.Join(repo, "propagate.yaml"))
		requireContains(t, configText, dana.PublicKeySHA)

		writeFile(t, filepath.Join(repo, ".env"), "", 0o600)
		result = h.run(repo, dana, "", "env", "pull", "--json", "--yes", "--non-interactive")
		requireNoPlaintext(t, result.Combined(), plaintextValues...)

		envText := readFile(t, filepath.Join(repo, ".env"))
		requireContainsAll(t, envText,
			"DATABASE_URL="+databaseURL,
			"API_TOKEN="+pushedAPIToken,
			"ROTATED_TOKEN="+rotatedToken,
			"PUSHED_ONLY="+pushedOnly,
		)
	})

	t.Run("three wrong PIN attempts invalidate invite", func(t *testing.T) {
		result := h.run(repo, admin, "", "team", "invite", "--json", "--label", "e2e-lockout", "--scope", "dev=read")
		var inv teamInviteCreateJSON
		decodeJSON(t, result.Stdout, &inv)
		if !inv.OK || inv.InviteID == "" || inv.PIN == "" {
			t.Fatalf("unexpected team invite result: %+v\nstdout:\n%s", inv, result.Stdout)
		}

		for i := 0; i < 3; i++ {
			attacker := h.newActor(fmt.Sprintf("badpin%d@example.com", i))
			badRepo := h.newRepo()
			copyFile(t, filepath.Join(repo, "propagate.yaml"), filepath.Join(badRepo, "propagate.yaml"), 0o600)
			res := h.runExpectFailure(badRepo, attacker, "", "team", "join", "--json", "--handle", attacker.Handle, "--non-interactive",
				"--join-mode", "invite", "--invite-id", inv.InviteID, "--pin", "0000A", "--scope", "dev=read")
			errJSON := decodeCLIJSON[commandErrorResult](t, res.Stderr)
			if i < 2 {
				if errJSON.Error.Code != "invite_pin_invalid" {
					t.Fatalf("attempt %d want invite_pin_invalid, got %+v\nstderr:\n%s", i+1, errJSON, res.Stderr)
				}
			} else {
				if errJSON.Error.Code != "invite_locked" {
					t.Fatalf("attempt %d want invite_locked, got %+v\nstderr:\n%s", i+1, errJSON, res.Stderr)
				}
			}
		}

		final := h.newActor("final@example.com")
		finalRepo := h.newRepo()
		copyFile(t, filepath.Join(repo, "propagate.yaml"), filepath.Join(finalRepo, "propagate.yaml"), 0o600)
		res := h.runExpectFailure(finalRepo, final, "", "team", "join", "--json", "--handle", final.Handle, "--non-interactive",
			"--join-mode", "invite", "--invite-id", inv.InviteID, "--pin", inv.PIN, "--scope", "dev=read")
		errJSON := decodeCLIJSON[commandErrorResult](t, res.Stderr)
		if errJSON.Error.Code != "invite_not_active" {
			t.Fatalf("correct pin after lockout want invite_not_active, got %+v\nstderr:\n%s", errJSON, res.Stderr)
		}
	})
}

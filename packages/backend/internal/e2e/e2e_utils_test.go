//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"propagate/backend/internal/api"
	"propagate/backend/internal/storage"

	_ "github.com/lib/pq"
)

const defaultDBImage = "supabase/postgres:15.14.1.107"
const exitPermissionDenied = 4

var migrationNamePattern = regexp.MustCompile(`^\d{4}_[a-z0-9_]+\.sql$`)

type e2eHarness struct {
	t       *testing.T
	ctx     context.Context
	root    string
	cliPath string
	apiURL  string
}

type testActor struct {
	Handle       string
	Home         string
	PublicKeySHA string
}

func newE2EHarness(t *testing.T) *e2eHarness {
	t.Helper()
	if os.Getenv("PROPAGATE_E2E") != "1" {
		t.Skip("set PROPAGATE_E2E=1 to run Docker-backed e2e tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	t.Cleanup(cancel)

	root := repoRoot(t)
	cliPath := buildCLI(t, ctx, root)
	db := startSupabasePostgres(t, ctx)
	t.Cleanup(func() {
		if err := db.DB.Close(); err != nil {
			t.Logf("close postgres connection: %v", err)
		}
	})
	applyMigrations(t, ctx, db.DB, filepath.Join(root, "packages", "backend", "migrations"))

	apiServer := httptest.NewServer(api.NewServer(storage.NewSQLStore(db.DB), api.Config{}))
	t.Cleanup(apiServer.Close)

	return &e2eHarness{
		t:       t,
		ctx:     ctx,
		root:    root,
		cliPath: cliPath,
		apiURL:  apiServer.URL,
	}
}

func (h *e2eHarness) newRepo() string {
	h.t.Helper()
	repo := h.t.TempDir()
	initGitRepo(h.t, h.ctx, repo)
	return repo
}

func (h *e2eHarness) newActor(handle string) testActor {
	h.t.Helper()
	return testActor{Handle: handle, Home: h.t.TempDir()}
}

func (h *e2eHarness) join(repo string, actor testActor, scopes ...string) testActor {
	h.t.Helper()
	args := []string{"team", "join", "--json", "--handle", actor.Handle, "--non-interactive"}
	for _, scope := range scopes {
		args = append(args, "--scope", scope)
	}
	result := h.run(repo, actor, "", args...)
	var out teamJoinResult
	decodeJSON(h.t, result.Stdout, &out)
	if out.Identity.PublicKeySHA == "" {
		h.t.Fatalf("team join did not return public key sha:\n%s", result.Stdout)
	}
	actor.PublicKeySHA = out.Identity.PublicKeySHA
	return actor
}

func (h *e2eHarness) envStatus(repo string, actor testActor, forbidden ...string) envStatusResult {
	h.t.Helper()
	result := h.run(repo, actor, "", "env", "status", "--json", "--scope", "dev")
	requireNoPlaintext(h.t, result.Combined(), forbidden...)
	return decodeCLIJSON[envStatusResult](h.t, result.Stdout)
}

func (h *e2eHarness) teamStatus(repo string, actor testActor) teamStatusResult {
	h.t.Helper()
	result := h.run(repo, actor, "", "team", "status", "--json")
	return decodeCLIJSON[teamStatusResult](h.t, result.Stdout)
}

func (h *e2eHarness) run(repo string, actor testActor, stdin string, args ...string) cliResult {
	h.t.Helper()
	result := h.runRaw(repo, actor, stdin, args...)
	if result.ExitCode != 0 {
		h.t.Fatalf("propagate %s failed with exit %d\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), result.ExitCode, result.Stdout, result.Stderr)
	}
	return result
}

func (h *e2eHarness) runExpectFailure(repo string, actor testActor, stdin string, args ...string) cliResult {
	h.t.Helper()
	result := h.runRaw(repo, actor, stdin, args...)
	if result.ExitCode == 0 {
		h.t.Fatalf("propagate %s succeeded unexpectedly\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), result.Stdout, result.Stderr)
	}
	return result
}

func (h *e2eHarness) runRaw(repo string, actor testActor, stdin string, args ...string) cliResult {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(h.ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, h.cliPath, args...)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(),
		"HOME="+actor.Home,
		"PROPAGATE_API_URL="+h.apiURL,
	)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	exitCode := 0
	if err := cmd.Run(); err != nil {
		exitCode = -1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	return cliResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}
}

type initResult struct {
	BackendStatus          string `json:"backend_status"`
	VariablesUploadedCount int    `json:"variables_uploaded_count"`
}

type configStatusResult struct {
	State             string `json:"state"`
	RecommendedAction string `json:"recommended_action"`
}

type variableSummary struct {
	Name string `json:"name"`
}

type envStatusResult struct {
	Variables      []variableSummary `json:"variables"`
	VariablesCount int               `json:"variables_count"`
}

type envPushResult struct {
	VariablesAddedCount   int    `json:"variables_added_count"`
	VariablesChangedCount int    `json:"variables_changed_count"`
	VariablesRemovedCount int    `json:"variables_removed_count"`
	CreatedVersionsCount  int    `json:"created_versions_count"`
	RemovedVariablesCount int    `json:"removed_variables_count"`
	BackendStatus         string `json:"backend_status"`
}

type joinDecision struct {
	PublicKeySHA string `json:"public_key_sha"`
	Decision     string `json:"decision"`
}

type configPushResult struct {
	NewRevision            string         `json:"new_revision"`
	ApprovedJoins          []joinDecision `json:"approved_joins"`
	DeclinedJoins          []joinDecision `json:"declined_joins"`
	SkippedJoins           []joinDecision `json:"skipped_joins"`
	EnvelopesUploadedCount int            `json:"envelopes_uploaded_count"`
}

type configPullResult struct {
	Updated     bool   `json:"updated"`
	OldRevision string `json:"old_revision"`
	NewRevision string `json:"new_revision"`
}

type teamJoinResult struct {
	Identity struct {
		PublicKeySHA string `json:"public_key_sha"`
	} `json:"identity"`
}

type teamStatusResult struct {
	Members map[string][]teamMember `json:"members"`

	PendingJoins      []teamPendingJoin `json:"pending_joins"`
	PendingJoinsCount int               `json:"pending_joins_count"`
	LastPulls         []teamPull        `json:"last_pulls"`
	NeverPulled       []teamMember      `json:"never_pulled"`
}

type teamMember struct {
	PublicKeySHA string `json:"public_key_sha"`
}

type teamPendingJoin struct {
	PublicKeySHA string `json:"public_key_sha"`
}

type teamPull struct {
	MemberPublicKeySHA string `json:"member_public_key_sha"`
	Scope              string `json:"scope"`
}

type commandErrorResult struct {
	OK     bool   `json:"ok"`
	Status string `json:"status"`
	Error  struct {
		ExitCode int    `json:"exit_code"`
		Code     string `json:"code"`
		Message  string `json:"message"`
	} `json:"error"`
}

type cliResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

func (r cliResult) Combined() string {
	return r.Stdout + r.Stderr
}

type testDB struct {
	DB        *sql.DB
	DSN       string
	Container string
}

func startSupabasePostgres(t *testing.T, ctx context.Context) testDB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("docker is required for e2e tests: %v", err)
	}
	if out, err := exec.CommandContext(ctx, "docker", "version").CombinedOutput(); err != nil {
		t.Skipf("docker daemon is not available: %v\n%s", err, out)
	}

	image := strings.TrimSpace(os.Getenv("PROPAGATE_E2E_DB_IMAGE"))
	if image == "" {
		image = defaultDBImage
	}
	name := fmt.Sprintf("propagate-e2e-%d-%d", os.Getpid(), time.Now().UnixNano())
	args := []string{
		"run", "--rm", "-d",
		"--name", name,
		"-e", "POSTGRES_PASSWORD=postgres",
		"-e", "POSTGRES_DB=postgres",
		"-p", "127.0.0.1::5432",
		image,
		"postgres", "-c", "config_file=/etc/postgresql/postgresql.conf",
	}
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Skipf("could not start Supabase Postgres image %q; ensure Docker can pull/run it: %v\n%s", image, err, out)
	}
	containerID := lastNonEmptyLine(string(out))
	t.Logf("started Supabase Postgres container %s (%s)", name, containerID)
	if os.Getenv("PROPAGATE_E2E_KEEP_DB") == "1" {
		t.Logf("leaving e2e database container running: %s", name)
	} else {
		t.Cleanup(func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if out, err := exec.CommandContext(cleanupCtx, "docker", "rm", "-f", name).CombinedOutput(); err != nil {
				t.Logf("cleanup docker rm -f %s failed: %v\n%s", name, err, out)
			}
		})
	}

	hostPort := dockerHostPort(t, ctx, name)
	dsn := fmt.Sprintf("postgres://postgres:postgres@%s/postgres?sslmode=disable", hostPort)
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("open postgres connection: %v", err)
	}
	waitForPostgres(t, ctx, db, name)
	return testDB{DB: db, DSN: dsn, Container: name}
}

func dockerHostPort(t *testing.T, ctx context.Context, container string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		out, err := exec.CommandContext(ctx, "docker", "port", container, "5432/tcp").CombinedOutput()
		last = string(out)
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(last), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				if host, port, err := net.SplitHostPort(line); err == nil {
					if host == "" || host == "::" || host == "0.0.0.0" {
						host = "127.0.0.1"
					}
					return net.JoinHostPort(host, port)
				}
				if idx := strings.LastIndex(line, ":"); idx > 0 {
					return net.JoinHostPort(line[:idx], line[idx+1:])
				}
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("could not discover mapped postgres port for %s; last docker port output: %s", container, last)
	return ""
}

func waitForPostgres(t *testing.T, ctx context.Context, db *sql.DB, container string) {
	t.Helper()
	deadline := time.Now().Add(120 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		pingCtx, cancel := context.WithTimeout(ctx, time.Second)
		err := db.PingContext(pingCtx)
		cancel()
		if err == nil {
			return
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	logs, _ := exec.CommandContext(ctx, "docker", "logs", "--tail", "120", container).CombinedOutput()
	t.Fatalf("postgres did not become ready: %v\ncontainer logs:\n%s", lastErr, logs)
}

func applyMigrations(t *testing.T, ctx context.Context, db *sql.DB, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read migrations dir %s: %v", dir, err)
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		if !migrationNamePattern.MatchString(entry.Name()) {
			t.Fatalf("migration %s does not match %s", entry.Name(), migrationNamePattern.String())
		}
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		t.Fatalf("no migrations found in %s", dir)
	}
	for _, path := range paths {
		sqlText, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read migration %s: %v", path, err)
		}
		if _, err := db.ExecContext(ctx, string(sqlText)); err != nil {
			t.Fatalf("apply migration %s: %v", filepath.Base(path), err)
		}
		t.Logf("applied migration %s", filepath.Base(path))
	}
}

func buildCLI(t *testing.T, ctx context.Context, root string) string {
	t.Helper()
	outName := "propagate"
	if runtime.GOOS == "windows" {
		outName += ".exe"
	}
	outPath := filepath.Join(t.TempDir(), outName)
	cmd := exec.CommandContext(ctx, "go", "build", "-o", outPath, "./packages/cli/cmd/propagate")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build propagate CLI: %v\n%s", err, out)
	}
	return outPath
}

func initGitRepo(t *testing.T, ctx context.Context, dir string) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "git", "init")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../../.."))
	if _, err := os.Stat(filepath.Join(root, "go.work")); err != nil {
		t.Fatalf("cannot determine repo root from %s: %v", file, err)
	}
	return root
}

func decodeCLIJSON[T any](t *testing.T, text string) T {
	t.Helper()
	var out T
	decodeJSON(t, text, &out)
	return out
}

func decodeJSON(t *testing.T, text string, out any) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(text))
	if err := dec.Decode(out); err != nil {
		t.Fatalf("decode JSON failed: %v\n%s", err, text)
	}
}

func copyFile(t *testing.T, src string, dst string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	writeFile(t, dst, string(data), mode)
}

func writeFile(t *testing.T, path string, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func requireContains(t *testing.T, text string, want string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Fatalf("expected text to contain %q:\n%s", want, text)
	}
}

func requireContainsAll(t *testing.T, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		requireContains(t, text, want)
	}
}

func requireNotContains(t *testing.T, text string, forbidden string) {
	t.Helper()
	if strings.Contains(text, forbidden) {
		t.Fatalf("expected text not to contain %q:\n%s", forbidden, text)
	}
}

func requireNoPlaintext(t *testing.T, text string, values ...string) {
	t.Helper()
	for _, value := range values {
		if value == "" {
			continue
		}
		if strings.Contains(text, value) {
			t.Fatalf("output leaked plaintext %q:\n%s", value, text)
		}
	}
}

func requireVariableNames(t *testing.T, vars []variableSummary, names ...string) {
	t.Helper()
	for _, name := range names {
		if !hasVariable(vars, name) {
			t.Fatalf("expected variable %s in %+v", name, vars)
		}
	}
}

func requireNoVariable(t *testing.T, vars []variableSummary, name string) {
	t.Helper()
	if hasVariable(vars, name) {
		t.Fatalf("expected variable %s to be absent from %+v", name, vars)
	}
}

func lastNonEmptyLine(text string) string {
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line
		}
	}
	return strings.TrimSpace(text)
}

func hasVariable(vars []variableSummary, name string) bool {
	for _, item := range vars {
		if item.Name == name {
			return true
		}
	}
	return false
}

func decisionExists(decisions []joinDecision, publicKeySHA string) bool {
	for _, decision := range decisions {
		if decision.PublicKeySHA == publicKeySHA {
			return true
		}
	}
	return false
}

func pendingJoinExists(joins []teamPendingJoin, publicKeySHA string) bool {
	for _, join := range joins {
		if join.PublicKeySHA == publicKeySHA {
			return true
		}
	}
	return false
}

func memberInRole(members map[string][]teamMember, role string, publicKeySHA string) bool {
	for _, member := range members[role] {
		if member.PublicKeySHA == publicKeySHA {
			return true
		}
	}
	return false
}

func pullActivityExists(pulls []teamPull, publicKeySHA string, scope string) bool {
	for _, pull := range pulls {
		if pull.MemberPublicKeySHA == publicKeySHA && pull.Scope == scope {
			return true
		}
	}
	return false
}

func neverPulledExists(members []teamMember, publicKeySHA string) bool {
	for _, member := range members {
		if member.PublicKeySHA == publicKeySHA {
			return true
		}
	}
	return false
}

package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"propagate/cli/internal/apiclient"
	"propagate/cli/internal/config"
	"propagate/cli/internal/envfile"
	"propagate/cli/internal/gitutil"
	"propagate/cli/internal/identity"
	"propagate/cli/internal/secretcrypto"
)

type configPushOptions struct {
	globalOptions
	DryRun       bool
	Yes          bool
	ApproveJoins multiFlag
	DeclineJoins multiFlag
	SkipJoins    multiFlag
}

var configPushHTTPClient *http.Client

type ConfigPushResult struct {
	OK                     bool                `json:"ok"`
	Command                string              `json:"command"`
	Status                 string              `json:"status"`
	DryRun                 bool                `json:"dry_run"`
	OperationID            string              `json:"operation_id,omitempty"`
	ProjectConfigPath      string              `json:"project_config_path"`
	TeamID                 string              `json:"team_id"`
	TeamName               string              `json:"team_name"`
	Identity               *identity.Summary   `json:"identity,omitempty"`
	OldRevision            string              `json:"old_revision,omitempty"`
	NewRevision            string              `json:"new_revision,omitempty"`
	LocalConfigHash        string              `json:"local_config_hash,omitempty"`
	CloudConfigHash        string              `json:"cloud_config_hash,omitempty"`
	EnvelopesUploadedCount int                 `json:"envelopes_uploaded_count"`
	ApprovedJoins          []JoinDecisionEntry `json:"approved_joins,omitempty"`
	DeclinedJoins          []JoinDecisionEntry `json:"declined_joins,omitempty"`
	SkippedJoins           []JoinDecisionEntry `json:"skipped_joins,omitempty"`
	ConfigModified         bool                `json:"config_modified"`
	BackendStatus          string              `json:"backend_status"`
	Warnings               []string            `json:"warnings,omitempty"`
	NextSteps              []string            `json:"next_steps,omitempty"`
}

type JoinDecisionEntry struct {
	PublicKeySHA string `json:"public_key_sha"`
	Decision     string `json:"decision"`
}


func runConfigCommand(args []string, global globalOptions, streams Streams) int {
	if len(args) == 0 {
		printConfigHelp(streams.Out)
		return ExitSuccess
	}
	switch args[0] {
	case "status":
		return runConfigStatusCommand(args[1:], global, streams)
	case "pull":
		return runConfigPullCommand(args[1:], global, streams)
	case "push":
		return runConfigPushCommand(args[1:], global, streams)
	case "edit":
		return runConfigEditCommand(args[1:], global, streams)
	case "help", "--help", "-h":
		printConfigHelp(streams.Out)
		return ExitSuccess
	default:
		err := commandError(ExitUsageError, "usage_error", fmt.Sprintf("Unknown config command %q", args[0]), nil, "Run `propagate config help` to see available config commands.")
		return renderError(streams.Err, global.JSON, global.NoColor, err)
	}
}

func runConfigPushCommand(args []string, global globalOptions, streams Streams) int {
	opts := configPushOptions{globalOptions: global}
	fs := flag.NewFlagSet("config push", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &opts.globalOptions)
	fs.BoolVar(&opts.DryRun, "dry-run", false, "validate and summarize without uploading or writing propagate.yaml")
	fs.BoolVar(&opts.Yes, "yes", false, "confirm the config push")
	fs.Var(&opts.ApproveJoins, "approve-join", "approve a pending join request (public_key_sha)")
	fs.Var(&opts.DeclineJoins, "decline-join", "decline a pending join request (public_key_sha)")
	fs.Var(&opts.SkipJoins, "skip-join", "skip a pending join request (public_key_sha)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printConfigPushHelp(streams.Out)
			return ExitSuccess
		}
		cmdErr := commandError(ExitUsageError, "usage_error", "Invalid config push flags", err, "Run `propagate config push --help` for usage.")
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if fs.NArg() != 0 {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate config push does not accept positional arguments", nil)
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}

	result, err := runConfigPush(opts, streams)
	if err != nil {
		return renderError(streams.Err, opts.JSON, opts.NoColor, err)
	}
	renderConfigPushResult(streams.Out, opts.JSON, opts.NoColor, result)
	return ExitSuccess
}

func runConfigPush(opts configPushOptions, streams Streams) (ConfigPushResult, error) {
	reader := bufio.NewReader(streams.In)
	result := ConfigPushResult{
		OK:            true,
		Command:       "config push",
		Status:        "success",
		DryRun:        opts.DryRun,
		BackendStatus: "not_contacted",
	}

	ident, err := identity.Load()
	if err != nil {
		return ConfigPushResult{}, commandError(ExitValidationError, "identity_missing", "Cannot load local Propagate identity for signed config push", err, "Run `propagate init` to create or repair the local identity.")
	}
	summary := ident.Summary()
	result.Identity = &summary

	worktree, err := gitutil.Discover(streams.WorkDir)
	if err != nil {
		return ConfigPushResult{}, commandError(ExitValidationError, "not_git_repo", "Cannot push config outside a Git worktree", err)
	}
	configPath, exists, err := config.ExistingPath(worktree.Root)
	if err != nil {
		return ConfigPushResult{}, commandError(ExitValidationError, "config_invalid", "Existing Propagate config needs attention", err, "Rename `propagate.yml` to `propagate.yaml` before running config push again.")
	}
	if !exists {
		return ConfigPushResult{}, commandError(ExitValidationError, "config_missing", "propagate.yaml is required before config push", nil, "Run `propagate init` or pull the repository config first.")
	}
	result.ProjectConfigPath = configPath

	project, err := config.ReadProject(configPath)
	if err != nil {
		return ConfigPushResult{}, commandError(ExitValidationError, "config_invalid", "Cannot read propagate.yaml", err)
	}
	result.TeamID = project.TeamID
	result.TeamName = project.TeamName
	result.OldRevision = project.CloudRevision

	actor := findMember(project.Members, summary.PublicKeySHA)
	if actor == nil || !config.MemberCanManage(*actor) {
		return ConfigPushResult{}, commandError(ExitPermissionDenied, "permission_denied", "Only members with management access can push Propagate config changes", nil, "Ask a Propagate manager to review the config diff and run `propagate config push`.")
	}

	localHash, err := config.ConfigHash(project)
	if err != nil {
		return ConfigPushResult{}, commandError(ExitValidationError, "config_invalid", "Cannot normalize propagate.yaml for config push", err)
	}
	result.LocalConfigHash = localHash

	apiURL := resolveAPIURL(opts.APIURL, streams.WorkDir)
	if apiURL == "" {
		return ConfigPushResult{}, commandError(ExitCloudUnavailable, "cloud_unavailable", "Propagate API URL is required for config push", nil, "Pass `--api-url` or set PROPAGATE_API_URL.")
	}
	client := apiclient.Client{BaseURL: apiURL, HTTPClient: configPushHTTPClient, CLIVersion: Version}
	status, err := client.ConfigStatus(context.Background(), ident, project.TeamID, project.CloudRevision, localHash)
	if err != nil {
		return ConfigPushResult{}, mapAPIError(err, "Cannot fetch current cloud config status")
	}
	result.BackendStatus = status.State
	result.CloudConfigHash = status.CloudConfigHash
	if status.CloudRevision != project.CloudRevision {
		return ConfigPushResult{}, commandError(
			ExitConflict,
			"revision_conflict",
			"Cloud config revision differs from local propagate.yaml",
			nil,
			"Run `propagate config pull`, review the diff, and retry config push.",
		)
	}
	if status.State == "cloud_ahead" || status.State == "conflict" {
		return ConfigPushResult{}, commandError(ExitConflict, "revision_conflict", "Local config is not based on the current cloud revision", nil, "Run `propagate config pull`, review the diff, and retry config push.")
	}

	hasJoinDecisions := len(opts.ApproveJoins) > 0 || len(opts.DeclineJoins) > 0 || len(opts.SkipJoins) > 0
	if status.State == "equal" && !hasJoinDecisions {
		result.Status = "no_change"
		result.BackendStatus = "equal"
		result.NextSteps = []string{"No config push is needed."}
		return result, nil
	}

	var targetEnvelopes []apiclient.ScopeKeyEnvelope
	var decisions apiclient.ConfigDecisions

	if len(opts.ApproveJoins) > 0 || len(opts.DeclineJoins) > 0 || len(opts.SkipJoins) > 0 {
		pendingData, pErr := client.ListPendingJoinRequests(context.Background(), ident, project.TeamID)
		if pErr != nil {
			return ConfigPushResult{}, mapAPIError(pErr, "Cannot list pending join requests")
		}
		pendingByKey := map[string]apiclient.JoinRequestRow{}
		for _, req := range pendingData.Requests {
			pendingByKey[req.PublicKeySHA] = req
		}
		for _, sha := range opts.ApproveJoins {
			joiner, found := pendingByKey[sha]
			if !found {
				return ConfigPushResult{}, commandError(ExitValidationError, "join_request_not_found", fmt.Sprintf("No pending join request found for %s", sha), nil)
			}
			project.Members = append(project.Members, config.Member{
				Handle:              joiner.Handle,
				PublicKeySHA:        joiner.PublicKeySHA,
				SigningPublicKey:    joiner.SigningPublicKey,
				EncryptionPublicKey: joiner.EncryptionPublicKey,
				Management:          joiner.RequestedManagement,
				Scopes:              joiner.RequestedScopes,
			})
			envelopes, bErr := buildEnvelopesForJoiner(context.Background(), client, ident, project, joiner)
			if bErr != nil {
				return ConfigPushResult{}, bErr
			}
			targetEnvelopes = append(targetEnvelopes, envelopes...)
			for scope, perm := range joiner.RequestedScopes {
				decisions.Approved = append(decisions.Approved, apiclient.ConfigDecision{
					Type:         "scope_access_change",
					PublicKeySHA: sha,
					Scope:        scope,
					Permission:   perm,
				})
			}
			result.ApprovedJoins = append(result.ApprovedJoins, JoinDecisionEntry{PublicKeySHA: sha, Decision: "approved"})
		}
		for _, sha := range opts.DeclineJoins {
			if _, found := pendingByKey[sha]; !found {
				return ConfigPushResult{}, commandError(ExitValidationError, "join_request_not_found", fmt.Sprintf("No pending join request found for %s", sha), nil)
			}
			decisions.Declined = append(decisions.Declined, apiclient.ConfigDecision{
				Type:         "join",
				PublicKeySHA: sha,
			})
			result.DeclinedJoins = append(result.DeclinedJoins, JoinDecisionEntry{PublicKeySHA: sha, Decision: "declined"})
		}
		for _, sha := range opts.SkipJoins {
			decisions.Skipped = append(decisions.Skipped, apiclient.ConfigDecision{
				Type:         "join",
				PublicKeySHA: sha,
			})
			result.SkippedJoins = append(result.SkippedJoins, JoinDecisionEntry{PublicKeySHA: sha, Decision: "skipped"})
		}
	}

	newScopeEnvelopes, err := buildNewScopeEnvelopesIfNeeded(context.Background(), client, ident, project, project, status)
	if err != nil {
		return ConfigPushResult{}, err
	}
	targetEnvelopes = append(targetEnvelopes, newScopeEnvelopes...)
	result.EnvelopesUploadedCount = len(targetEnvelopes)

	if opts.DryRun {
		result.Status = "dry_run"
		result.BackendStatus = "validated"
		result.ConfigModified = status.State == "local_ahead"
		result.NextSteps = []string{"Re-run without `--dry-run` and with `--yes` after reviewing the change summary."}
		return result, nil
	}

	if opts.NonInteractive && !opts.Yes {
		return ConfigPushResult{}, commandError(ExitConfirmationRequired, "confirmation_required", "Non-interactive config push requires --yes", nil, "Re-run with `--yes` after reviewing `propagate config push --dry-run`.")
	}
	if !opts.NonInteractive && !opts.Yes {
		ok, err := promptConfirm(reader, streams.In, streams.Out, "Push local config changes to the cloud?", false)
		if err != nil {
			return ConfigPushResult{}, err
		}
		if !ok {
			return ConfigPushResult{}, commandError(ExitUserCanceled, "user_canceled", "Config push was canceled before upload", nil)
		}
	}

	snapshot, err := config.SnapshotJSON(project)
	if err != nil {
		return ConfigPushResult{}, commandError(ExitValidationError, "config_invalid", "Cannot build target config snapshot", err)
	}
	opID, err := operationID("config_push")
	if err != nil {
		return ConfigPushResult{}, commandError(ExitInternalError, "internal_error", "Cannot create operation ID", err)
	}
	result.OperationID = opID
	pushResult, err := client.PushConfig(context.Background(), ident, project.TeamID, apiclient.ConfigPushRequest{
		OperationID:            opID,
		ExpectedConfigRevision: project.CloudRevision,
		TargetConfigSnapshot:   json.RawMessage(snapshot),
		Decisions:              decisions,
		ScopeKeyEnvelopes:      targetEnvelopes,
		Client:                 apiclient.ClientMetadata{CLIVersion: Version, ClientKind: "cli"},
	})
	if err != nil {
		return ConfigPushResult{}, mapAPIError(err, "Config push was rejected by the cloud")
	}

	project.CloudRevision = pushResult.NewRevision
	project.SyncStatus = "synced"
	rendered, err := config.RenderParsed(project)
	if err != nil {
		return ConfigPushResult{}, commandError(ExitValidationError, "config_invalid", "Cloud accepted config push but local config could not be rendered", err, "Run `propagate config pull` to recover the accepted cloud config.")
	}
	if err := config.WriteRaw(configPath, rendered); err != nil {
		return ConfigPushResult{}, commandError(ExitPartialLocalFailure, "partial_local_failure", "Cloud accepted config push but propagate.yaml could not be updated", err, "Run `propagate config pull` to recover the accepted cloud config.")
	}
	result.OldRevision = pushResult.OldRevision
	result.NewRevision = pushResult.NewRevision
	result.EnvelopesUploadedCount = pushResult.EnvelopesCount
	result.ConfigModified = rendered != project.Raw
	result.BackendStatus = "pushed"
	result.NextSteps = []string{"Review the updated propagate.yaml and commit the config revision change."}
	return result, nil
}


func buildNewScopeEnvelopesIfNeeded(ctx context.Context, client apiclient.Client, ident identity.Identity, project config.ParsedProject, target config.ParsedProject, status apiclient.ConfigStatusData) ([]apiclient.ScopeKeyEnvelope, error) {
	cloudScopeCount, ok := safeSummaryInt(status.SafeSummary, "scopes_count")
	if !ok || len(target.Scopes) <= cloudScopeCount {
		return nil, nil
	}
	cloudConfig, err := client.GetConfig(ctx, ident, project.TeamID)
	if err != nil {
		return nil, mapAPIError(err, "Cannot fetch current cloud config before creating scope envelopes")
	}
	if cloudConfig.ConfigRevision != "" && cloudConfig.ConfigRevision != project.CloudRevision {
		return nil, commandError(
			ExitConflict,
			"revision_conflict",
			"Cloud config revision changed while preparing new scope envelopes",
			nil,
			"Run `propagate config pull`, review the diff, and retry config push.",
		)
	}
	cloudProject, err := config.ParseSnapshot(cloudConfig.ConfigSnapshot, cloudConfig.ConfigRevision)
	if err != nil {
		return nil, commandError(ExitValidationError, "config_invalid", "Cannot parse current cloud config before creating scope envelopes", err)
	}
	cloudScopes := map[string]bool{}
	for _, scope := range cloudProject.Scopes {
		cloudScopes[scope.Name] = true
	}

	var envelopes []apiclient.ScopeKeyEnvelope
	for _, scope := range target.Scopes {
		if cloudScopes[scope.Name] {
			continue
		}
		scopeKey, err := secretcrypto.GenerateScopeKey()
		if err != nil {
			return nil, commandError(ExitInternalError, "internal_error", fmt.Sprintf("Cannot create scope key for %q", scope.Name), err)
		}
		for _, member := range target.Members {
			if !scopeMemberCanRead(scope, member) {
				continue
			}
			encryptedScopeKey, err := secretcrypto.EncryptScopeKey(scopeKey, member.EncryptionPublicKey, scope.Name, member.PublicKeySHA, 1)
			if err != nil {
				return nil, commandError(ExitValidationError, "config_invalid", fmt.Sprintf("Cannot encrypt scope key for member %s", member.PublicKeySHA), err)
			}
			envelopes = append(envelopes, apiclient.ScopeKeyEnvelope{
				Scope:             scope.Name,
				RecipientKeySHA:   member.PublicKeySHA,
				ScopeKeyVersion:   1,
				EncryptedScopeKey: encryptedScopeKey,
				Algorithm:         secretcrypto.EnvelopeAlgorithm,
			})
		}
	}
	return envelopes, nil
}

func safeSummaryInt(summary map[string]any, key string) (int, bool) {
	if summary == nil {
		return 0, false
	}
	switch value := summary[key].(type) {
	case int:
		return value, true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	default:
		return 0, false
	}
}

func scopeMemberCanRead(scope config.ScopeSummary, member config.Member) bool {
	permission := config.MemberScopePermission(member, scope)
	return permissionRank(permission) >= permissionRank("read")
}

func permissionRank(permission string) int {
	switch permission {
	case "admin":
		return 3
	case "write":
		return 2
	case "read":
		return 1
	default:
		return 0
	}
}


func findMember(members []config.Member, publicKeySHA string) *config.Member {
	for idx := range members {
		if members[idx].PublicKeySHA == publicKeySHA {
			return &members[idx]
		}
	}
	return nil
}

func resolveAPIURL(flagValue string, workDirs ...string) string {
	if value := strings.TrimSpace(flagValue); value != "" {
		return value
	}
	if value := strings.TrimSpace(os.Getenv("PROPAGATE_API_URL")); value != "" {
		return value
	}
	return resolveAPIURLFromDotenv(workDirs...)
}

func resolveAPIURLFromDotenv(workDirs ...string) string {
	for _, path := range apiURLDotenvCandidates(workDirs...) {
		parsed, err := envfile.ParseAssignments(path)
		if err != nil {
			continue
		}
		if value := strings.TrimSpace(parsed.Values["PROPAGATE_API_URL"]); value != "" {
			return value
		}
	}
	return ""
}

func loadLocalDotenv(workDir string) {
	for _, path := range apiURLDotenvCandidates(workDir) {
		parsed, err := envfile.ParseAssignments(path)
		if err != nil {
			continue
		}
		for name, value := range parsed.Values {
			if !strings.HasPrefix(name, "PROPAGATE_") {
				continue
			}
			if _, exists := os.LookupEnv(name); exists {
				continue
			}
			_ = os.Setenv(name, value)
		}
	}
}

func apiURLDotenvCandidates(workDirs ...string) []string {
	var candidates []string
	seen := map[string]bool{}
	add := func(path string) {
		path = filepath.Clean(path)
		if seen[path] {
			return
		}
		seen[path] = true
		candidates = append(candidates, path)
	}
	addDir := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		add(filepath.Join(dir, ".env"))
		add(filepath.Join(dir, "packages", "backend", ".env"))
		if worktree, err := gitutil.Discover(dir); err == nil {
			add(filepath.Join(worktree.Root, ".env"))
			add(filepath.Join(worktree.Root, "packages", "backend", ".env"))
		}
	}
	for _, dir := range workDirs {
		addDir(dir)
	}
	if len(workDirs) == 0 {
		cwd, err := os.Getwd()
		if err != nil {
			return candidates
		}
		addDir(cwd)
	}
	return candidates
}

func operationID(prefix string) (string, error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "op_" + prefix + "_" + time.Now().UTC().Format("20060102150405") + "_" + hex.EncodeToString(raw[:]), nil
}

func mapAPIError(err error, message string) error {
	var apiErr *apiclient.APIError
	if !errors.As(err, &apiErr) {
		return commandError(ExitCloudUnavailable, "cloud_unavailable", message, err)
	}
	switch apiErr.Code {
	case "permission_denied":
		return commandError(ExitPermissionDenied, apiErr.Code, message, apiErr)
	case "invite_pin_invalid", "invite_locked", "invite_not_active":
		return commandError(ExitValidationError, apiErr.Code, message, apiErr)
	case "revision_conflict", "idempotency_conflict":
		return commandError(ExitConflict, apiErr.Code, message, apiErr, "Run `propagate config pull`, review the diff, and retry config push.")
	case "validation_failed", "usage_error":
		return commandError(ExitValidationError, apiErr.Code, message, apiErr)
	default:
		code := ExitCloudUnavailable
		if apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 && !apiErr.Retryable {
			code = ExitValidationError
		}
		return commandError(code, apiErr.Code, message, apiErr)
	}
}

func renderConfigPushResult(w io.Writer, jsonOutput bool, noColor bool, result ConfigPushResult) {
	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}
	style := newOutputStyle(noColor)
	renderCommandTitle(w, style, "Config push", result.DryRun)
	switch result.Status {
	case "dry_run":
		renderNote(w, style, "Config push dry run complete.")
	case "no_change":
		renderNote(w, style, "Config already matches the cloud.")
	default:
		renderOK(w, style, "Config push complete.")
	}
	fmt.Fprintln(w)
	if result.TeamName != "" {
		fmt.Fprintf(w, "Team: %s (%s)\n", result.TeamName, result.TeamID)
	}
	if result.Identity != nil {
		fmt.Fprintf(w, "Pushed by: %s (%s)\n", result.Identity.Handle, result.Identity.PublicKeySHA)
	}
	if result.OldRevision != "" || result.NewRevision != "" {
		fmt.Fprintf(w, "Revision: %s -> %s\n", valueOrDash(result.OldRevision), valueOrDash(result.NewRevision))
	}
	fmt.Fprintf(w, "Encrypted access envelopes uploaded: %d\n", result.EnvelopesUploadedCount)
	fmt.Fprintf(w, "propagate.yaml modified: %t\n", result.ConfigModified)
	fmt.Fprintf(w, "Backend: %s\n", result.BackendStatus)
	renderWarnings(w, style, result.Warnings)
	renderNextSteps(w, style, result.NextSteps)
}

func copyStringMap(values map[string]string) map[string]string {
	out := map[string]string{}
	for key, value := range values {
		out[key] = value
	}
	return out
}

func valueOrDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func printConfigHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate config status [flags]")
	fmt.Fprintln(w, "  propagate config pull [flags]")
	fmt.Fprintln(w, "  propagate config push [flags]")
	fmt.Fprintln(w, "  propagate config edit [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  status    compare local config metadata with cloud state")
	fmt.Fprintln(w, "  pull      pull cloud config state into propagate.yaml")
	fmt.Fprintln(w, "  push      push approved propagate.yaml config decisions to the cloud")
	fmt.Fprintln(w, "  edit      open an interactive editor for variable declarations")
}

func printConfigPushHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate config push [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --dry-run                 validate and summarize without upload")
	fmt.Fprintln(w, "  --yes                     confirm the config push")
	fmt.Fprintln(w, "  --api-url VALUE           override Propagate API URL")
	fmt.Fprintln(w, "  --json                    render machine-readable JSON")
	fmt.Fprintln(w, "  --non-interactive         fail instead of prompting")
}

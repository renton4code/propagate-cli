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
	"sort"
	"strings"
	"time"

	"propagate/cli/internal/apiclient"
	"propagate/cli/internal/config"
	"propagate/cli/internal/gitutil"
	"propagate/cli/internal/identity"
	"propagate/cli/internal/secretcrypto"
)

type configPushOptions struct {
	globalOptions
	DryRun       bool
	Yes          bool
	ApproveJoins decisionFlags
	DeclineJoins decisionFlags
	SkipJoins    decisionFlags
}

type decisionFlags []string

var configPushHTTPClient *http.Client

type ConfigPushResult struct {
	OK                     bool              `json:"ok"`
	Command                string            `json:"command"`
	Status                 string            `json:"status"`
	DryRun                 bool              `json:"dry_run"`
	OperationID            string            `json:"operation_id,omitempty"`
	ProjectConfigPath      string            `json:"project_config_path"`
	TeamID                 string            `json:"team_id"`
	TeamName               string            `json:"team_name"`
	Identity               *identity.Summary `json:"identity,omitempty"`
	OldRevision            string            `json:"old_revision,omitempty"`
	NewRevision            string            `json:"new_revision,omitempty"`
	LocalConfigHash        string            `json:"local_config_hash,omitempty"`
	CloudConfigHash        string            `json:"cloud_config_hash,omitempty"`
	ApprovedJoins          []JoinDecision    `json:"approved_joins,omitempty"`
	DeclinedJoins          []JoinDecision    `json:"declined_joins,omitempty"`
	SkippedJoins           []JoinDecision    `json:"skipped_joins,omitempty"`
	EnvelopesUploadedCount int               `json:"envelopes_uploaded_count"`
	ConfigModified         bool              `json:"config_modified"`
	BackendStatus          string            `json:"backend_status"`
	Warnings               []string          `json:"warnings,omitempty"`
	NextSteps              []string          `json:"next_steps,omitempty"`
}

type JoinDecision struct {
	Handle       string            `json:"handle"`
	PublicKeySHA string            `json:"public_key_sha"`
	Role         string            `json:"role"`
	Scopes       map[string]string `json:"scopes,omitempty"`
	Decision     string            `json:"decision"`
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
	case "help", "--help", "-h":
		printConfigHelp(streams.Out)
		return ExitSuccess
	default:
		err := commandError(ExitUsageError, "usage_error", fmt.Sprintf("Unknown config command %q", args[0]), nil, "Run `propagate config help` to see available config commands.")
		return renderError(streams.Err, global.JSON, err)
	}
}

func runConfigPushCommand(args []string, global globalOptions, streams Streams) int {
	opts := configPushOptions{globalOptions: global}
	fs := flag.NewFlagSet("config push", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &opts.globalOptions)
	fs.BoolVar(&opts.DryRun, "dry-run", false, "validate and summarize without uploading or writing propagate.yaml")
	fs.BoolVar(&opts.Yes, "yes", false, "confirm the config push after reviewing decisions")
	fs.Var(&opts.ApproveJoins, "approve-join", "pending join public_key_sha to approve; may be repeated")
	fs.Var(&opts.DeclineJoins, "decline-join", "pending join public_key_sha to decline; may be repeated")
	fs.Var(&opts.SkipJoins, "skip-join", "pending join public_key_sha to leave pending; may be repeated")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printConfigPushHelp(streams.Out)
			return ExitSuccess
		}
		cmdErr := commandError(ExitUsageError, "usage_error", "Invalid config push flags", err, "Run `propagate config push --help` for usage.")
		return renderError(streams.Err, opts.JSON, cmdErr)
	}
	if fs.NArg() != 0 {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate config push does not accept positional arguments", nil)
		return renderError(streams.Err, opts.JSON, cmdErr)
	}

	result, err := runConfigPush(opts, streams)
	if err != nil {
		return renderError(streams.Err, opts.JSON, err)
	}
	renderConfigPushResult(streams.Out, opts.JSON, result)
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

	if len(project.AccessChangesRaw) > 0 {
		return ConfigPushResult{}, commandError(ExitValidationError, "config_invalid", "pending.access_changes are not supported by this implementation pass", nil, "Resolve or remove pending access_changes, then retry config push.")
	}

	actor := findMember(project.Members, summary.PublicKeySHA)
	if actor == nil || actor.Role != "admins" {
		return ConfigPushResult{}, commandError(ExitPermissionDenied, "permission_denied", "Only admins can push Propagate config changes", nil, "Ask a Propagate admin to review the config diff and run `propagate config push`.")
	}

	localHash, err := config.ConfigHash(project)
	if err != nil {
		return ConfigPushResult{}, commandError(ExitValidationError, "config_invalid", "Cannot normalize propagate.yaml for config push", err)
	}
	result.LocalConfigHash = localHash

	apiURL := resolveAPIURL(opts.APIURL)
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

	decisions, err := collectJoinDecisions(opts, reader, streams.Out, project.PendingJoins)
	if err != nil {
		return ConfigPushResult{}, err
	}
	target, approved, declined, skipped := applyJoinDecisions(project, decisions)
	result.ApprovedJoins = approved
	result.DeclinedJoins = declined
	result.SkippedJoins = skipped

	if len(project.PendingJoins) == 0 && status.State == "equal" {
		result.Status = "no_change"
		result.BackendStatus = "equal"
		result.NextSteps = []string{"No config push is needed."}
		return result, nil
	}
	if len(project.PendingJoins) > 0 && len(approved)+len(declined) == 0 {
		result.Status = "no_change"
		result.BackendStatus = "skipped"
		result.NextSteps = []string{"Skipped joins remain pending in propagate.yaml. Re-run config push when an admin is ready to approve or decline them."}
		return result, nil
	}
	if len(approved) > 0 {
		envelopes, err := buildApprovedJoinEnvelopes(context.Background(), client, ident, project, approved)
		if err != nil {
			return ConfigPushResult{}, err
		}
		result.EnvelopesUploadedCount = len(envelopes)
		targetEnvelopes := envelopes
		if opts.DryRun {
			result.Status = "dry_run"
			result.BackendStatus = "validated"
			result.ConfigModified = len(approved)+len(declined) > 0
			result.NextSteps = []string{"Re-run without `--dry-run` and with `--yes` after reviewing the decision summary."}
			return result, nil
		}
		return pushConfigDecisions(opts, streams, reader, client, ident, configPath, project, target, approved, declined, skipped, targetEnvelopes, &result)
	}

	if opts.DryRun {
		result.Status = "dry_run"
		result.BackendStatus = "validated"
		result.ConfigModified = len(approved)+len(declined) > 0
		result.NextSteps = []string{"Re-run without `--dry-run` and with `--yes` after reviewing the decision summary."}
		return result, nil
	}
	return pushConfigDecisions(opts, streams, reader, client, ident, configPath, project, target, approved, declined, skipped, nil, &result)
}

func pushConfigDecisions(
	opts configPushOptions,
	streams Streams,
	reader *bufio.Reader,
	client apiclient.Client,
	ident identity.Identity,
	configPath string,
	project config.ParsedProject,
	target config.ParsedProject,
	approved []JoinDecision,
	declined []JoinDecision,
	skipped []JoinDecision,
	envelopes []apiclient.ScopeKeyEnvelope,
	result *ConfigPushResult,
) (ConfigPushResult, error) {
	if opts.NonInteractive && !opts.Yes {
		return ConfigPushResult{}, commandError(ExitConfirmationRequired, "confirmation_required", "Non-interactive config push requires --yes", nil, "Re-run with `--yes` after reviewing the pending config decisions.")
	}
	if !opts.NonInteractive && !opts.Yes {
		ok, err := promptConfirm(reader, streams.Out, "Push accepted config decisions to the cloud?", false)
		if err != nil {
			return ConfigPushResult{}, err
		}
		if !ok {
			return ConfigPushResult{}, commandError(ExitUserCanceled, "user_canceled", "Config push was canceled before upload", nil)
		}
	}
	cloudTarget := target
	cloudTarget.PendingJoins = nil
	snapshot, err := config.SnapshotJSON(cloudTarget)
	if err != nil {
		return ConfigPushResult{}, commandError(ExitValidationError, "config_invalid", "Cannot build target config snapshot", err)
	}
	operationID, err := operationID("config_push")
	if err != nil {
		return ConfigPushResult{}, commandError(ExitInternalError, "internal_error", "Cannot create operation ID", err)
	}
	result.OperationID = operationID
	pushResult, err := client.PushConfig(context.Background(), ident, project.TeamID, apiclient.ConfigPushRequest{
		OperationID:            operationID,
		ExpectedConfigRevision: project.CloudRevision,
		TargetConfigSnapshot:   json.RawMessage(snapshot),
		Decisions:              apiDecisions(approved, declined, skipped),
		ScopeKeyEnvelopes:      envelopes,
		Client:                 apiclient.ClientMetadata{CLIVersion: Version, ClientKind: "cli"},
	})
	if err != nil {
		return ConfigPushResult{}, mapAPIError(err, "Config push was rejected by the cloud")
	}

	target.CloudRevision = pushResult.NewRevision
	target.SyncStatus = "synced"
	rendered, err := config.RenderParsed(target)
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
	return *result, nil
}

func buildApprovedJoinEnvelopes(ctx context.Context, client apiclient.Client, ident identity.Identity, project config.ParsedProject, approved []JoinDecision) ([]apiclient.ScopeKeyEnvelope, error) {
	joinsBySHA := map[string]config.JoinRequest{}
	for _, join := range project.PendingJoins {
		joinsBySHA[join.PublicKeySHA] = join
	}
	scopeKeyCache := map[string]struct {
		key     []byte
		version int
	}{}
	var envelopes []apiclient.ScopeKeyEnvelope
	for _, decision := range approved {
		join, ok := joinsBySHA[decision.PublicKeySHA]
		if !ok {
			return nil, commandError(ExitValidationError, "config_invalid", "Approved join was not found in pending requests", fmt.Errorf("%s", decision.PublicKeySHA))
		}
		for _, scope := range sortedJoinScopes(join.RequestedScopes) {
			permission := join.RequestedScopes[scope]
			if permission == "" || permission == "none" {
				continue
			}
			cached, ok := scopeKeyCache[scope]
			if !ok {
				envelopeData, err := client.KeyEnvelope(ctx, ident, project.TeamID, scope)
				if err != nil {
					return nil, mapAPIError(err, fmt.Sprintf("Cannot fetch current scope key envelope for scope %q", scope))
				}
				scopeKey, err := secretcrypto.DecryptScopeKey(
					ident.EncryptionPrivateKey,
					envelopeData.ScopeKeyEnvelope.EncryptedScopeKey,
					envelopeData.ScopeKeyEnvelope.Algorithm,
					scope,
					ident.PublicKeySHA,
					envelopeData.ScopeKeyEnvelope.ScopeKeyVersion,
				)
				if err != nil {
					return nil, commandError(ExitPermissionDenied, "scope_key_decrypt_failed", fmt.Sprintf("Cannot decrypt current scope key for scope %q", scope), err, "No config changes were pushed.")
				}
				cached = struct {
					key     []byte
					version int
				}{key: scopeKey, version: envelopeData.ScopeKeyEnvelope.ScopeKeyVersion}
				scopeKeyCache[scope] = cached
			}
			encryptedScopeKey, err := secretcrypto.EncryptScopeKey(cached.key, join.EncryptionPublicKey, scope, join.PublicKeySHA, cached.version)
			if err != nil {
				return nil, commandError(ExitValidationError, "config_invalid", fmt.Sprintf("Cannot encrypt scope key for approved member %s", join.PublicKeySHA), err)
			}
			envelopes = append(envelopes, apiclient.ScopeKeyEnvelope{
				Scope:             scope,
				RecipientKeySHA:   join.PublicKeySHA,
				ScopeKeyVersion:   cached.version,
				EncryptedScopeKey: encryptedScopeKey,
				Algorithm:         secretcrypto.EnvelopeAlgorithm,
			})
		}
	}
	return envelopes, nil
}

func (f *decisionFlags) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(*f, ",")
}

func (f *decisionFlags) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("public_key_sha cannot be empty")
	}
	*f = append(*f, value)
	return nil
}

func collectJoinDecisions(opts configPushOptions, reader *bufio.Reader, out io.Writer, joins []config.JoinRequest) (map[string]string, error) {
	decisions, err := explicitJoinDecisions(opts)
	if err != nil {
		return nil, commandError(ExitUsageError, "usage_error", "Invalid config push decisions", err)
	}
	pending := map[string]config.JoinRequest{}
	for _, join := range joins {
		pending[join.PublicKeySHA] = join
	}
	for sha := range decisions {
		if _, ok := pending[sha]; !ok {
			return nil, commandError(ExitUsageError, "usage_error", "Decision references a public_key_sha that is not pending", fmt.Errorf("%s is not in pending.joins", sha))
		}
	}
	for _, join := range joins {
		if _, ok := decisions[join.PublicKeySHA]; ok {
			continue
		}
		if opts.NonInteractive {
			return nil, commandError(ExitConfirmationRequired, "confirmation_required", "Every pending join needs an explicit approve, decline, or skip decision in non-interactive mode", nil, "Pass --approve-join, --decline-join, or --skip-join for each pending public_key_sha.")
		}
		decision, err := promptJoinDecision(reader, out, join)
		if err != nil {
			return nil, err
		}
		decisions[join.PublicKeySHA] = decision
	}
	return decisions, nil
}

func explicitJoinDecisions(opts configPushOptions) (map[string]string, error) {
	out := map[string]string{}
	add := func(values []string, decision string) error {
		for _, value := range values {
			if existing, ok := out[value]; ok && existing != decision {
				return fmt.Errorf("%s has conflicting decisions %s and %s", value, existing, decision)
			}
			out[value] = decision
		}
		return nil
	}
	if err := add(opts.ApproveJoins, "approve"); err != nil {
		return nil, err
	}
	if err := add(opts.DeclineJoins, "decline"); err != nil {
		return nil, err
	}
	if err := add(opts.SkipJoins, "skip"); err != nil {
		return nil, err
	}
	return out, nil
}

func promptJoinDecision(reader *bufio.Reader, out io.Writer, join config.JoinRequest) (string, error) {
	for {
		fmt.Fprintf(out, "Pending join %s (%s). Approve, decline, or skip? [a/d/s]: ", join.Handle, join.PublicKeySHA)
		value, err := reader.ReadString('\n')
		if err != nil && len(value) == 0 {
			return "", commandError(ExitUserCanceled, "user_canceled", "Prompt could not read input", err)
		}
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "a", "approve":
			return "approve", nil
		case "d", "decline":
			return "decline", nil
		case "s", "skip", "":
			return "skip", nil
		default:
			fmt.Fprintln(out, "Please enter a, d, or s.")
		}
	}
}

func applyJoinDecisions(project config.ParsedProject, decisions map[string]string) (config.ParsedProject, []JoinDecision, []JoinDecision, []JoinDecision) {
	target := project
	target.Members = append([]config.Member(nil), project.Members...)
	target.PendingJoins = nil
	var approved []JoinDecision
	var declined []JoinDecision
	var skipped []JoinDecision
	for _, join := range project.PendingJoins {
		summary := joinDecisionSummary(join, decisions[join.PublicKeySHA])
		switch summary.Decision {
		case "approve":
			target.Members = append(target.Members, config.Member{
				Handle:              join.Handle,
				PublicKeySHA:        join.PublicKeySHA,
				SigningPublicKey:    join.SigningPublicKey,
				EncryptionPublicKey: join.EncryptionPublicKey,
				Role:                join.RequestedRole,
			})
			approved = append(approved, summary)
		case "decline":
			declined = append(declined, summary)
		default:
			target.PendingJoins = append(target.PendingJoins, join)
			summary.Decision = "skip"
			skipped = append(skipped, summary)
		}
	}
	return target, approved, declined, skipped
}

func joinDecisionSummary(join config.JoinRequest, decision string) JoinDecision {
	if decision == "" {
		decision = "skip"
	}
	return JoinDecision{
		Handle:       join.Handle,
		PublicKeySHA: join.PublicKeySHA,
		Role:         join.RequestedRole,
		Scopes:       join.RequestedScopes,
		Decision:     decision,
	}
}

func apiDecisions(approved []JoinDecision, declined []JoinDecision, skipped []JoinDecision) apiclient.ConfigDecisions {
	return apiclient.ConfigDecisions{
		Approved: apiDecisionList(approved, true),
		Declined: apiDecisionList(declined, false),
		Skipped:  apiDecisionList(skipped, false),
	}
}

func apiDecisionList(items []JoinDecision, includeScopeAccess bool) []apiclient.ConfigDecision {
	out := make([]apiclient.ConfigDecision, 0, len(items))
	for _, item := range items {
		out = append(out, apiclient.ConfigDecision{
			Type:         "join",
			Handle:       item.Handle,
			PublicKeySHA: item.PublicKeySHA,
			Role:         item.Role,
		})
		if !includeScopeAccess {
			continue
		}
		for _, scope := range sortedJoinScopes(item.Scopes) {
			permission := item.Scopes[scope]
			if permission == "" || permission == "none" {
				continue
			}
			out = append(out, apiclient.ConfigDecision{
				Type:         "scope_access_change",
				PublicKeySHA: item.PublicKeySHA,
				Scope:        scope,
				Permission:   permission,
			})
		}
	}
	return out
}

func findMember(members []config.Member, publicKeySHA string) *config.Member {
	for idx := range members {
		if members[idx].PublicKeySHA == publicKeySHA {
			return &members[idx]
		}
	}
	return nil
}

func resolveAPIURL(flagValue string) string {
	if value := strings.TrimSpace(flagValue); value != "" {
		return value
	}
	return strings.TrimSpace(os.Getenv("PROPAGATE_API_URL"))
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

func renderConfigPushResult(w io.Writer, jsonOutput bool, result ConfigPushResult) {
	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}
	switch result.Status {
	case "dry_run":
		fmt.Fprintln(w, "Config push dry run complete.")
	case "no_change":
		fmt.Fprintln(w, "Config already matches the cloud.")
	default:
		fmt.Fprintln(w, "Config push complete.")
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
	fmt.Fprintf(w, "Approved joins: %d\n", len(result.ApprovedJoins))
	fmt.Fprintf(w, "Declined joins: %d\n", len(result.DeclinedJoins))
	fmt.Fprintf(w, "Skipped joins: %d\n", len(result.SkippedJoins))
	fmt.Fprintf(w, "Encrypted access envelopes uploaded: %d\n", result.EnvelopesUploadedCount)
	fmt.Fprintf(w, "propagate.yaml modified: %t\n", result.ConfigModified)
	fmt.Fprintf(w, "Backend: %s\n", result.BackendStatus)
	renderJoinDecisionDetails(w, "Approved", result.ApprovedJoins)
	renderJoinDecisionDetails(w, "Declined", result.DeclinedJoins)
	renderJoinDecisionDetails(w, "Skipped", result.SkippedJoins)
	if len(result.Warnings) > 0 {
		fmt.Fprintln(w, "\nWarnings:")
		for _, warning := range result.Warnings {
			fmt.Fprintf(w, "- %s\n", warning)
		}
	}
	if len(result.NextSteps) > 0 {
		fmt.Fprintln(w, "\nNext steps:")
		for i, step := range result.NextSteps {
			fmt.Fprintf(w, "%d. %s\n", i+1, step)
		}
	}
}

func renderJoinDecisionDetails(w io.Writer, label string, joins []JoinDecision) {
	if len(joins) == 0 {
		return
	}
	fmt.Fprintf(w, "%s joins:\n", label)
	for _, join := range joins {
		fmt.Fprintf(w, "  - %s (%s), role %s", join.Handle, join.PublicKeySHA, join.Role)
		if len(join.Scopes) > 0 {
			var parts []string
			for _, scope := range sortedJoinScopes(join.Scopes) {
				parts = append(parts, scope+":"+join.Scopes[scope])
			}
			fmt.Fprintf(w, ", scopes %s", strings.Join(parts, ", "))
		}
		fmt.Fprintln(w)
	}
}

func sortedJoinScopes(scopes map[string]string) []string {
	names := make([]string, 0, len(scopes))
	for name := range scopes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  status    compare local config metadata with cloud state")
	fmt.Fprintln(w, "  pull      pull cloud config state into propagate.yaml")
	fmt.Fprintln(w, "  push      push approved propagate.yaml config decisions to the cloud")
}

func printConfigPushHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate config push [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --dry-run                 validate and summarize without upload")
	fmt.Fprintln(w, "  --yes                     confirm upload after reviewing decisions")
	fmt.Fprintln(w, "  --approve-join SHA        approve pending join by public_key_sha")
	fmt.Fprintln(w, "  --decline-join SHA        decline pending join by public_key_sha")
	fmt.Fprintln(w, "  --skip-join SHA           leave pending join for later")
	fmt.Fprintln(w, "  --api-url VALUE           override Propagate API URL")
	fmt.Fprintln(w, "  --json                    render machine-readable JSON")
	fmt.Fprintln(w, "  --non-interactive         fail instead of prompting")
}

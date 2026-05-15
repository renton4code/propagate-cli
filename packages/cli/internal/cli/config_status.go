package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"propagate/cli/internal/apiclient"
	"propagate/cli/internal/config"
	"propagate/cli/internal/gitutil"
	"propagate/cli/internal/identity"
)

type configStatusOptions struct {
	globalOptions
}

type ConfigStatusResult struct {
	OK                bool              `json:"ok"`
	Command           string            `json:"command"`
	Status            string            `json:"status"`
	ProjectConfigPath string            `json:"project_config_path"`
	TeamID            string            `json:"team_id"`
	TeamName          string            `json:"team_name"`
	Identity          *identity.Summary `json:"identity,omitempty"`
	LocalRevision     string            `json:"local_revision,omitempty"`
	CloudRevision     string            `json:"cloud_revision,omitempty"`
	LocalConfigHash   string            `json:"local_config_hash,omitempty"`
	CloudConfigHash   string            `json:"cloud_config_hash,omitempty"`
	State             string            `json:"state,omitempty"`
	RecommendedAction string            `json:"recommended_action,omitempty"`
	BackendStatus     string            `json:"backend_status"`
	LocalOnlyChanges  []string          `json:"local_only_changes,omitempty"`
	CloudOnlyChanges  []string          `json:"cloud_only_changes,omitempty"`
	SafeSummary       map[string]any    `json:"safe_summary,omitempty"`
	Warnings          []string          `json:"warnings,omitempty"`
	NextSteps         []string          `json:"next_steps,omitempty"`
}

func runConfigStatusCommand(args []string, global globalOptions, streams Streams) int {
	opts := configStatusOptions{globalOptions: global}
	fs := flag.NewFlagSet("config status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &opts.globalOptions)

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printConfigStatusHelp(streams.Out)
			return ExitSuccess
		}
		cmdErr := commandError(ExitUsageError, "usage_error", "Invalid config status flags", err, "Run `propagate config status --help` for usage.")
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if fs.NArg() != 0 {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate config status does not accept positional arguments", nil)
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}

	result, err := runConfigStatus(opts, streams)
	if err != nil {
		if resultHasLocalConfigFacts(result) {
			if opts.JSON {
				renderConfigStatusError(streams.Err, result, err)
				return errorExitCode(err)
			}
			renderConfigStatusResult(streams.Out, false, opts.NoColor, result)
		}
		return renderError(streams.Err, opts.JSON, opts.NoColor, err)
	}
	renderConfigStatusResult(streams.Out, opts.JSON, opts.NoColor, result)
	return ExitSuccess
}

func runConfigStatus(opts configStatusOptions, streams Streams) (ConfigStatusResult, error) {
	result := ConfigStatusResult{
		OK:            true,
		Command:       "config status",
		Status:        "success",
		BackendStatus: "not_contacted",
	}

	worktree, err := gitutil.Discover(streams.WorkDir)
	if err != nil {
		return ConfigStatusResult{}, commandError(ExitValidationError, "not_git_repo", "Cannot check config status outside a Git worktree", err)
	}
	configPath, exists, err := config.ExistingPath(worktree.Root)
	if err != nil {
		return ConfigStatusResult{}, commandError(ExitValidationError, "config_invalid", "Existing Propagate config needs attention", err, "Rename `propagate.yml` to `propagate.yaml` before running config status again.")
	}
	if !exists {
		return ConfigStatusResult{}, commandError(ExitValidationError, "config_missing", "propagate.yaml is required before config status", nil, "Run `propagate init` or pull the repository config first.")
	}
	result.ProjectConfigPath = configPath

	project, err := config.ReadProject(configPath)
	if err != nil {
		return ConfigStatusResult{}, commandError(ExitValidationError, "config_invalid", "Cannot read propagate.yaml", err)
	}
	result.TeamID = project.TeamID
	result.TeamName = project.TeamName
	result.LocalRevision = project.CloudRevision

	localHash, err := config.ConfigHash(project)
	if err != nil {
		return ConfigStatusResult{}, commandError(ExitValidationError, "config_invalid", "Cannot normalize propagate.yaml for config status", err)
	}
	result.LocalConfigHash = localHash
	result.LocalOnlyChanges = summarizeLocalConfigStatusChanges(project, apiclient.ConfigStatusData{})

	ident, err := identity.Load()
	if err != nil {
		result.OK = false
		result.Status = "identity_missing"
		result.Warnings = append(result.Warnings, "Cloud status was not checked because the local Propagate identity could not be loaded.")
		result.NextSteps = []string{"Run `propagate init` to create or repair the local identity."}
		return result, commandError(ExitValidationError, "identity_missing", "Cannot load local Propagate identity for signed config status", err, result.NextSteps...)
	}
	summary := ident.Summary()
	result.Identity = &summary

	apiURL := resolveAPIURL(opts.APIURL, streams.WorkDir)
	if apiURL == "" {
		result.OK = false
		result.Status = "cloud_unavailable"
		result.BackendStatus = "not_contacted"
		result.Warnings = append(result.Warnings, "Cloud status was not checked because no Propagate API URL is configured.")
		result.NextSteps = []string{"Pass `--api-url` or set PROPAGATE_API_URL."}
		return result, commandError(ExitCloudUnavailable, "cloud_unavailable", "Propagate API URL is required for config status", nil, result.NextSteps...)
	}

	client := apiclient.Client{BaseURL: apiURL, HTTPClient: configPushHTTPClient, CLIVersion: Version}
	status, err := client.ConfigStatus(context.Background(), ident, project.TeamID, project.CloudRevision, localHash)
	if err != nil {
		cmdErr := mapAPIError(err, "Cannot fetch current cloud config status")
		result.OK = false
		result.Status = "failed"
		result.BackendStatus = "failed"
		result.Warnings = append(result.Warnings, "Cloud status request failed; local config facts are shown above.")
		result.NextSteps = commandNextSteps(cmdErr, "Retry `propagate config status` after checking connectivity and credentials.")
		if commandCode(cmdErr) == ExitCloudUnavailable {
			result.Status = "cloud_unavailable"
			result.BackendStatus = "unavailable"
			result.Warnings = []string{"Cloud status could not be fetched; local config facts are shown above."}
		}
		return result, cmdErr
	}

	result.CloudRevision = status.CloudRevision
	result.CloudConfigHash = status.CloudConfigHash
	result.State = status.State
	result.RecommendedAction = status.RecommendedAction
	result.BackendStatus = status.State
	result.SafeSummary = status.SafeSummary
	result.LocalOnlyChanges = summarizeLocalConfigStatusChanges(project, status)
	result.CloudOnlyChanges = summarizeCloudConfigStatusChanges(project, status)
	result.NextSteps = configStatusNextSteps(status.RecommendedAction)
	return result, nil
}

func summarizeLocalConfigStatusChanges(project config.ParsedProject, status apiclient.ConfigStatusData) []string {
	var changes []string
	if project.SyncStatus != "" && project.SyncStatus != "synced" {
		changes = append(changes, "sync status: "+project.SyncStatus)
	}
	if project.CloudRevision == config.LocalRevision {
		changes = append(changes, "local config has not been pushed to the cloud")
	}
	if len(project.PendingJoins) > 0 {
		changes = append(changes, "pending joins: "+strings.Join(configStatusJoinLabels(project.PendingJoins), ", "))
	}
	if len(project.AccessChangesRaw) > 0 {
		changes = append(changes, "pending access changes: "+strconv.Itoa(len(project.AccessChangesRaw)))
	}
	switch status.State {
	case "local_ahead":
		if len(changes) == 0 {
			changes = append(changes, "local config hash differs from cloud at the same revision")
		}
	case "conflict":
		if revisionGreater(project.CloudRevision, status.CloudRevision) {
			changes = append(changes, "local revision "+project.CloudRevision+" is newer than cloud revision "+status.CloudRevision)
		}
	}
	return changes
}

func summarizeCloudConfigStatusChanges(project config.ParsedProject, status apiclient.ConfigStatusData) []string {
	var changes []string
	switch status.State {
	case "cloud_ahead":
		changes = append(changes, "cloud revision "+status.CloudRevision+" is newer than local revision "+project.CloudRevision)
	case "conflict":
		if status.CloudRevision != "" && status.CloudRevision != project.CloudRevision {
			changes = append(changes, "cloud revision "+status.CloudRevision+" differs from local revision "+project.CloudRevision)
		}
	}
	if status.State == "cloud_ahead" || status.State == "conflict" {
		if len(status.SafeSummary) > 0 {
			changes = append(changes, "cloud summary: "+strings.Join(configStatusSafeSummaryLines(status.SafeSummary), "; "))
		}
	}
	return changes
}

func configStatusJoinLabels(joins []config.JoinRequest) []string {
	labels := make([]string, 0, len(joins))
	for _, join := range joins {
		labels = append(labels, memberLabel(join.Handle, join.PublicKeySHA))
	}
	sort.Strings(labels)
	return labels
}

func revisionGreater(left, right string) bool {
	leftNum, leftErr := revisionNumberOrZero(left)
	rightNum, rightErr := revisionNumberOrZero(right)
	return leftErr == nil && rightErr == nil && leftNum > rightNum
}

func revisionNumberOrZero(value string) (int, error) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "rev_") {
		return 0, fmt.Errorf("invalid revision %q", value)
	}
	return strconv.Atoi(strings.TrimPrefix(value, "rev_"))
}

func configStatusNextSteps(action string) []string {
	switch action {
	case "none":
		return []string{"No config sync action is needed."}
	case "push":
		return []string{"Run `propagate config push` after reviewing local config changes."}
	case "pull":
		return []string{"Run `propagate config pull --dry-run` to review cloud changes before updating propagate.yaml."}
	case "resolve_conflict":
		return []string{"Run `propagate config pull --dry-run`, review the cloud changes, and resolve the local config conflict before pushing."}
	default:
		return []string{"Review local and cloud revisions before changing propagate.yaml."}
	}
}

func renderConfigStatusResult(w io.Writer, jsonOutput bool, noColor bool, result ConfigStatusResult) {
	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}
	style := newOutputStyle(noColor)
	renderCommandTitle(w, style, "Propagate config status", false)
	switch result.Status {
	case "cloud_unavailable":
		renderWarning(w, style, "Config local status available; cloud status unavailable.")
	case "identity_missing":
		renderWarning(w, style, "Config local status available; identity is missing.")
	default:
		renderConfigStatusHeadline(w, style, result.State)
	}
	fmt.Fprintln(w)
	if result.TeamName != "" {
		fmt.Fprintf(w, "Team: %s (%s)\n", result.TeamName, result.TeamID)
	}
	if result.Identity != nil {
		fmt.Fprintf(w, "Checked by: %s (%s)\n", result.Identity.Handle, result.Identity.PublicKeySHA)
	}
	fmt.Fprintf(w, "Local revision: %s\n", valueOrDash(result.LocalRevision))
	fmt.Fprintf(w, "Cloud revision: %s\n", valueOrDash(result.CloudRevision))
	fmt.Fprintf(w, "Local config hash: %s\n", valueOrDash(result.LocalConfigHash))
	fmt.Fprintf(w, "Cloud config hash: %s\n", valueOrDash(result.CloudConfigHash))
	if result.State != "" {
		fmt.Fprintf(w, "State: %s\n", result.State)
	}
	if result.RecommendedAction != "" {
		fmt.Fprintf(w, "Recommended action: %s\n", result.RecommendedAction)
	}
	fmt.Fprintf(w, "Backend: %s\n", result.BackendStatus)
	renderConfigStatusList(w, style, "Local-only changes", result.LocalOnlyChanges)
	renderConfigStatusList(w, style, "Cloud-only changes", result.CloudOnlyChanges)
	if len(result.SafeSummary) > 0 {
		renderConfigStatusList(w, style, "Cloud summary", configStatusSafeSummaryLines(result.SafeSummary))
	}
	renderWarnings(w, style, result.Warnings)
	renderNextSteps(w, style, result.NextSteps)
}

func renderConfigStatusHeadline(w io.Writer, style outputStyle, state string) {
	headline := configStatusHeadline(state)
	switch state {
	case "equal":
		renderOK(w, style, headline)
	case "local_ahead", "cloud_ahead", "conflict":
		renderWarning(w, style, headline)
	default:
		renderNote(w, style, headline)
	}
}

func configStatusHeadline(state string) string {
	switch state {
	case "equal":
		return "Config matches the cloud."
	case "local_ahead":
		return "Config has local changes."
	case "cloud_ahead":
		return "Cloud config is ahead."
	case "conflict":
		return "Config revisions conflict."
	default:
		return "Config status needs review."
	}
}

func renderConfigStatusList(w io.Writer, style outputStyle, label string, values []string) {
	renderListSection(w, style, label, values)
}

func configStatusSafeSummaryLines(summary map[string]any) []string {
	keys := make([]string, 0, len(summary))
	for key := range summary {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("%s=%v", key, summary[key]))
	}
	return lines
}

func renderConfigStatusError(w io.Writer, result ConfigStatusResult, err error) {
	cmdErr, ok := err.(*CommandError)
	if !ok {
		cmdErr = commandError(ExitInternalError, "internal_error", "Unexpected internal error", err)
	}
	result.OK = false
	payload := struct {
		ConfigStatusResult
		Error *CommandError `json:"error"`
	}{
		ConfigStatusResult: result,
		Error:              cmdErr,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(payload)
}

func resultHasLocalConfigFacts(result ConfigStatusResult) bool {
	return result.ProjectConfigPath != "" || result.TeamID != "" || result.LocalConfigHash != ""
}

func errorExitCode(err error) int {
	return commandCode(err)
}

func commandCode(err error) int {
	cmdErr, ok := err.(*CommandError)
	if !ok {
		return ExitInternalError
	}
	return cmdErr.Code
}

func commandNextSteps(err error, fallback string) []string {
	cmdErr, ok := err.(*CommandError)
	if !ok || len(cmdErr.NextSteps) == 0 {
		return []string{fallback}
	}
	return cmdErr.NextSteps
}

func printConfigStatusHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate config status [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --api-url VALUE     override Propagate API URL")
	fmt.Fprintln(w, "  --json              render machine-readable JSON")
	fmt.Fprintln(w, "  --non-interactive   fail instead of prompting")
}

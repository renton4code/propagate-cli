package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"propagate/cli/internal/apiclient"
	"propagate/cli/internal/config"
	"propagate/cli/internal/gitutil"
	"propagate/cli/internal/identity"
)

type configPullOptions struct {
	globalOptions
	DryRun bool
	Yes    bool
}

type ConfigPullResult struct {
	OK                bool                    `json:"ok"`
	Command           string                  `json:"command"`
	Status            string                  `json:"status"`
	DryRun            bool                    `json:"dry_run"`
	Updated           bool                    `json:"updated"`
	WouldUpdate       bool                    `json:"would_update"`
	WouldOverwrite    bool                    `json:"would_overwrite"`
	ProjectConfigPath string                  `json:"project_config_path"`
	TeamID            string                  `json:"team_id"`
	TeamName          string                  `json:"team_name"`
	Identity          *identity.Summary       `json:"identity,omitempty"`
	OldRevision       string                  `json:"old_revision,omitempty"`
	NewRevision       string                  `json:"new_revision,omitempty"`
	LocalConfigHash   string                  `json:"local_config_hash,omitempty"`
	CloudConfigHash   string                  `json:"cloud_config_hash,omitempty"`
	BackendStatus     string                  `json:"backend_status"`
	Changes           ConfigPullChangeSummary `json:"changes"`
	Warnings          []string                `json:"warnings,omitempty"`
	NextSteps         []string                `json:"next_steps,omitempty"`
}

type ConfigPullChangeSummary struct {
	TeamNameChanged     bool     `json:"team_name_changed,omitempty"`
	MembersAdded        []string `json:"members_added,omitempty"`
	MembersRemoved      []string `json:"members_removed,omitempty"`
	MembersChanged      []string `json:"members_changed,omitempty"`
	ScopesAdded         []string `json:"scopes_added,omitempty"`
	ScopesRemoved       []string `json:"scopes_removed,omitempty"`
	ScopesChanged       []string `json:"scopes_changed,omitempty"`
}

func runConfigPullCommand(args []string, global globalOptions, streams Streams) int {
	opts := configPullOptions{globalOptions: global}
	fs := flag.NewFlagSet("config pull", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &opts.globalOptions)
	fs.BoolVar(&opts.DryRun, "dry-run", false, "show what would change without writing propagate.yaml")
	fs.BoolVar(&opts.Yes, "yes", false, "confirm overwriting local config changes")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printConfigPullHelp(streams.Out)
			return ExitSuccess
		}
		cmdErr := commandError(ExitUsageError, "usage_error", "Invalid config pull flags", err, "Run `propagate config pull --help` for usage.")
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if fs.NArg() != 0 {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate config pull does not accept positional arguments", nil)
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}

	result, err := runConfigPull(opts, streams)
	if err != nil {
		return renderError(streams.Err, opts.JSON, opts.NoColor, err)
	}
	renderConfigPullResult(streams.Out, opts.JSON, opts.NoColor, result)
	return ExitSuccess
}

func runConfigPull(opts configPullOptions, streams Streams) (ConfigPullResult, error) {
	reader := bufio.NewReader(streams.In)
	result := ConfigPullResult{
		OK:            true,
		Command:       "config pull",
		Status:        "success",
		DryRun:        opts.DryRun,
		BackendStatus: "not_contacted",
	}

	ident, err := identity.Load()
	if err != nil {
		return ConfigPullResult{}, commandError(ExitValidationError, "identity_missing", "Cannot load local Propagate identity for signed config pull", err, "Run `propagate init` to create or repair the local identity.")
	}
	summary := ident.Summary()
	result.Identity = &summary

	worktree, err := gitutil.Discover(streams.WorkDir)
	if err != nil {
		return ConfigPullResult{}, commandError(ExitValidationError, "not_git_repo", "Cannot pull config outside a Git worktree", err)
	}
	configPath, exists, err := config.ExistingPath(worktree.Root)
	if err != nil {
		return ConfigPullResult{}, commandError(ExitValidationError, "config_invalid", "Existing Propagate config needs attention", err, "Rename `propagate.yml` to `propagate.yaml` before running config pull again.")
	}
	if !exists {
		return ConfigPullResult{}, commandError(ExitValidationError, "config_missing", "propagate.yaml is required before config pull", nil, "Run `propagate init` or pull the repository config first.")
	}
	result.ProjectConfigPath = configPath

	project, err := config.ReadProject(configPath)
	if err != nil {
		return ConfigPullResult{}, commandError(ExitValidationError, "config_invalid", "Cannot read propagate.yaml", err)
	}
	result.TeamID = project.TeamID
	result.TeamName = project.TeamName
	result.OldRevision = project.CloudRevision

	localHash, err := config.ConfigHash(project)
	if err != nil {
		return ConfigPullResult{}, commandError(ExitValidationError, "config_invalid", "Cannot normalize local propagate.yaml for config pull", err)
	}
	result.LocalConfigHash = localHash

	apiURL := resolveAPIURL(opts.APIURL, streams.WorkDir)
	if apiURL == "" {
		return ConfigPullResult{}, commandError(ExitCloudUnavailable, "cloud_unavailable", "Propagate API URL is required for config pull", nil, "Pass `--api-url` or set PROPAGATE_API_URL.")
	}
	client := apiclient.Client{BaseURL: apiURL, HTTPClient: configPushHTTPClient, CLIVersion: Version}
	cloud, err := client.GetConfig(context.Background(), ident, project.TeamID)
	if err != nil {
		return ConfigPullResult{}, mapAPIError(err, "Cannot fetch cloud config")
	}
	result.BackendStatus = "fetched"
	result.NewRevision = cloud.ConfigRevision
	result.CloudConfigHash = cloud.ConfigHash

	pulled, err := config.ParseSnapshot(cloud.ConfigSnapshot, cloud.ConfigRevision)
	if err != nil {
		return ConfigPullResult{}, commandError(ExitValidationError, "config_invalid", "Cloud config snapshot is invalid", err, "Contact a Propagate management member before overwriting local config.")
	}
	if pulled.TeamID != project.TeamID {
		return ConfigPullResult{}, commandError(ExitValidationError, "config_invalid", "Cloud config belongs to a different team", fmt.Errorf("local team %s, cloud team %s", project.TeamID, pulled.TeamID))
	}
	pulledHash, err := config.ConfigHash(pulled)
	if err != nil {
		return ConfigPullResult{}, commandError(ExitValidationError, "config_invalid", "Cannot normalize pulled cloud config", err)
	}
	if cloud.ConfigHash != "" && pulledHash != cloud.ConfigHash {
		return ConfigPullResult{}, commandError(ExitValidationError, "config_invalid", "Cloud config hash does not match the pulled snapshot", fmt.Errorf("expected %s, got %s", cloud.ConfigHash, pulledHash))
	}

	rendered, err := config.RenderParsed(pulled)
	if err != nil {
		return ConfigPullResult{}, commandError(ExitValidationError, "config_invalid", "Cloud config snapshot cannot be rendered as propagate.yaml", err)
	}
	result.TeamName = pulled.TeamName
	result.Changes = summarizeConfigPullChanges(project, pulled)
	result.WouldUpdate = project.CloudRevision != cloud.ConfigRevision || localHash != pulledHash || project.SyncStatus != "synced"
	result.WouldOverwrite = result.WouldUpdate && hasLocalUnpushedConfig(project, localHash, cloud.ConfigRevision, pulledHash)

	if !result.WouldUpdate {
		result.Status = "no_change"
		result.BackendStatus = "equal"
		result.NextSteps = []string{"No config pull is needed."}
		return result, nil
	}
	if opts.DryRun {
		result.Status = "dry_run"
		result.BackendStatus = "validated"
		result.NextSteps = []string{"Re-run without `--dry-run` after reviewing the change summary."}
		return result, nil
	}
	if result.WouldOverwrite && !opts.Yes {
		if opts.NonInteractive {
			return ConfigPullResult{}, commandError(ExitConfirmationRequired, "confirmation_required", "Non-interactive config pull requires --yes before overwriting local config changes", nil, "Re-run with `--yes` after reviewing `propagate config pull --dry-run`.")
		}
		ok, err := promptConfirm(reader, streams.In, streams.Out, "Overwrite local propagate.yaml with the cloud config?", false)
		if err != nil {
			return ConfigPullResult{}, err
		}
		if !ok {
			return ConfigPullResult{}, commandError(ExitUserCanceled, "user_canceled", "Config pull was canceled before writing propagate.yaml", nil)
		}
	}
	if err := config.WriteRaw(configPath, rendered); err != nil {
		return ConfigPullResult{}, commandError(ExitPartialLocalFailure, "partial_local_failure", "Cloud config was fetched but propagate.yaml could not be updated", err)
	}
	result.Updated = true
	result.BackendStatus = "pulled"
	result.NextSteps = []string{"Review the updated propagate.yaml and commit the config revision change."}
	return result, nil
}

func hasLocalUnpushedConfig(project config.ParsedProject, localHash string, cloudRevision string, cloudHash string) bool {
	if localHash == cloudHash {
		return false
	}
	if project.CloudRevision == cloudRevision {
		return true
	}
	if project.CloudRevision == config.LocalRevision || project.SyncStatus != "synced" {
		return true
	}
	return false
}

func summarizeConfigPullChanges(local, pulled config.ParsedProject) ConfigPullChangeSummary {
	return ConfigPullChangeSummary{
		TeamNameChanged: local.TeamName != pulled.TeamName,
		MembersAdded:    addedMembers(local.Members, pulled.Members),
		MembersRemoved:  removedMembers(local.Members, pulled.Members),
		MembersChanged:  changedMembers(local.Members, pulled.Members),
		ScopesAdded:     addedScopes(local.Scopes, pulled.Scopes),
		ScopesRemoved:   removedScopes(local.Scopes, pulled.Scopes),
		ScopesChanged:   changedScopes(local.Scopes, pulled.Scopes),
	}
}

func addedMembers(local, pulled []config.Member) []string {
	localBySHA := memberMap(local)
	var out []string
	for _, member := range pulled {
		if _, ok := localBySHA[member.PublicKeySHA]; !ok {
			out = append(out, memberLabel(member.Handle, member.PublicKeySHA))
		}
	}
	sort.Strings(out)
	return out
}

func removedMembers(local, pulled []config.Member) []string {
	pulledBySHA := memberMap(pulled)
	var out []string
	for _, member := range local {
		if _, ok := pulledBySHA[member.PublicKeySHA]; !ok {
			out = append(out, memberLabel(member.Handle, member.PublicKeySHA))
		}
	}
	sort.Strings(out)
	return out
}

func changedMembers(local, pulled []config.Member) []string {
	localBySHA := memberMap(local)
	var out []string
	for _, member := range pulled {
		if previous, ok := localBySHA[member.PublicKeySHA]; ok && !reflect.DeepEqual(previous, member) {
			out = append(out, memberLabel(member.Handle, member.PublicKeySHA))
		}
	}
	sort.Strings(out)
	return out
}

func memberMap(members []config.Member) map[string]config.Member {
	out := map[string]config.Member{}
	for _, member := range members {
		out[member.PublicKeySHA] = member
	}
	return out
}

func memberLabel(handle, sha string) string {
	if handle == "" {
		return sha
	}
	return handle + " (" + sha + ")"
}

func addedScopes(local, pulled []config.ScopeSummary) []string {
	localByName := scopeMap(local)
	var out []string
	for _, scope := range pulled {
		if _, ok := localByName[scope.Name]; !ok {
			out = append(out, scope.Name)
		}
	}
	sort.Strings(out)
	return out
}

func removedScopes(local, pulled []config.ScopeSummary) []string {
	pulledByName := scopeMap(pulled)
	var out []string
	for _, scope := range local {
		if _, ok := pulledByName[scope.Name]; !ok {
			out = append(out, scope.Name)
		}
	}
	sort.Strings(out)
	return out
}

func changedScopes(local, pulled []config.ScopeSummary) []string {
	localByName := scopeMap(local)
	var out []string
	for _, scope := range pulled {
		if previous, ok := localByName[scope.Name]; ok && !reflect.DeepEqual(previous, scope) {
			out = append(out, scope.Name)
		}
	}
	sort.Strings(out)
	return out
}

func scopeMap(scopes []config.ScopeSummary) map[string]config.ScopeSummary {
	out := map[string]config.ScopeSummary{}
	for _, scope := range scopes {
		out[scope.Name] = scope
	}
	return out
}

func renderConfigPullResult(w io.Writer, jsonOutput bool, noColor bool, result ConfigPullResult) {
	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}
	style := newOutputStyle(noColor)
	renderCommandTitle(w, style, "Propagate config pull", result.DryRun)
	switch result.Status {
	case "dry_run":
		renderNote(w, style, "Config pull dry run complete.")
	case "no_change":
		renderNote(w, style, "Config already matches the cloud.")
	default:
		renderOK(w, style, "Config pull complete.")
	}
	fmt.Fprintln(w)
	if result.TeamName != "" {
		fmt.Fprintf(w, "Team: %s (%s)\n", result.TeamName, result.TeamID)
	}
	if result.Identity != nil {
		fmt.Fprintf(w, "Pulled by: %s (%s)\n", result.Identity.Handle, result.Identity.PublicKeySHA)
	}
	if result.OldRevision != "" || result.NewRevision != "" {
		fmt.Fprintf(w, "Revision: %s -> %s\n", valueOrDash(result.OldRevision), valueOrDash(result.NewRevision))
	}
	fmt.Fprintf(w, "Would update: %t\n", result.WouldUpdate)
	fmt.Fprintf(w, "Updated: %t\n", result.Updated)
	fmt.Fprintf(w, "Would overwrite local changes: %t\n", result.WouldOverwrite)
	fmt.Fprintf(w, "Backend: %s\n", result.BackendStatus)
	renderConfigPullChanges(w, style, result.Changes)
	renderWarnings(w, style, result.Warnings)
	renderNextSteps(w, style, result.NextSteps)
}

func renderConfigPullChanges(w io.Writer, style outputStyle, changes ConfigPullChangeSummary) {
	lines := configPullChangeLines(changes)
	if len(lines) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s\n", style.bold("Changes:"))
	for _, line := range lines {
		fmt.Fprintf(w, "- %s\n", line)
	}
}

func configPullChangeLines(changes ConfigPullChangeSummary) []string {
	var lines []string
	if changes.TeamNameChanged {
		lines = append(lines, "team name changed")
	}
	addList := func(label string, values []string) {
		if len(values) > 0 {
			lines = append(lines, label+": "+strings.Join(values, ", "))
		}
	}
	addList("members added", changes.MembersAdded)
	addList("members removed", changes.MembersRemoved)
	addList("members changed", changes.MembersChanged)
	addList("scopes added", changes.ScopesAdded)
	addList("scopes removed", changes.ScopesRemoved)
	addList("scopes changed", changes.ScopesChanged)
	return lines
}

func printConfigPullHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate config pull [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --dry-run           show what would change without writing")
	fmt.Fprintln(w, "  --yes               confirm overwriting local config changes")
	fmt.Fprintln(w, "  --api-url VALUE     override Propagate API URL")
	fmt.Fprintln(w, "  --json              render machine-readable JSON")
	fmt.Fprintln(w, "  --non-interactive   fail instead of prompting")
}

package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"propagate/cli/internal/apiclient"
	"propagate/cli/internal/config"
	"propagate/cli/internal/envfile"
	"propagate/cli/internal/gitutil"
	"propagate/cli/internal/identity"
	"propagate/cli/internal/secretcrypto"
)

type envPullOptions struct {
	globalOptions
	Scope  string
	DryRun bool
	Yes    bool
}

type EnvPullResult struct {
	OK                      bool              `json:"ok"`
	Command                 string            `json:"command"`
	Status                  string            `json:"status"`
	DryRun                  bool              `json:"dry_run"`
	Scope                   string            `json:"scope"`
	ProjectConfigPath       string            `json:"project_config_path"`
	TeamID                  string            `json:"team_id"`
	TeamName                string            `json:"team_name"`
	ConfigRevision          string            `json:"config_revision,omitempty"`
	Identity                *identity.Summary `json:"identity,omitempty"`
	Files                   []EnvPullFile     `json:"files"`
	VariablesPulledCount    int               `json:"variables_pulled_count"`
	VariablesWrittenCount   int               `json:"variables_written_count"`
	VariablesPreservedCount int               `json:"variables_preserved_count"`
	ConflictsResolvedCount  int               `json:"conflicts_resolved_count"`
	PullEventRecorded       bool              `json:"pull_event_recorded"`
	BackendStatus           string            `json:"backend_status"`
	Warnings                []string          `json:"warnings,omitempty"`
	NextSteps               []string          `json:"next_steps,omitempty"`
}

type EnvPullFile struct {
	Path                  string `json:"path"`
	Created               bool   `json:"created,omitempty"`
	Updated               bool   `json:"updated"`
	WouldUpdate           bool   `json:"would_update,omitempty"`
	VariablesAdded        int    `json:"variables_added"`
	VariablesUpdated      int    `json:"variables_updated"`
	VariablesUnchanged    int    `json:"variables_unchanged"`
	VariablesPreserved    int    `json:"variables_preserved"`
	ManagedVariablesCount int    `json:"managed_variables_count"`
	VariablesWrittenCount int    `json:"variables_written_count"`
}

type envPullPlan struct {
	resultFile EnvPullFile
	absPath    string
	content    []byte
	perm       os.FileMode
	changed    bool
}

func runEnvCommand(args []string, global globalOptions, streams Streams) int {
	if len(args) == 0 {
		printEnvHelp(streams.Out)
		return ExitSuccess
	}
	switch args[0] {
	case "pull":
		return runEnvPullCommand(args[1:], global, streams)
	case "push":
		return runEnvPushCommand(args[1:], global, streams)
	case "set":
		return runEnvSetCommand(args[1:], global, streams)
	case "status":
		return runEnvStatusCommand(args[1:], global, streams)
	case "help", "--help", "-h":
		printEnvHelp(streams.Out)
		return ExitSuccess
	default:
		err := commandError(ExitUsageError, "usage_error", fmt.Sprintf("Unknown env command %q", args[0]), nil, "Run `propagate env help` to see available env commands.")
		return renderError(streams.Err, global.JSON, global.NoColor, err)
	}
}

func runEnvPullCommand(args []string, global globalOptions, streams Streams) int {
	opts := envPullOptions{globalOptions: global, Scope: "dev"}
	fs := flag.NewFlagSet("env pull", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &opts.globalOptions)
	fs.StringVar(&opts.Scope, "scope", "dev", "scope to pull")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "decrypt and show the merge plan without writing env files")
	fs.BoolVar(&opts.Yes, "yes", false, "confirm overwrites, missing file creation, and prod writes where safe")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printEnvPullHelp(streams.Out)
			return ExitSuccess
		}
		cmdErr := commandError(ExitUsageError, "usage_error", "Invalid env pull flags", err, "Run `propagate env pull --help` for usage.")
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if fs.NArg() != 0 {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate env pull does not accept positional arguments", nil)
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}

	result, err := runEnvPull(opts, streams)
	if err != nil {
		return renderError(streams.Err, opts.JSON, opts.NoColor, err)
	}
	renderEnvPullResult(streams.Out, opts.JSON, opts.NoColor, result)
	return ExitSuccess
}

func runEnvPull(opts envPullOptions, streams Streams) (EnvPullResult, error) {
	reader := bufio.NewReader(streams.In)
	scopeName := strings.TrimSpace(opts.Scope)
	if scopeName == "" {
		scopeName = "dev"
	}
	if err := config.ValidateScopeName(scopeName); err != nil {
		return EnvPullResult{}, commandError(ExitUsageError, "usage_error", "Invalid env pull scope", err)
	}

	result := EnvPullResult{
		OK:            true,
		Command:       "env pull",
		Status:        "success",
		DryRun:        opts.DryRun,
		Scope:         scopeName,
		BackendStatus: "not_contacted",
	}

	ident, err := identity.Load()
	if err != nil {
		return EnvPullResult{}, commandError(ExitValidationError, "identity_missing", "Cannot load local Propagate identity for signed env pull", err, "Run `propagate init` to create or repair the local identity.")
	}
	summary := ident.Summary()
	result.Identity = &summary

	worktree, err := gitutil.Discover(streams.WorkDir)
	if err != nil {
		return EnvPullResult{}, commandError(ExitValidationError, "not_git_repo", "Cannot pull env values outside a Git worktree", err)
	}
	configPath, exists, err := config.ExistingPath(worktree.Root)
	if err != nil {
		return EnvPullResult{}, commandError(ExitValidationError, "config_invalid", "Existing Propagate config needs attention", err, "Rename `propagate.yml` to `propagate.yaml` before running env pull again.")
	}
	if !exists {
		return EnvPullResult{}, commandError(ExitValidationError, "config_missing", "propagate.yaml is required before env pull", nil, "Run `propagate init` or pull the repository config first.")
	}
	result.ProjectConfigPath = configPath

	project, err := config.ReadProject(configPath)
	if err != nil {
		return EnvPullResult{}, commandError(ExitValidationError, "config_invalid", "Cannot read propagate.yaml", err)
	}
	result.TeamID = project.TeamID
	result.TeamName = project.TeamName

	localScope := findScopeSummary(project.Scopes, scopeName)
	if localScope == nil {
		return EnvPullResult{}, commandError(ExitValidationError, "scope_not_found", fmt.Sprintf("Scope %q is not configured in propagate.yaml", scopeName), nil, "Run `propagate config pull` if the scope was added in the cloud.")
	}

	apiURL := resolveAPIURL(opts.APIURL, streams.WorkDir)
	if apiURL == "" {
		return EnvPullResult{}, commandError(ExitCloudUnavailable, "cloud_unavailable", "Propagate API URL is required for env pull", nil, "Pass `--api-url` or set PROPAGATE_API_URL.")
	}
	client := apiclient.Client{BaseURL: apiURL, HTTPClient: configPushHTTPClient, CLIVersion: Version}
	bundle, err := client.PullBundle(context.Background(), ident, project.TeamID, scopeName)
	if err != nil {
		return EnvPullResult{}, mapEnvPullAPIError(err, scopeName, summary)
	}
	result.BackendStatus = "fetched"
	result.ConfigRevision = bundle.ConfigRevision

	if bundle.Scope.Name != "" && bundle.Scope.Name != scopeName {
		return EnvPullResult{}, commandError(ExitValidationError, "validation_failed", "Cloud returned a pull bundle for a different scope", fmt.Errorf("requested %s, received %s", scopeName, bundle.Scope.Name))
	}
	if bundle.ScopeKeyEnvelope.RecipientKeySHA != "" && bundle.ScopeKeyEnvelope.RecipientKeySHA != ident.PublicKeySHA {
		return EnvPullResult{}, commandError(ExitPermissionDenied, "permission_denied", fmt.Sprintf("No readable scope key envelope was returned for scope %q", scopeName), nil, "Ask a Propagate management member to approve access and run `propagate config push`.")
	}

	scopeKey, err := secretcrypto.DecryptScopeKey(
		ident.EncryptionPrivateKey,
		bundle.ScopeKeyEnvelope.EncryptedScopeKey,
		bundle.ScopeKeyEnvelope.Algorithm,
		scopeName,
		ident.PublicKeySHA,
		bundle.ScopeKeyEnvelope.ScopeKeyVersion,
	)
	if err != nil {
		return EnvPullResult{}, commandError(ExitPermissionDenied, "scope_key_decrypt_failed", fmt.Sprintf("Cannot decrypt the scope key envelope for scope %q", scopeName), err, "No values were written.", "Ask a Propagate management member to refresh your access envelope.")
	}

	valuesByFile, err := decryptPullValues(project.TeamID, scopeName, scopeKey, bundle.SecretVersions)
	if err != nil {
		return EnvPullResult{}, commandError(ExitValidationError, "decrypt_failed", "Cannot decrypt one or more pulled env values", err, "No values were written.")
	}
	result.VariablesPulledCount = len(bundle.SecretVersions)

	filePaths := pullFilePaths(localScope.EnvFiles, bundle.EnvFileMappings, valuesByFile)
	plans, err := buildEnvPullPlans(worktree.Root, filePaths, valuesByFile)
	if err != nil {
		return EnvPullResult{}, err
	}
	for _, plan := range plans {
		file := plan.resultFile
		if !opts.DryRun {
			file.Updated = plan.changed
			file.WouldUpdate = false
		}
		result.Files = append(result.Files, file)
		result.VariablesWrittenCount += file.VariablesWrittenCount
		result.VariablesPreservedCount += file.VariablesPreserved
		result.ConflictsResolvedCount += file.VariablesUpdated
	}

	if opts.DryRun {
		result.Status = "dry_run"
		result.BackendStatus = "validated"
		result.NextSteps = []string{"Re-run without `--dry-run` after reviewing the file plan."}
		return result, nil
	}

	if err := confirmEnvPullWrites(opts, reader, streams.In, streams.Out, scopeName, plans); err != nil {
		return EnvPullResult{}, err
	}

	changed := false
	for _, plan := range plans {
		if !plan.changed {
			continue
		}
		if err := envfile.WriteMerged(plan.absPath, plan.content, plan.perm); err != nil {
			return EnvPullResult{}, commandError(ExitPartialLocalFailure, "partial_local_failure", "Env pull could not update one or more local env files", err)
		}
		changed = true
	}
	if !changed {
		result.Status = "no_change"
		result.BackendStatus = "up_to_date"
	}

	event, err := client.RecordPullEvent(context.Background(), ident, project.TeamID, apiclient.PullEventRequest{
		Scope:          scopeName,
		EnvFilePaths:   filePathsWithValues(filePaths, valuesByFile),
		ConfigRevision: bundle.ConfigRevision,
		VariablesCount: len(bundle.SecretVersions),
		Client:         apiclient.ClientMetadata{CLIVersion: Version, ClientKind: "cli"},
	})
	if err != nil {
		result.Warnings = append(result.Warnings, "Pull event could not be recorded: "+safeAPIError(err))
	} else {
		result.PullEventRecorded = event.RecordedCount > 0
	}
	if len(result.NextSteps) == 0 {
		result.NextSteps = []string{"Review local env file changes before running dependent services."}
	}
	return result, nil
}

func decryptPullValues(teamID string, scopeName string, scopeKey []byte, records []apiclient.SecretVersionRecord) (map[string]map[string]string, error) {
	valuesByFile := map[string]map[string]string{}
	for _, record := range records {
		if record.ScopeKeyVersion <= 0 {
			return nil, fmt.Errorf("%s in %s has invalid scope key version", record.Name, record.EnvFilePath)
		}
		value, err := secretcrypto.DecryptValue(
			scopeKey,
			teamID,
			scopeName,
			record.EnvFilePath,
			record.Name,
			record.ScopeKeyVersion,
			record.Ciphertext,
			record.Nonce,
			record.Algorithm,
		)
		if err != nil {
			return nil, fmt.Errorf("%s in %s: %w", record.Name, record.EnvFilePath, err)
		}
		if valuesByFile[record.EnvFilePath] == nil {
			valuesByFile[record.EnvFilePath] = map[string]string{}
		}
		if _, exists := valuesByFile[record.EnvFilePath][record.Name]; exists {
			return nil, fmt.Errorf("cloud returned duplicate variable %s for %s", record.Name, record.EnvFilePath)
		}
		valuesByFile[record.EnvFilePath][record.Name] = value
	}
	return valuesByFile, nil
}

func buildEnvPullPlans(root string, filePaths []string, valuesByFile map[string]map[string]string) ([]envPullPlan, error) {
	plans := make([]envPullPlan, 0, len(filePaths))
	for _, rel := range filePaths {
		absPath, err := repoFilePath(root, rel)
		if err != nil {
			return nil, commandError(ExitValidationError, "validation_failed", "Cloud pull bundle referenced an invalid env file path", err)
		}
		values := valuesByFile[rel]
		if values == nil {
			values = map[string]string{}
		}
		var existing []byte
		exists := true
		perm := os.FileMode(0o600)
		info, statErr := os.Stat(absPath)
		switch {
		case statErr == nil:
			if info.IsDir() {
				return nil, commandError(ExitValidationError, "env_file_invalid", "Configured env file path is a directory", fmt.Errorf("%s", rel))
			}
			perm = info.Mode().Perm()
			data, err := os.ReadFile(absPath)
			if err != nil {
				return nil, commandError(ExitValidationError, "env_file_read_failed", "Cannot read an existing env file before merge", err)
			}
			existing = data
		case errors.Is(statErr, os.ErrNotExist):
			exists = false
		default:
			return nil, commandError(ExitValidationError, "env_file_read_failed", "Cannot inspect an env file before merge", statErr)
		}
		merge, err := envfile.PlanMerge(existing, exists, values)
		if err != nil {
			return nil, commandError(ExitValidationError, "env_merge_conflict", "Cannot safely merge pulled values into a local env file", err, "No values were written.")
		}
		file := EnvPullFile{
			Path:                  rel,
			Created:               merge.Created,
			Updated:               false,
			WouldUpdate:           merge.Changed,
			VariablesAdded:        merge.VariablesAdded,
			VariablesUpdated:      merge.VariablesUpdated,
			VariablesUnchanged:    merge.VariablesUnchanged,
			VariablesPreserved:    merge.VariablesPreserved,
			ManagedVariablesCount: merge.ManagedVariables(),
			VariablesWrittenCount: merge.VariablesWritten(),
		}
		plans = append(plans, envPullPlan{
			resultFile: file,
			absPath:    absPath,
			content:    merge.Content,
			perm:       perm,
			changed:    merge.Changed,
		})
	}
	return plans, nil
}

func confirmEnvPullWrites(opts envPullOptions, reader *bufio.Reader, in io.Reader, out io.Writer, scopeName string, plans []envPullPlan) error {
	changed := false
	creates := 0
	overwrites := 0
	for _, plan := range plans {
		if plan.changed {
			changed = true
		}
		if plan.resultFile.Created {
			creates++
		}
		overwrites += plan.resultFile.VariablesUpdated
	}
	if !changed {
		return nil
	}
	if scopeName == "prod" && !opts.Yes {
		if opts.NonInteractive {
			return commandError(ExitConfirmationRequired, "confirmation_required", "Non-interactive prod env pull requires --yes before writing local files", nil, "Re-run with `--yes` after reviewing `propagate env pull --scope prod --dry-run`.")
		}
		ok, err := promptConfirm(reader, in, out, "Pull prod env values into local files?", false)
		if err != nil {
			return err
		}
		if !ok {
			return commandError(ExitUserCanceled, "user_canceled", "Env pull was canceled before writing local files", nil)
		}
	}
	if (creates > 0 || overwrites > 0) && !opts.Yes {
		if opts.NonInteractive {
			return commandError(ExitConfirmationRequired, "confirmation_required", "Non-interactive env pull requires --yes before creating files or overwriting existing values", nil, "Re-run with `--yes` after reviewing `propagate env pull --dry-run`.")
		}
		label := fmt.Sprintf("Apply env pull changes (%d new file(s), %d overwrite(s))?", creates, overwrites)
		ok, err := promptConfirm(reader, in, out, label, false)
		if err != nil {
			return err
		}
		if !ok {
			return commandError(ExitUserCanceled, "user_canceled", "Env pull was canceled before writing local files", nil)
		}
	}
	return nil
}

func findScopeSummary(scopes []config.ScopeSummary, name string) *config.ScopeSummary {
	for idx := range scopes {
		if scopes[idx].Name == name {
			return &scopes[idx]
		}
	}
	return nil
}

func pullFilePaths(local []string, cloud []string, valuesByFile map[string]map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(path string) {
		path = strings.TrimSpace(filepath.ToSlash(path))
		if filepath.ToSlash(filepath.Clean(path)) == "." || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	for _, path := range local {
		add(path)
	}
	for _, path := range cloud {
		add(path)
	}
	for path := range valuesByFile {
		add(path)
	}
	sort.Strings(out)
	return out
}

func filePathsWithValues(filePaths []string, valuesByFile map[string]map[string]string) []string {
	var out []string
	for _, path := range filePaths {
		if len(valuesByFile[path]) > 0 {
			out = append(out, path)
		}
	}
	return out
}

func repoFilePath(root string, rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", fmt.Errorf("env file path is empty")
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(rel) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("env file path must be repo-relative and inside the worktree: %s", rel)
	}
	return filepath.Join(root, clean), nil
}

func mapEnvPullAPIError(err error, scope string, ident identity.Summary) error {
	var apiErr *apiclient.APIError
	if !errors.As(err, &apiErr) {
		return commandError(ExitCloudUnavailable, "cloud_unavailable", "Cannot fetch encrypted env pull bundle", err)
	}
	switch apiErr.Code {
	case "permission_denied":
		return commandError(
			ExitPermissionDenied,
			apiErr.Code,
			fmt.Sprintf("Cannot pull env values for scope %q with identity %s", scope, ident.PublicKeySHA),
			apiErr,
			"No values were written.",
			"Commit a `propagate team join` request or ask a Propagate management member to grant read access for this scope.",
		)
	case "team_not_found", "scope_not_found":
		return commandError(ExitValidationError, apiErr.Code, "The requested team or scope was not found in the cloud", apiErr, "Run `propagate config pull` if the local config is stale.")
	case "validation_failed", "usage_error":
		return commandError(ExitValidationError, apiErr.Code, "Env pull request was rejected by the cloud", apiErr)
	default:
		code := ExitCloudUnavailable
		if apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 && !apiErr.Retryable {
			code = ExitValidationError
		}
		return commandError(code, apiErr.Code, "Cannot fetch encrypted env pull bundle", apiErr)
	}
}

func safeAPIError(err error) string {
	var apiErr *apiclient.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Code != "" {
			return apiErr.Code + ": " + apiErr.Message
		}
		return apiErr.Message
	}
	return err.Error()
}

func renderEnvPullResult(w io.Writer, jsonOutput bool, noColor bool, result EnvPullResult) {
	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}
	style := newOutputStyle(noColor)
	renderCommandTitle(w, style, "Propagate env pull", result.DryRun)
	switch result.Status {
	case "dry_run":
		renderNote(w, style, "Env pull dry run complete.")
	case "no_change":
		renderNote(w, style, "Env files already match the cloud.")
	default:
		renderOK(w, style, "Env pull complete.")
	}
	fmt.Fprintln(w)
	if result.TeamName != "" {
		fmt.Fprintf(w, "Team: %s (%s)\n", result.TeamName, result.TeamID)
	}
	fmt.Fprintf(w, "Scope: %s\n", result.Scope)
	if result.Identity != nil {
		fmt.Fprintf(w, "Pulled by: %s (%s)\n", result.Identity.Handle, result.Identity.PublicKeySHA)
	}
	if result.ConfigRevision != "" {
		fmt.Fprintf(w, "Config revision: %s\n", result.ConfigRevision)
	}
	if len(result.Files) > 0 {
		fmt.Fprintln(w, style.bold("Files:"))
		for _, file := range result.Files {
			action := "unchanged"
			if result.DryRun && file.WouldUpdate {
				action = "would update"
			} else if file.Updated || file.WouldUpdate && result.Status != "dry_run" {
				action = "updated"
			}
			if file.Created {
				action = "created"
				if result.DryRun {
					action = "would create"
				}
			}
			fmt.Fprintf(w, "  - %s: %s (%d added, %d overwritten, %d unchanged, %d preserved)\n", file.Path, action, file.VariablesAdded, file.VariablesUpdated, file.VariablesUnchanged, file.VariablesPreserved)
		}
	}
	fmt.Fprintf(w, "Variables pulled: %d\n", result.VariablesPulledCount)
	fmt.Fprintf(w, "Variables written: %d\n", result.VariablesWrittenCount)
	fmt.Fprintf(w, "Pull event recorded: %t\n", result.PullEventRecorded)
	fmt.Fprintf(w, "Backend: %s\n", result.BackendStatus)
	renderWarnings(w, style, result.Warnings)
	renderNextSteps(w, style, result.NextSteps)
}

func printEnvHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate env pull [flags]")
	fmt.Fprintln(w, "  propagate env push [flags]")
	fmt.Fprintln(w, "  propagate env set NAME [flags]")
	fmt.Fprintln(w, "  propagate env status [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  pull      fetch encrypted env values, decrypt locally, and merge env files")
	fmt.Fprintln(w, "  push      encrypt local env file changes and upload them")
	fmt.Fprintln(w, "  set       securely set one encrypted cloud variable")
	fmt.Fprintln(w, "  status    show encrypted cloud env metadata for a scope")
}

func printEnvPullHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate env pull [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --scope VALUE       scope to pull (default dev)")
	fmt.Fprintln(w, "  --dry-run           decrypt and show the merge plan without writing files")
	fmt.Fprintln(w, "  --yes               confirm overwrites, missing file creation, and prod writes")
	fmt.Fprintln(w, "  --api-url VALUE     override Propagate API URL")
	fmt.Fprintln(w, "  --json              render machine-readable JSON")
	fmt.Fprintln(w, "  --non-interactive   fail instead of prompting")
}

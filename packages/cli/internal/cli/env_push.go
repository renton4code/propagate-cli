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
	"sort"
	"strings"

	"propagate/cli/internal/apiclient"
	"propagate/cli/internal/config"
	"propagate/cli/internal/envfile"
	"propagate/cli/internal/gitutil"
	"propagate/cli/internal/identity"
	"propagate/cli/internal/secretcrypto"
)

type envPushOptions struct {
	globalOptions
	Scope  string
	DryRun bool
	Yes    bool
}

type EnvPushResult struct {
	OK                      bool              `json:"ok"`
	Command                 string            `json:"command"`
	Status                  string            `json:"status"`
	DryRun                  bool              `json:"dry_run"`
	Scope                   string            `json:"scope"`
	ProjectConfigPath       string            `json:"project_config_path"`
	TeamID                  string            `json:"team_id"`
	TeamName                string            `json:"team_name"`
	ConfigRevision          string            `json:"config_revision,omitempty"`
	NewConfigRevision       string            `json:"new_config_revision,omitempty"`
	ConfigHash              string            `json:"config_hash,omitempty"`
	OperationID             string            `json:"operation_id,omitempty"`
	Identity                *identity.Summary `json:"identity,omitempty"`
	Files                   []EnvPushFile     `json:"files"`
	Changes                 []EnvPushChange   `json:"changes,omitempty"`
	VariablesAddedCount     int               `json:"variables_added_count"`
	VariablesChangedCount   int               `json:"variables_changed_count"`
	VariablesRemovedCount   int               `json:"variables_removed_count"`
	VariablesUnchangedCount int               `json:"variables_unchanged_count"`
	CreatedVersionsCount    int               `json:"created_versions_count"`
	RemovedVariablesCount   int               `json:"removed_variables_count"`
	AuditEventsCount        int               `json:"audit_events_count"`
	BackendStatus           string            `json:"backend_status"`
	Warnings                []string          `json:"warnings,omitempty"`
	NextSteps               []string          `json:"next_steps,omitempty"`
}

type EnvPushFile struct {
	Path               string `json:"path"`
	VariablesAdded     int    `json:"variables_added"`
	VariablesChanged   int    `json:"variables_changed"`
	VariablesRemoved   int    `json:"variables_removed"`
	VariablesUnchanged int    `json:"variables_unchanged"`
	UnknownLineCount   int    `json:"unknown_line_count,omitempty"`
}

type EnvPushChange struct {
	Path   string `json:"path"`
	Name   string `json:"name"`
	Change string `json:"change"`
}

type envVarKey struct {
	Path string
	Name string
}

type cloudEnvValue struct {
	Value     string
	VersionID string
}

type envPushDiff struct {
	Added     []envVarKey
	Changed   []envVarKey
	Removed   []envVarKey
	Unchanged []envVarKey
}

func runEnvPushCommand(args []string, global globalOptions, streams Streams) int {
	opts := envPushOptions{globalOptions: global, Scope: "dev"}
	fs := flag.NewFlagSet("env push", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &opts.globalOptions)
	fs.StringVar(&opts.Scope, "scope", "dev", "scope to push")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "show encrypted upload plan without writing cloud state")
	fs.BoolVar(&opts.Yes, "yes", false, "confirm pushing additions, changes, and removals")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printEnvPushHelp(streams.Out)
			return ExitSuccess
		}
		cmdErr := commandError(ExitUsageError, "usage_error", "Invalid env push flags", err, "Run `propagate env push --help` for usage.")
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if fs.NArg() != 0 {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate env push does not accept positional arguments", nil)
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}

	result, err := runEnvPush(opts, streams)
	if err != nil {
		return renderError(streams.Err, opts.JSON, opts.NoColor, err)
	}
	renderEnvPushResult(streams.Out, opts.JSON, opts.NoColor, result)
	return ExitSuccess
}

func runEnvPush(opts envPushOptions, streams Streams) (EnvPushResult, error) {
	reader := bufio.NewReader(streams.In)
	scopeName := strings.TrimSpace(opts.Scope)
	if scopeName == "" {
		scopeName = "dev"
	}
	if err := config.ValidateScopeName(scopeName); err != nil {
		return EnvPushResult{}, commandError(ExitUsageError, "usage_error", "Invalid env push scope", err)
	}

	result := EnvPushResult{
		OK:            true,
		Command:       "env push",
		Status:        "success",
		DryRun:        opts.DryRun,
		Scope:         scopeName,
		BackendStatus: "not_contacted",
	}

	ident, err := identity.Load()
	if err != nil {
		return EnvPushResult{}, commandError(ExitValidationError, "identity_missing", "Cannot load local Propagate identity for signed env push", err, "Run `propagate init` to create or repair the local identity.")
	}
	summary := ident.Summary()
	result.Identity = &summary

	worktree, err := gitutil.Discover(streams.WorkDir)
	if err != nil {
		return EnvPushResult{}, commandError(ExitValidationError, "not_git_repo", "Cannot push env values outside a Git worktree", err)
	}
	configPath, exists, err := config.ExistingPath(worktree.Root)
	if err != nil {
		return EnvPushResult{}, commandError(ExitValidationError, "config_invalid", "Existing Propagate config needs attention", err, "Rename `propagate.yml` to `propagate.yaml` before running env push again.")
	}
	if !exists {
		return EnvPushResult{}, commandError(ExitValidationError, "config_missing", "propagate.yaml is required before env push", nil, "Run `propagate init` or pull the repository config first.")
	}
	result.ProjectConfigPath = configPath

	project, err := config.ReadProject(configPath)
	if err != nil {
		return EnvPushResult{}, commandError(ExitValidationError, "config_invalid", "Cannot read propagate.yaml", err)
	}
	result.TeamID = project.TeamID
	result.TeamName = project.TeamName

	localScope := findScopeSummary(project.Scopes, scopeName)
	if localScope == nil {
		return EnvPushResult{}, commandError(ExitValidationError, "scope_not_found", fmt.Sprintf("Scope %q is not configured in propagate.yaml", scopeName), nil, "Run `propagate config pull` if the scope was added in the cloud.")
	}
	if len(localScope.EnvFiles) == 0 {
		return EnvPushResult{}, commandError(ExitValidationError, "env_file_missing", fmt.Sprintf("Scope %q has no env_files configured", scopeName), nil, "Add an env file mapping to propagate.yaml before pushing values.")
	}

	localValues, files, warnings, err := readLocalEnvPushFiles(worktree.Root, localScope.EnvFiles)
	if err != nil {
		return EnvPushResult{}, err
	}
	result.Files = files
	result.Warnings = append(result.Warnings, warnings...)

	apiURL := resolveAPIURL(opts.APIURL, streams.WorkDir)
	if apiURL == "" {
		return EnvPushResult{}, commandError(ExitCloudUnavailable, "cloud_unavailable", "Propagate API URL is required for env push", nil, "Pass `--api-url` or set PROPAGATE_API_URL.")
	}
	client := apiclient.Client{BaseURL: apiURL, HTTPClient: configPushHTTPClient, CLIVersion: Version}
	bundle, err := client.PullBundle(context.Background(), ident, project.TeamID, scopeName)
	if err != nil {
		return EnvPushResult{}, mapEnvPushAPIError(err, scopeName, summary)
	}
	result.BackendStatus = "fetched"
	result.ConfigRevision = bundle.ConfigRevision
	if bundle.ConfigRevision != project.CloudRevision {
		return EnvPushResult{}, commandError(
			ExitConflict,
			"revision_conflict",
			"Cloud config revision differs from local propagate.yaml",
			nil,
			"Run `propagate config pull`, review the env file mappings, and retry env push.",
		)
	}
	if bundle.Scope.Name != "" && bundle.Scope.Name != scopeName {
		return EnvPushResult{}, commandError(ExitValidationError, "validation_failed", "Cloud returned a pull bundle for a different scope", fmt.Errorf("requested %s, received %s", scopeName, bundle.Scope.Name))
	}
	if bundle.ScopeKeyEnvelope.RecipientKeySHA != "" && bundle.ScopeKeyEnvelope.RecipientKeySHA != ident.PublicKeySHA {
		return EnvPushResult{}, commandError(ExitPermissionDenied, "permission_denied", fmt.Sprintf("No writable scope key envelope was returned for scope %q", scopeName), nil, "Ask a Propagate admin to approve access and run `propagate config push`.")
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
		return EnvPushResult{}, commandError(ExitPermissionDenied, "scope_key_decrypt_failed", fmt.Sprintf("Cannot decrypt the scope key envelope for scope %q", scopeName), err, "No values were uploaded.", "Ask a Propagate admin to refresh your access envelope.")
	}

	cloudValues, err := decryptCloudEnvState(project.TeamID, scopeName, scopeKey, bundle.SecretVersions)
	if err != nil {
		return EnvPushResult{}, commandError(ExitValidationError, "decrypt_failed", "Cannot decrypt current cloud env values for diffing", err, "No values were uploaded.")
	}
	targetProject := updateScopeDeclarationsFromLocalValues(project, scopeName, scopeKey, bundle.ScopeKeyEnvelope.ScopeKeyVersion, localValues)
	targetSnapshot, err := config.SnapshotJSON(targetProject)
	if err != nil {
		return EnvPushResult{}, commandError(ExitValidationError, "config_invalid", "Cannot build variable declarations for env push", err)
	}
	targetRendered, err := config.RenderParsed(targetProject)
	if err != nil {
		return EnvPushResult{}, commandError(ExitValidationError, "config_invalid", "Cannot render variable declarations for env push", err)
	}
	configMetadataChanged := targetRendered != project.Raw

	diff := diffEnvValues(localValues, cloudValues, localScope.EnvFiles)
	applyEnvPushDiffToResult(&result, diff)
	result.Changes = pushChanges(diff)
	result.Files = applyEnvPushDiffToFiles(result.Files, diff)

	if result.VariablesAddedCount+result.VariablesChangedCount+result.VariablesRemovedCount == 0 && !configMetadataChanged {
		result.Status = "no_change"
		result.BackendStatus = "up_to_date"
		result.NextSteps = []string{"No env push is needed."}
		return result, nil
	}
	if opts.DryRun {
		result.Status = "dry_run"
		result.BackendStatus = "validated"
		result.NextSteps = []string{"Re-run without `--dry-run` and with `--yes` after reviewing the change summary."}
		return result, nil
	}
	if err := confirmEnvPush(opts, reader, streams.In, streams.Out, result); err != nil {
		return EnvPushResult{}, err
	}

	upserts, removals, err := buildEnvPushPayload(project.TeamID, scopeName, scopeKey, bundle.ScopeKeyEnvelope.ScopeKeyVersion, localValues, cloudValues, diff)
	if err != nil {
		return EnvPushResult{}, commandError(ExitInternalError, "internal_error", "Cannot encrypt env push payload", err)
	}
	operationID, err := operationID("env_push")
	if err != nil {
		return EnvPushResult{}, commandError(ExitInternalError, "internal_error", "Cannot create operation ID", err)
	}
	result.OperationID = operationID
	pushResult, err := client.EnvPush(context.Background(), ident, project.TeamID, scopeName, apiclient.EnvPushRequest{
		OperationID:            operationID,
		ExpectedConfigRevision: bundle.ConfigRevision,
		TargetConfigSnapshot:   json.RawMessage(targetSnapshot),
		Upserts:                upserts,
		Removals:               removals,
		SafeCounts: apiclient.SafeCounts{
			Added:   result.VariablesAddedCount,
			Changed: result.VariablesChangedCount,
			Removed: result.VariablesRemovedCount,
		},
		Client: apiclient.ClientMetadata{CLIVersion: Version, ClientKind: "cli"},
	})
	if err != nil {
		return EnvPushResult{}, mapEnvPushAPIError(err, scopeName, summary)
	}
	result.CreatedVersionsCount = len(pushResult.CreatedVersions)
	result.RemovedVariablesCount = len(pushResult.RemovedVariables)
	result.AuditEventsCount = pushResult.AuditEventsCount
	result.NewConfigRevision = pushResult.ConfigRevision
	result.ConfigHash = pushResult.ConfigHash
	if pushResult.ConfigRevision != "" {
		targetProject.CloudRevision = pushResult.ConfigRevision
		targetProject.SyncStatus = "synced"
		rendered, err := config.RenderParsed(targetProject)
		if err != nil {
			return EnvPushResult{}, commandError(ExitValidationError, "config_invalid", "Cloud accepted env push but local config could not be rendered", err, "Run `propagate config pull` to recover the accepted config.")
		}
		if err := config.WriteRaw(configPath, rendered); err != nil {
			return EnvPushResult{}, commandError(ExitPartialLocalFailure, "partial_local_failure", "Cloud accepted env push but propagate.yaml could not be updated", err, "Run `propagate config pull` to recover the accepted config.")
		}
	}
	result.BackendStatus = "pushed"
	result.NextSteps = []string{"Ask teammates to run `propagate env pull` when they need the updated values."}
	return result, nil
}

func readLocalEnvPushFiles(root string, paths []string) (map[envVarKey]string, []EnvPushFile, []string, error) {
	values := map[envVarKey]string{}
	var files []EnvPushFile
	var warnings []string
	for _, rel := range paths {
		absPath, err := repoFilePath(root, rel)
		if err != nil {
			return nil, nil, nil, commandError(ExitValidationError, "validation_failed", "Configured env file path is invalid", err)
		}
		info, err := os.Stat(absPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, nil, nil, commandError(ExitValidationError, "env_file_missing", fmt.Sprintf("Configured env file %s is missing", rel), nil, "No values were uploaded.")
			}
			return nil, nil, nil, commandError(ExitValidationError, "env_file_read_failed", "Cannot inspect a configured env file", err)
		}
		if info.IsDir() {
			return nil, nil, nil, commandError(ExitValidationError, "env_file_invalid", "Configured env file path is a directory", fmt.Errorf("%s", rel))
		}
		parsed, err := envfile.ParseAssignments(absPath)
		if err != nil {
			return nil, nil, nil, commandError(ExitValidationError, "env_parse_error", "Cannot parse a configured env file", err)
		}
		if len(parsed.DuplicateVariables) > 0 {
			return nil, nil, nil, commandError(ExitValidationError, "env_parse_error", fmt.Sprintf("%s contains duplicate variables: %s", rel, strings.Join(parsed.DuplicateVariables, ", ")), nil, "Resolve duplicates before pushing so Propagate knows which value to encrypt.")
		}
		if parsed.UnknownLineCount > 0 {
			warnings = append(warnings, fmt.Sprintf("%s contains %d line(s) Propagate ignored because they are not simple assignments", rel, parsed.UnknownLineCount))
		}
		for name, value := range parsed.Values {
			values[envVarKey{Path: rel, Name: name}] = value
		}
		files = append(files, EnvPushFile{Path: rel, UnknownLineCount: parsed.UnknownLineCount})
	}
	return values, files, warnings, nil
}

func decryptCloudEnvState(teamID string, scopeName string, scopeKey []byte, records []apiclient.SecretVersionRecord) (map[envVarKey]cloudEnvValue, error) {
	values := map[envVarKey]cloudEnvValue{}
	for _, record := range records {
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
		key := envVarKey{Path: record.EnvFilePath, Name: record.Name}
		if _, exists := values[key]; exists {
			return nil, fmt.Errorf("cloud returned duplicate variable %s for %s", record.Name, record.EnvFilePath)
		}
		values[key] = cloudEnvValue{Value: value, VersionID: record.CurrentVersionID}
	}
	return values, nil
}

func diffEnvValues(local map[envVarKey]string, cloud map[envVarKey]cloudEnvValue, configuredFiles []string) envPushDiff {
	configured := map[string]bool{}
	for _, file := range configuredFiles {
		configured[file] = true
	}
	var diff envPushDiff
	for _, key := range sortedLocalKeys(local) {
		current, exists := cloud[key]
		switch {
		case !exists:
			diff.Added = append(diff.Added, key)
		case current.Value != local[key]:
			diff.Changed = append(diff.Changed, key)
		default:
			diff.Unchanged = append(diff.Unchanged, key)
		}
	}
	for _, key := range sortedCloudKeys(cloud) {
		if !configured[key.Path] {
			continue
		}
		if _, exists := local[key]; !exists {
			diff.Removed = append(diff.Removed, key)
		}
	}
	return diff
}

func applyEnvPushDiffToResult(result *EnvPushResult, diff envPushDiff) {
	result.VariablesAddedCount = len(diff.Added)
	result.VariablesChangedCount = len(diff.Changed)
	result.VariablesRemovedCount = len(diff.Removed)
	result.VariablesUnchangedCount = len(diff.Unchanged)
}

func applyEnvPushDiffToFiles(files []EnvPushFile, diff envPushDiff) []EnvPushFile {
	byPath := map[string]*EnvPushFile{}
	for idx := range files {
		byPath[files[idx].Path] = &files[idx]
	}
	for _, key := range diff.Added {
		if file := byPath[key.Path]; file != nil {
			file.VariablesAdded++
		}
	}
	for _, key := range diff.Changed {
		if file := byPath[key.Path]; file != nil {
			file.VariablesChanged++
		}
	}
	for _, key := range diff.Removed {
		if file := byPath[key.Path]; file != nil {
			file.VariablesRemoved++
		}
	}
	for _, key := range diff.Unchanged {
		if file := byPath[key.Path]; file != nil {
			file.VariablesUnchanged++
		}
	}
	return files
}

func pushChanges(diff envPushDiff) []EnvPushChange {
	var changes []EnvPushChange
	appendChanges := func(keys []envVarKey, kind string) {
		for _, key := range keys {
			changes = append(changes, EnvPushChange{Path: key.Path, Name: key.Name, Change: kind})
		}
	}
	appendChanges(diff.Added, "added")
	appendChanges(diff.Changed, "changed")
	appendChanges(diff.Removed, "removed")
	return changes
}

func buildEnvPushPayload(teamID string, scopeName string, scopeKey []byte, scopeKeyVersion int, local map[envVarKey]string, cloud map[envVarKey]cloudEnvValue, diff envPushDiff) ([]apiclient.EnvPushUpsert, []apiclient.EnvPushRemoval, error) {
	var upserts []apiclient.EnvPushUpsert
	for _, key := range append(append([]envVarKey{}, diff.Added...), diff.Changed...) {
		ciphertext, nonce, err := secretcrypto.EncryptValue(scopeKey, teamID, scopeName, key.Path, key.Name, scopeKeyVersion, local[key])
		if err != nil {
			return nil, nil, err
		}
		upsert := apiclient.EnvPushUpsert{
			EnvFilePath:     key.Path,
			Name:            key.Name,
			Ciphertext:      ciphertext,
			Nonce:           nonce,
			Algorithm:       secretcrypto.ValueAlgorithm,
			ScopeKeyVersion: scopeKeyVersion,
		}
		if current, ok := cloud[key]; ok {
			upsert.ExpectedVersionID = current.VersionID
		}
		upserts = append(upserts, upsert)
	}
	var removals []apiclient.EnvPushRemoval
	for _, key := range diff.Removed {
		removals = append(removals, apiclient.EnvPushRemoval{
			EnvFilePath:       key.Path,
			Name:              key.Name,
			ExpectedVersionID: cloud[key].VersionID,
		})
	}
	return upserts, removals, nil
}

func confirmEnvPush(opts envPushOptions, reader *bufio.Reader, in io.Reader, out io.Writer, result EnvPushResult) error {
	if opts.Yes {
		return nil
	}
	if opts.NonInteractive {
		return commandError(ExitConfirmationRequired, "confirmation_required", "Non-interactive env push requires --yes before uploading encrypted changes", nil, "Re-run with `--yes` after reviewing `propagate env push --dry-run`.")
	}
	label := fmt.Sprintf(
		"Push env changes to %s (%d added, %d changed, %d removed)?",
		result.Scope,
		result.VariablesAddedCount,
		result.VariablesChangedCount,
		result.VariablesRemovedCount,
	)
	ok, err := promptConfirm(reader, in, out, label, false)
	if err != nil {
		return err
	}
	if !ok {
		return commandError(ExitUserCanceled, "user_canceled", "Env push was canceled before upload", nil)
	}
	return nil
}

func mapEnvPushAPIError(err error, scope string, ident identity.Summary) error {
	var apiErr *apiclient.APIError
	if !errors.As(err, &apiErr) {
		return commandError(ExitCloudUnavailable, "cloud_unavailable", "Cannot push encrypted env changes", err)
	}
	switch apiErr.Code {
	case "permission_denied":
		return commandError(
			ExitPermissionDenied,
			apiErr.Code,
			fmt.Sprintf("Cannot push env values for scope %q with identity %s", scope, ident.PublicKeySHA),
			apiErr,
			"No values were uploaded.",
			"Ask a Propagate admin to grant write access for this scope.",
		)
	case "revision_conflict":
		return commandError(ExitConflict, apiErr.Code, "Cloud config revision changed before env push", apiErr, "Run `propagate config pull`, review the env file mappings, and retry env push.")
	case "secret_version_conflict":
		return commandError(ExitConflict, apiErr.Code, "One or more env values changed in the cloud before this push", apiErr, "Run `propagate env pull`, review the local changes, and retry env push.")
	case "team_not_found", "scope_not_found":
		return commandError(ExitValidationError, apiErr.Code, "The requested team or scope was not found in the cloud", apiErr, "Run `propagate config pull` if the local config is stale.")
	case "validation_failed", "usage_error":
		return commandError(ExitValidationError, apiErr.Code, "Env push request was rejected by the cloud", apiErr)
	default:
		code := ExitCloudUnavailable
		if apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 && !apiErr.Retryable {
			code = ExitValidationError
		}
		return commandError(code, apiErr.Code, "Cannot push encrypted env changes", apiErr)
	}
}

func sortedLocalKeys(values map[envVarKey]string) []envVarKey {
	keys := make([]envVarKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sortEnvKeys(keys)
	return keys
}

func sortedCloudKeys(values map[envVarKey]cloudEnvValue) []envVarKey {
	keys := make([]envVarKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sortEnvKeys(keys)
	return keys
}

func sortEnvKeys(keys []envVarKey) {
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Path != keys[j].Path {
			return keys[i].Path < keys[j].Path
		}
		return keys[i].Name < keys[j].Name
	})
}

func renderEnvPushResult(w io.Writer, jsonOutput bool, noColor bool, result EnvPushResult) {
	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}
	style := newOutputStyle(noColor)
	renderCommandTitle(w, style, "Propagate env push", result.DryRun)
	switch result.Status {
	case "dry_run":
		renderNote(w, style, "Env push dry run complete.")
	case "no_change":
		renderNote(w, style, "Env files already match the cloud.")
	default:
		renderOK(w, style, "Env push complete.")
	}
	fmt.Fprintln(w)
	if result.TeamName != "" {
		fmt.Fprintf(w, "Team: %s (%s)\n", result.TeamName, result.TeamID)
	}
	fmt.Fprintf(w, "Scope: %s\n", result.Scope)
	if result.Identity != nil {
		fmt.Fprintf(w, "Pushed by: %s (%s)\n", result.Identity.Handle, result.Identity.PublicKeySHA)
	}
	if result.ConfigRevision != "" {
		fmt.Fprintf(w, "Config revision: %s\n", result.ConfigRevision)
	}
	if result.OperationID != "" {
		fmt.Fprintf(w, "Operation: %s\n", result.OperationID)
	}
	if len(result.Files) > 0 {
		fmt.Fprintln(w, style.bold("Files:"))
		for _, file := range result.Files {
			fmt.Fprintf(w, "  - %s: %d added, %d changed, %d removed, %d unchanged\n", file.Path, file.VariablesAdded, file.VariablesChanged, file.VariablesRemoved, file.VariablesUnchanged)
		}
	}
	fmt.Fprintf(w, "Variables added: %d\n", result.VariablesAddedCount)
	fmt.Fprintf(w, "Variables changed: %d\n", result.VariablesChangedCount)
	fmt.Fprintf(w, "Variables removed: %d\n", result.VariablesRemovedCount)
	fmt.Fprintf(w, "Variables unchanged: %d\n", result.VariablesUnchangedCount)
	fmt.Fprintf(w, "Encrypted versions uploaded: %d\n", result.CreatedVersionsCount)
	fmt.Fprintf(w, "Removed variables uploaded: %d\n", result.RemovedVariablesCount)
	fmt.Fprintf(w, "Backend: %s\n", result.BackendStatus)
	if len(result.Changes) > 0 {
		fmt.Fprintln(w, style.bold("Changes:"))
		for _, change := range result.Changes {
			fmt.Fprintf(w, "  - %s %s in %s\n", change.Change, change.Name, change.Path)
		}
	}
	renderWarnings(w, style, result.Warnings)
	renderNextSteps(w, style, result.NextSteps)
}

func printEnvPushHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate env push [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --scope VALUE       scope to push (default dev)")
	fmt.Fprintln(w, "  --dry-run           show encrypted upload plan without writing cloud state")
	fmt.Fprintln(w, "  --yes               confirm pushing additions, changes, and removals")
	fmt.Fprintln(w, "  --api-url VALUE     override Propagate API URL")
	fmt.Fprintln(w, "  --json              render machine-readable JSON")
	fmt.Fprintln(w, "  --non-interactive   fail instead of prompting")
}

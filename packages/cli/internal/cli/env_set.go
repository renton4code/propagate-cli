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
	"os/exec"
	"strings"

	"propagate/cli/internal/apiclient"
	"propagate/cli/internal/config"
	"propagate/cli/internal/envfile"
	"propagate/cli/internal/gitutil"
	"propagate/cli/internal/identity"
	"propagate/cli/internal/secretcrypto"
)

type envSetOptions struct {
	globalOptions
	Scope  string
	DryRun bool
	Yes    bool
}

type EnvSetResult struct {
	OK                bool              `json:"ok"`
	Command           string            `json:"command"`
	Status            string            `json:"status"`
	DryRun            bool              `json:"dry_run"`
	Scope             string            `json:"scope"`
	Variable          string            `json:"variable"`
	ChangeType        string            `json:"change_type"`
	EnvFilePath       string            `json:"env_file_path,omitempty"`
	ProjectConfigPath string            `json:"project_config_path"`
	TeamID            string            `json:"team_id"`
	TeamName          string            `json:"team_name"`
	ConfigRevision    string            `json:"config_revision,omitempty"`
	OperationID       string            `json:"operation_id,omitempty"`
	Identity          *identity.Summary `json:"identity,omitempty"`
	NewVersionsCount  int               `json:"new_versions_count"`
	AuditEventsCount  int               `json:"audit_events_count"`
	BackendStatus     string            `json:"backend_status"`
	Warnings          []string          `json:"warnings,omitempty"`
	NextSteps         []string          `json:"next_steps,omitempty"`
}

func runEnvSetCommand(args []string, global globalOptions, streams Streams) int {
	opts := envSetOptions{globalOptions: global, Scope: "dev"}
	fs := flag.NewFlagSet("env set", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &opts.globalOptions)
	fs.StringVar(&opts.Scope, "scope", "dev", "scope to update")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "validate and show the single-value update plan without uploading")
	fs.BoolVar(&opts.Yes, "yes", false, "confirm uploading the encrypted value update")

	flagArgs, variable, showHelp, splitErr := splitEnvSetArgs(args)
	if splitErr != nil {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate env set requires exactly one variable name and never accepts the value as an argument", splitErr, "Run `propagate env set NAME --scope dev`; Propagate will prompt for the value.")
		return renderError(streams.Err, opts.JSON, cmdErr)
	}
	if showHelp {
		printEnvSetHelp(streams.Out)
		return ExitSuccess
	}
	if err := fs.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printEnvSetHelp(streams.Out)
			return ExitSuccess
		}
		cmdErr := commandError(ExitUsageError, "usage_error", "Invalid env set flags", err, "Run `propagate env set --help` for usage.")
		return renderError(streams.Err, opts.JSON, cmdErr)
	}
	if fs.NArg() != 0 || variable == "" {
		message := "propagate env set requires exactly one variable name and never accepts the value as an argument"
		next := "Run `propagate env set NAME --scope dev`; Propagate will prompt for the value."
		cmdErr := commandError(ExitUsageError, "usage_error", message, nil, next)
		return renderError(streams.Err, opts.JSON, cmdErr)
	}

	result, err := runEnvSet(opts, variable, streams)
	if err != nil {
		return renderError(streams.Err, opts.JSON, err)
	}
	renderEnvSetResult(streams.Out, opts.JSON, result)
	return ExitSuccess
}

func splitEnvSetArgs(args []string) ([]string, string, bool, error) {
	var flagArgs []string
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "help" || arg == "--help" || arg == "-h" {
			return nil, "", true, nil
		}
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
			if flagConsumesNext(arg) {
				if i+1 >= len(args) {
					return flagArgs, "", false, fmt.Errorf("%s requires a value", arg)
				}
				i++
				flagArgs = append(flagArgs, args[i])
			}
			continue
		}
		positionals = append(positionals, arg)
	}
	if len(positionals) > 1 {
		return flagArgs, "", false, fmt.Errorf("received %d positional arguments", len(positionals))
	}
	if len(positionals) == 0 {
		return flagArgs, "", false, nil
	}
	return flagArgs, positionals[0], false, nil
}

func flagConsumesNext(arg string) bool {
	name := strings.TrimLeft(arg, "-")
	if strings.Contains(name, "=") {
		return false
	}
	switch name {
	case "scope", "api-url":
		return true
	default:
		return false
	}
}

func runEnvSet(opts envSetOptions, variable string, streams Streams) (EnvSetResult, error) {
	reader := bufio.NewReader(streams.In)
	scopeName := strings.TrimSpace(opts.Scope)
	if scopeName == "" {
		scopeName = "dev"
	}
	if err := config.ValidateScopeName(scopeName); err != nil {
		return EnvSetResult{}, commandError(ExitUsageError, "usage_error", "Invalid env set scope", err)
	}
	variable = strings.TrimSpace(variable)
	if err := envfile.ValidateVariableName(variable); err != nil {
		return EnvSetResult{}, commandError(ExitUsageError, "usage_error", "Invalid env set variable name", err)
	}
	if opts.NonInteractive {
		return EnvSetResult{}, commandError(ExitConfirmationRequired, "confirmation_required", "env set requires an interactive value prompt", nil, "Run `propagate env set NAME` in an interactive terminal. `--yes` does not provide the value.")
	}
	if scopeName == "prod" && !opts.Yes {
		ok, err := promptConfirm(reader, streams.Out, "Set a prod env value in the encrypted cloud store?", false)
		if err != nil {
			return EnvSetResult{}, err
		}
		if !ok {
			return EnvSetResult{}, commandError(ExitUserCanceled, "user_canceled", "Env set was canceled before reading a value", nil)
		}
	}
	value, err := promptEnvSetValue(reader, streams.In, streams.Out, variable)
	if err != nil {
		return EnvSetResult{}, err
	}

	result := EnvSetResult{
		OK:            true,
		Command:       "env set",
		Status:        "success",
		DryRun:        opts.DryRun,
		Scope:         scopeName,
		Variable:      variable,
		BackendStatus: "not_contacted",
	}

	ident, err := identity.Load()
	if err != nil {
		return EnvSetResult{}, commandError(ExitValidationError, "identity_missing", "Cannot load local Propagate identity for signed env set", err, "Run `propagate init` to create or repair the local identity.")
	}
	summary := ident.Summary()
	result.Identity = &summary

	worktree, err := gitutil.Discover(streams.WorkDir)
	if err != nil {
		return EnvSetResult{}, commandError(ExitValidationError, "not_git_repo", "Cannot set env values outside a Git worktree", err)
	}
	configPath, exists, err := config.ExistingPath(worktree.Root)
	if err != nil {
		return EnvSetResult{}, commandError(ExitValidationError, "config_invalid", "Existing Propagate config needs attention", err, "Rename `propagate.yml` to `propagate.yaml` before running env set again.")
	}
	if !exists {
		return EnvSetResult{}, commandError(ExitValidationError, "config_missing", "propagate.yaml is required before env set", nil, "Run `propagate init` or pull the repository config first.")
	}
	result.ProjectConfigPath = configPath

	project, err := config.ReadProject(configPath)
	if err != nil {
		return EnvSetResult{}, commandError(ExitValidationError, "config_invalid", "Cannot read propagate.yaml", err)
	}
	result.TeamID = project.TeamID
	result.TeamName = project.TeamName

	localScope := findScopeSummary(project.Scopes, scopeName)
	if localScope == nil {
		return EnvSetResult{}, commandError(ExitValidationError, "scope_not_found", fmt.Sprintf("Scope %q is not configured in propagate.yaml", scopeName), nil, "Run `propagate config pull` if the scope was added in the cloud.")
	}

	apiURL := resolveAPIURL(opts.APIURL)
	if apiURL == "" {
		return EnvSetResult{}, commandError(ExitCloudUnavailable, "cloud_unavailable", "Propagate API URL is required for env set", nil, "Pass `--api-url` or set PROPAGATE_API_URL.")
	}
	client := apiclient.Client{BaseURL: apiURL, HTTPClient: configPushHTTPClient, CLIVersion: Version}
	bundle, err := client.PullBundle(context.Background(), ident, project.TeamID, scopeName)
	if err != nil {
		return EnvSetResult{}, mapEnvPushAPIError(err, scopeName, summary)
	}
	result.BackendStatus = "fetched"
	result.ConfigRevision = bundle.ConfigRevision
	if bundle.ConfigRevision != project.CloudRevision {
		return EnvSetResult{}, commandError(
			ExitConflict,
			"revision_conflict",
			"Cloud config revision differs from local propagate.yaml",
			nil,
			"Run `propagate config pull`, review the env file mappings, and retry env set.",
		)
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
		return EnvSetResult{}, commandError(ExitPermissionDenied, "scope_key_decrypt_failed", fmt.Sprintf("Cannot decrypt the scope key envelope for scope %q", scopeName), err, "No value was uploaded.", "Ask a Propagate admin to refresh your access envelope.")
	}

	existing, ambiguous, err := findCloudVariable(project.TeamID, scopeName, scopeKey, bundle.SecretVersions, variable)
	if err != nil {
		return EnvSetResult{}, commandError(ExitValidationError, "decrypt_failed", "Cannot decrypt current cloud env value for comparison", err, "No value was uploaded.")
	}
	if ambiguous {
		return EnvSetResult{}, commandError(ExitValidationError, "env_set_ambiguous", fmt.Sprintf("Variable %s exists in multiple env files for scope %s", variable, scopeName), nil, "Use `propagate env push` after editing the intended env file, or resolve duplicate cloud metadata.")
	}
	envPath := existing.Path
	if envPath == "" {
		envPath = chooseEnvSetPath(localScope.EnvFiles, bundle.EnvFileMappings)
	}
	if envPath == "" {
		return EnvSetResult{}, commandError(ExitValidationError, "env_file_missing", fmt.Sprintf("Scope %q has no env file mapping for new variable %s", scopeName, variable), nil, "Add an env file mapping to propagate.yaml before setting a new value.")
	}
	result.EnvFilePath = envPath

	switch {
	case !existing.Exists:
		result.ChangeType = "added"
	case existing.Value == value:
		result.ChangeType = "unchanged"
		result.Status = "no_change"
		result.BackendStatus = "up_to_date"
		result.NextSteps = []string{"No env set upload is needed."}
		return result, nil
	default:
		result.ChangeType = "changed"
	}
	if opts.DryRun {
		result.Status = "dry_run"
		result.BackendStatus = "validated"
		result.NextSteps = []string{"Re-run without `--dry-run` and with `--yes` after reviewing the safe summary."}
		return result, nil
	}
	if !opts.Yes {
		ok, err := promptConfirm(reader, streams.Out, fmt.Sprintf("Upload encrypted %s value for %s in %s?", result.ChangeType, variable, scopeName), false)
		if err != nil {
			return EnvSetResult{}, err
		}
		if !ok {
			return EnvSetResult{}, commandError(ExitUserCanceled, "user_canceled", "Env set was canceled before upload", nil)
		}
	}

	ciphertext, nonce, err := secretcrypto.EncryptValue(scopeKey, project.TeamID, scopeName, envPath, variable, bundle.ScopeKeyEnvelope.ScopeKeyVersion, value)
	if err != nil {
		return EnvSetResult{}, commandError(ExitInternalError, "internal_error", "Cannot encrypt env set value", err)
	}
	targetProject := updateScopeDeclarationForValue(project, scopeName, scopeKey, bundle.ScopeKeyEnvelope.ScopeKeyVersion, envVarKey{Path: envPath, Name: variable}, value)
	targetSnapshot, err := config.SnapshotJSON(targetProject)
	if err != nil {
		return EnvSetResult{}, commandError(ExitValidationError, "config_invalid", "Cannot build variable declaration for env set", err)
	}
	upsert := apiclient.EnvPushUpsert{
		EnvFilePath:     envPath,
		Name:            variable,
		Ciphertext:      ciphertext,
		Nonce:           nonce,
		Algorithm:       secretcrypto.ValueAlgorithm,
		ScopeKeyVersion: bundle.ScopeKeyEnvelope.ScopeKeyVersion,
	}
	if existing.Exists {
		upsert.ExpectedVersionID = existing.VersionID
	}
	operationID, err := operationID("env_set")
	if err != nil {
		return EnvSetResult{}, commandError(ExitInternalError, "internal_error", "Cannot create operation ID", err)
	}
	result.OperationID = operationID
	counts := apiclient.SafeCounts{}
	if result.ChangeType == "added" {
		counts.Added = 1
	} else {
		counts.Changed = 1
	}
	pushResult, err := client.EnvPush(context.Background(), ident, project.TeamID, scopeName, apiclient.EnvPushRequest{
		OperationID:            operationID,
		ExpectedConfigRevision: bundle.ConfigRevision,
		TargetConfigSnapshot:   json.RawMessage(targetSnapshot),
		Upserts:                []apiclient.EnvPushUpsert{upsert},
		SafeCounts:             counts,
		Client:                 apiclient.ClientMetadata{CLIVersion: Version, ClientKind: "cli"},
	})
	if err != nil {
		return EnvSetResult{}, mapEnvPushAPIError(err, scopeName, summary)
	}
	result.NewVersionsCount = len(pushResult.CreatedVersions)
	result.AuditEventsCount = pushResult.AuditEventsCount
	if pushResult.ConfigRevision != "" {
		targetProject.CloudRevision = pushResult.ConfigRevision
		targetProject.SyncStatus = "synced"
		rendered, err := config.RenderParsed(targetProject)
		if err != nil {
			return EnvSetResult{}, commandError(ExitValidationError, "config_invalid", "Cloud accepted env set but local config could not be rendered", err, "Run `propagate config pull` to recover the accepted config.")
		}
		if err := config.WriteRaw(configPath, rendered); err != nil {
			return EnvSetResult{}, commandError(ExitPartialLocalFailure, "partial_local_failure", "Cloud accepted env set but propagate.yaml could not be updated", err, "Run `propagate config pull` to recover the accepted config.")
		}
	}
	result.BackendStatus = "pushed"
	result.NextSteps = []string{"Ask teammates to run `propagate env pull` when they need the updated value."}
	return result, nil
}

type envSetExisting struct {
	Exists    bool
	Path      string
	Value     string
	VersionID string
}

func findCloudVariable(teamID string, scopeName string, scopeKey []byte, records []apiclient.SecretVersionRecord, variable string) (envSetExisting, bool, error) {
	var found envSetExisting
	for _, record := range records {
		if record.Name != variable {
			continue
		}
		if found.Exists {
			return envSetExisting{}, true, nil
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
			return envSetExisting{}, false, err
		}
		found = envSetExisting{
			Exists:    true,
			Path:      record.EnvFilePath,
			Value:     value,
			VersionID: record.CurrentVersionID,
		}
	}
	return found, false, nil
}

func chooseEnvSetPath(local []string, cloud []string) string {
	for _, path := range local {
		if strings.TrimSpace(path) != "" {
			return path
		}
	}
	for _, path := range cloud {
		if strings.TrimSpace(path) != "" {
			return path
		}
	}
	return ""
}

func promptEnvSetValue(reader *bufio.Reader, in io.Reader, out io.Writer, variable string) (string, error) {
	fmt.Fprintf(out, "Value for %s: ", variable)
	if file, ok := in.(*os.File); ok && isCharDevice(file) {
		if err := setTerminalEcho(file, false); err != nil {
			return "", commandError(ExitValidationError, "secure_prompt_failed", "Could not disable terminal echo for env value prompt", err)
		}
		defer func() {
			_ = setTerminalEcho(file, true)
			fmt.Fprintln(out)
		}()
	}
	value, err := reader.ReadString('\n')
	if err != nil && len(value) == 0 {
		return "", commandError(ExitUserCanceled, "user_canceled", "Prompt could not read env value", err)
	}
	value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
	if strings.ContainsAny(value, "\r\n") {
		return "", commandError(ExitValidationError, "validation_failed", "Env set only accepts a single-line value", nil)
	}
	return value, nil
}

func isCharDevice(file *os.File) bool {
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func setTerminalEcho(file *os.File, enabled bool) error {
	arg := "-echo"
	if enabled {
		arg = "echo"
	}
	cmd := exec.Command("stty", arg)
	cmd.Stdin = file
	return cmd.Run()
}

func renderEnvSetResult(w io.Writer, jsonOutput bool, result EnvSetResult) {
	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}
	switch result.Status {
	case "dry_run":
		fmt.Fprintln(w, "Env set dry run complete.")
	case "no_change":
		fmt.Fprintln(w, "Env value already matches the cloud.")
	default:
		fmt.Fprintln(w, "Env set complete.")
	}
	fmt.Fprintln(w)
	if result.TeamName != "" {
		fmt.Fprintf(w, "Team: %s (%s)\n", result.TeamName, result.TeamID)
	}
	fmt.Fprintf(w, "Scope: %s\n", result.Scope)
	fmt.Fprintf(w, "Variable: %s\n", result.Variable)
	if result.ChangeType != "" {
		fmt.Fprintf(w, "Change: %s\n", result.ChangeType)
	}
	if result.EnvFilePath != "" {
		fmt.Fprintf(w, "Env file: %s\n", result.EnvFilePath)
	}
	if result.Identity != nil {
		fmt.Fprintf(w, "Set by: %s (%s)\n", result.Identity.Handle, result.Identity.PublicKeySHA)
	}
	if result.OperationID != "" {
		fmt.Fprintf(w, "Operation: %s\n", result.OperationID)
	}
	fmt.Fprintf(w, "New versions uploaded: %d\n", result.NewVersionsCount)
	fmt.Fprintf(w, "Backend: %s\n", result.BackendStatus)
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

func printEnvSetHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate env set NAME [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --scope VALUE       scope to update (default dev)")
	fmt.Fprintln(w, "  --dry-run           validate and show the update plan without upload")
	fmt.Fprintln(w, "  --yes               confirm uploading the encrypted value update")
	fmt.Fprintln(w, "  --api-url VALUE     override Propagate API URL")
	fmt.Fprintln(w, "  --json              render machine-readable JSON")
	fmt.Fprintln(w, "  --non-interactive   fail instead of prompting")
}

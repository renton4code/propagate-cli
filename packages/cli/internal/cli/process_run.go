package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"propagate/cli/internal/apiclient"
	"propagate/cli/internal/config"
	"propagate/cli/internal/gitutil"
	"propagate/cli/internal/identity"
	"propagate/cli/internal/secretcrypto"
)

type processRunOptions struct {
	globalOptions
	Scope string
	Yes   bool
}

type decryptedProcessEnv struct {
	Ident          identity.Identity
	Project        config.ParsedProject
	ScopeName      string
	Bundle         apiclient.PullBundleData
	Client         apiclient.Client
	ValuesByFile   map[string]map[string]string
	FilePaths      []string
	VariablesCount int
}

func runProcessCommand(args []string, global globalOptions, streams Streams) int {
	opts := processRunOptions{globalOptions: global, Scope: "dev"}
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &opts.globalOptions)
	fs.StringVar(&opts.Scope, "scope", "dev", "scope to inject")
	fs.BoolVar(&opts.Yes, "yes", false, "confirm prod process injection")

	separator := commandSeparatorIndex(args)
	flagArgs := args
	var commandArgs []string
	if separator >= 0 {
		flagArgs = args[:separator]
		commandArgs = args[separator+1:]
	}
	for _, arg := range flagArgs {
		if arg == "help" || arg == "--help" || arg == "-h" {
			printProcessRunHelp(streams.Out)
			return ExitSuccess
		}
	}
	if err := fs.Parse(flagArgs); err != nil {
		cmdErr := commandError(ExitUsageError, "usage_error", "Invalid run flags", err, "Run `propagate run --help` for usage.")
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if fs.NArg() != 0 {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate run flags must come before `--`", nil, "Run `propagate run --scope dev -- COMMAND [args...]`.")
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if separator < 0 {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate run requires `--` before the command to execute", nil, "Run `propagate run --scope dev -- COMMAND [args...]`.")
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if len(commandArgs) == 0 {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate run requires a command after `--`", nil, "Run `propagate run --scope dev -- COMMAND [args...]`.")
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}

	if err := confirmProcessRun(opts, bufio.NewReader(streams.In), streams.In, streams.Out); err != nil {
		return renderError(streams.Err, opts.JSON, opts.NoColor, err)
	}
	result, err := prepareProcessEnv(opts, streams)
	if err != nil {
		return renderError(streams.Err, opts.JSON, opts.NoColor, err)
	}
	injected, err := processEnvAssignments(result.ValuesByFile)
	if err != nil {
		return renderError(streams.Err, opts.JSON, opts.NoColor, err)
	}
	if err := recordProcessRunEvent(result); err != nil {
		fmt.Fprintf(streams.Err, "Warning: process injection event could not be recorded: %s\n", safeAPIError(err))
	}
	return runInjectedProcess(commandArgs, injected, streams, opts)
}

func commandSeparatorIndex(args []string) int {
	for idx, arg := range args {
		if arg == "--" {
			return idx
		}
	}
	return -1
}

func prepareProcessEnv(opts processRunOptions, streams Streams) (decryptedProcessEnv, error) {
	scopeName := strings.TrimSpace(opts.Scope)
	if scopeName == "" {
		scopeName = "dev"
	}
	if err := config.ValidateScopeName(scopeName); err != nil {
		return decryptedProcessEnv{}, commandError(ExitUsageError, "usage_error", "Invalid run scope", err)
	}

	ident, err := identity.Load()
	if err != nil {
		return decryptedProcessEnv{}, commandError(ExitValidationError, "identity_missing", "Cannot load local Propagate identity for process injection", err, "Run `propagate init` to create or repair the local identity.")
	}
	summary := ident.Summary()

	worktree, err := gitutil.Discover(streams.WorkDir)
	if err != nil {
		return decryptedProcessEnv{}, commandError(ExitValidationError, "not_git_repo", "Cannot inject env values outside a Git worktree", err)
	}
	configPath, exists, err := config.ExistingPath(worktree.Root)
	if err != nil {
		return decryptedProcessEnv{}, commandError(ExitValidationError, "config_invalid", "Existing Propagate config needs attention", err, "Rename `propagate.yml` to `propagate.yaml` before running process injection again.")
	}
	if !exists {
		return decryptedProcessEnv{}, commandError(ExitValidationError, "config_missing", "propagate.yaml is required before process injection", nil, "Run `propagate init` or pull the repository config first.")
	}

	project, err := config.ReadProject(configPath)
	if err != nil {
		return decryptedProcessEnv{}, commandError(ExitValidationError, "config_invalid", "Cannot read propagate.yaml", err)
	}
	localScope := findScopeSummary(project.Scopes, scopeName)
	if localScope == nil {
		return decryptedProcessEnv{}, commandError(ExitValidationError, "scope_not_found", fmt.Sprintf("Scope %q is not configured in propagate.yaml", scopeName), nil, "Run `propagate config pull` if the scope was added in the cloud.")
	}

	apiURL := resolveAPIURL(opts.APIURL, streams.WorkDir)
	if apiURL == "" {
		return decryptedProcessEnv{}, commandError(ExitCloudUnavailable, "cloud_unavailable", "Propagate API URL is required for process injection", nil, "Pass `--api-url` or set PROPAGATE_API_URL.")
	}
	client := apiclient.Client{BaseURL: apiURL, HTTPClient: configPushHTTPClient, CLIVersion: Version}
	bundle, err := client.PullBundle(context.Background(), ident, project.TeamID, scopeName)
	if err != nil {
		return decryptedProcessEnv{}, mapProcessRunAPIError(err, scopeName, summary)
	}
	if bundle.Scope.Name != "" && bundle.Scope.Name != scopeName {
		return decryptedProcessEnv{}, commandError(ExitValidationError, "validation_failed", "Cloud returned a pull bundle for a different scope", fmt.Errorf("requested %s, received %s", scopeName, bundle.Scope.Name))
	}
	if bundle.ScopeKeyEnvelope.RecipientKeySHA != "" && bundle.ScopeKeyEnvelope.RecipientKeySHA != ident.PublicKeySHA {
		return decryptedProcessEnv{}, commandError(ExitPermissionDenied, "permission_denied", fmt.Sprintf("No readable scope key envelope was returned for scope %q", scopeName), nil, "Ask a Propagate admin to approve access and run `propagate config push`.")
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
		return decryptedProcessEnv{}, commandError(ExitPermissionDenied, "scope_key_decrypt_failed", fmt.Sprintf("Cannot decrypt the scope key envelope for scope %q", scopeName), err, "No process was started.", "Ask a Propagate admin to refresh your access envelope.")
	}

	valuesByFile, err := decryptPullValues(project.TeamID, scopeName, scopeKey, bundle.SecretVersions)
	if err != nil {
		return decryptedProcessEnv{}, commandError(ExitValidationError, "decrypt_failed", "Cannot decrypt one or more env values for process injection", err, "No process was started.")
	}
	filePaths := pullFilePaths(localScope.EnvFiles, bundle.EnvFileMappings, valuesByFile)
	return decryptedProcessEnv{
		Ident:          ident,
		Project:        project,
		ScopeName:      scopeName,
		Bundle:         bundle,
		Client:         client,
		ValuesByFile:   valuesByFile,
		FilePaths:      filePaths,
		VariablesCount: len(bundle.SecretVersions),
	}, nil
}

func processEnvAssignments(valuesByFile map[string]map[string]string) ([]string, error) {
	seen := map[string]envVarKey{}
	values := map[envVarKey]string{}
	for path, fileValues := range valuesByFile {
		for name, value := range fileValues {
			key := envVarKey{Path: path, Name: name}
			if first, exists := seen[name]; exists && first.Path != path {
				return nil, commandError(
					ExitValidationError,
					"env_injection_conflict",
					fmt.Sprintf("Cannot inject %s because it appears in multiple env files for this scope", name),
					nil,
					"Use env pull for file-based workflows, or remove duplicate variable names before using `propagate run`.",
				)
			}
			seen[name] = key
			values[key] = value
		}
	}

	keys := sortedLocalKeys(values)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key.Name+"="+values[key])
	}
	return env, nil
}

func confirmProcessRun(opts processRunOptions, reader *bufio.Reader, in io.Reader, out io.Writer) error {
	scopeName := strings.TrimSpace(opts.Scope)
	if scopeName == "" {
		scopeName = "dev"
	}
	if scopeName != "prod" || opts.Yes {
		return nil
	}
	if opts.NonInteractive {
		return commandError(ExitConfirmationRequired, "confirmation_required", "Non-interactive prod process injection requires --yes", nil, "Re-run with `--yes` only after confirming the child command should receive prod values.")
	}
	ok, err := promptConfirm(reader, in, out, "Inject prod env values into this process?", false)
	if err != nil {
		return err
	}
	if !ok {
		return commandError(ExitUserCanceled, "user_canceled", "Process injection was canceled before starting the child process", nil)
	}
	return nil
}

func recordProcessRunEvent(result decryptedProcessEnv) error {
	_, err := result.Client.RecordPullEvent(context.Background(), result.Ident, result.Project.TeamID, apiclient.PullEventRequest{
		Scope:          result.ScopeName,
		EnvFilePaths:   filePathsWithValues(result.FilePaths, result.ValuesByFile),
		ConfigRevision: result.Bundle.ConfigRevision,
		VariablesCount: result.VariablesCount,
		Client:         apiclient.ClientMetadata{CLIVersion: Version, ClientKind: "cli_run"},
	})
	return err
}

func mapProcessRunAPIError(err error, scope string, ident identity.Summary) error {
	var apiErr *apiclient.APIError
	if !errors.As(err, &apiErr) {
		return commandError(ExitCloudUnavailable, "cloud_unavailable", "Cannot fetch encrypted env values for process injection", err)
	}
	switch apiErr.Code {
	case "permission_denied":
		return commandError(
			ExitPermissionDenied,
			apiErr.Code,
			fmt.Sprintf("Cannot inject env values for scope %q with identity %s", scope, ident.PublicKeySHA),
			apiErr,
			"No process was started.",
			"Commit a `propagate team join` request or ask a Propagate admin to grant read access for this scope.",
		)
	case "team_not_found", "scope_not_found":
		return commandError(ExitValidationError, apiErr.Code, "The requested team or scope was not found in the cloud", apiErr, "Run `propagate config pull` if the local config is stale.")
	case "validation_failed", "usage_error":
		return commandError(ExitValidationError, apiErr.Code, "Process injection request was rejected by the cloud", apiErr)
	default:
		code := ExitCloudUnavailable
		if apiErr.StatusCode >= 400 && apiErr.StatusCode < 500 && !apiErr.Retryable {
			code = ExitValidationError
		}
		return commandError(code, apiErr.Code, "Cannot fetch encrypted env values for process injection", apiErr)
	}
}

func runInjectedProcess(commandArgs []string, injected []string, streams Streams, opts processRunOptions) int {
	cmd := exec.Command(commandArgs[0], commandArgs[1:]...)
	cmd.Dir = streams.WorkDir
	cmd.Env = append(os.Environ(), injected...)
	cmd.Stdin = streams.In
	cmd.Stdout = streams.Out
	cmd.Stderr = streams.Err
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		return renderError(streams.Err, opts.JSON, opts.NoColor, commandError(ExitValidationError, "process_start_failed", "Cannot start injected process", err))
	}
	return ExitSuccess
}

func printProcessRunHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate run [flags] -- COMMAND [args...]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --scope VALUE       scope to inject (default dev)")
	fmt.Fprintln(w, "  --yes               confirm prod process injection")
	fmt.Fprintln(w, "  --api-url VALUE     override Propagate API URL")
	fmt.Fprintln(w, "  --json              render machine-readable errors")
	fmt.Fprintln(w, "  --non-interactive   fail instead of prompting")
}

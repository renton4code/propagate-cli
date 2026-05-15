package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"propagate/cli/internal/config"
	"propagate/cli/internal/gitutil"
)

type scopeCreateOptions struct {
	globalOptions
	DryRun   bool
	EnvFiles envFileFlags
}

type envFileFlags []string

type ScopeCreateResult struct {
	OK                bool              `json:"ok"`
	Command           string            `json:"command"`
	Status            string            `json:"status"`
	DryRun            bool              `json:"dry_run"`
	ProjectConfigPath string            `json:"project_config_path"`
	TeamID            string            `json:"team_id"`
	TeamName          string            `json:"team_name"`
	Scope             string            `json:"scope"`
	EnvFiles          []string          `json:"env_files"`
	DefaultRoleAccess map[string]string `json:"default_role_access"`
	ConfigModified    bool              `json:"config_modified"`
	Warnings          []string          `json:"warnings,omitempty"`
	NextSteps         []string          `json:"next_steps,omitempty"`
}

func runScopeCommand(args []string, global globalOptions, streams Streams) int {
	if len(args) == 0 {
		printScopeHelp(streams.Out)
		return ExitSuccess
	}
	switch args[0] {
	case "create":
		return runScopeCreateCommand(args[1:], global, streams)
	case "help", "--help", "-h":
		printScopeHelp(streams.Out)
		return ExitSuccess
	default:
		err := commandError(ExitUsageError, "usage_error", fmt.Sprintf("Unknown scope command %q", args[0]), nil, "Run `propagate scope help` to see available scope commands.")
		return renderError(streams.Err, global.JSON, global.NoColor, err)
	}
}

func runScopeCreateCommand(args []string, global globalOptions, streams Streams) int {
	opts := scopeCreateOptions{globalOptions: global}
	fs := flag.NewFlagSet("scope create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &opts.globalOptions)
	fs.BoolVar(&opts.DryRun, "dry-run", false, "preview scope creation without writing propagate.yaml")
	fs.Var(&opts.EnvFiles, "env-file", "env file mapping for the new scope; may be repeated")

	flagArgs, scopeName, showHelp, splitErr := splitScopeCreateArgs(args)
	if splitErr != nil {
		cmdErr := commandError(ExitUsageError, "usage_error", "Invalid scope create arguments", splitErr, "Run `propagate scope create --help` for usage.")
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if showHelp {
		printScopeCreateHelp(streams.Out)
		return ExitSuccess
	}
	if err := fs.Parse(flagArgs); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printScopeCreateHelp(streams.Out)
			return ExitSuccess
		}
		cmdErr := commandError(ExitUsageError, "usage_error", "Invalid scope create flags", err, "Run `propagate scope create --help` for usage.")
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if fs.NArg() != 0 || scopeName == "" {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate scope create requires exactly one scope name", nil, "Run `propagate scope create staging`.")
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}

	result, err := runScopeCreate(scopeName, opts, streams)
	if err != nil {
		return renderError(streams.Err, opts.JSON, opts.NoColor, err)
	}
	renderScopeCreateResult(streams.Out, opts.JSON, opts.NoColor, result)
	return ExitSuccess
}

func splitScopeCreateArgs(args []string) ([]string, string, bool, error) {
	var flagArgs []string
	var scopeName string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "help" || arg == "--help" || arg == "-h" {
			return nil, "", true, nil
		}
		if strings.HasPrefix(arg, "-") {
			flagArgs = append(flagArgs, arg)
			if scopeCreateFlagNeedsValue(arg) && !strings.Contains(arg, "=") {
				if i+1 >= len(args) {
					return flagArgs, scopeName, false, nil
				}
				i++
				flagArgs = append(flagArgs, args[i])
			}
			continue
		}
		if scopeName != "" {
			return nil, "", false, fmt.Errorf("unexpected extra scope name %q", arg)
		}
		scopeName = arg
	}
	return flagArgs, scopeName, false, nil
}

func scopeCreateFlagNeedsValue(flagName string) bool {
	switch flagName {
	case "--env-file", "-env-file", "--api-url", "-api-url":
		return true
	default:
		return false
	}
}

func runScopeCreate(name string, opts scopeCreateOptions, streams Streams) (ScopeCreateResult, error) {
	scopeName := strings.TrimSpace(name)
	if err := config.ValidateScopeName(scopeName); err != nil {
		return ScopeCreateResult{}, commandError(ExitUsageError, "usage_error", "Invalid scope name", err)
	}
	envFiles, err := opts.EnvFiles.normalized()
	if err != nil {
		return ScopeCreateResult{}, commandError(ExitUsageError, "usage_error", "Invalid env file mapping", err)
	}

	result := ScopeCreateResult{
		OK:                true,
		Command:           "scope create",
		Status:            "success",
		DryRun:            opts.DryRun,
		Scope:             scopeName,
		EnvFiles:          envFiles,
		DefaultRoleAccess: defaultRoleAccess(scopeName),
	}

	worktree, err := gitutil.Discover(streams.WorkDir)
	if err != nil {
		return ScopeCreateResult{}, commandError(ExitValidationError, "not_git_repo", "Cannot create a scope outside a Git worktree", err)
	}
	configPath, exists, err := config.ExistingPath(worktree.Root)
	if err != nil {
		return ScopeCreateResult{}, commandError(ExitValidationError, "config_invalid", "Existing Propagate config needs attention", err, "Rename `propagate.yml` to `propagate.yaml` before running scope create again.")
	}
	if !exists {
		return ScopeCreateResult{}, commandError(ExitValidationError, "config_missing", "propagate.yaml is required before scope create", nil, "Run `propagate init` or pull the repository config first.")
	}
	result.ProjectConfigPath = configPath

	project, err := config.ReadProject(configPath)
	if err != nil {
		return ScopeCreateResult{}, commandError(ExitValidationError, "config_invalid", "Cannot read propagate.yaml", err)
	}
	result.TeamID = project.TeamID
	result.TeamName = project.TeamName

	if findScopeSummary(project.Scopes, scopeName) != nil {
		return ScopeCreateResult{}, commandError(ExitConflict, "duplicate_scope", fmt.Sprintf("Scope %q already exists in propagate.yaml", scopeName), nil, "Choose a different scope name or edit the existing scope metadata.")
	}

	target := cloneParsedProjectForConfigEdit(project)
	target.Scopes = append(target.Scopes, config.ScopeSummary{
		Name:              scopeName,
		EnvFiles:          envFiles,
		DefaultRoleAccess: defaultRoleAccess(scopeName),
	})

	rendered, err := config.RenderParsed(target)
	if err != nil {
		return ScopeCreateResult{}, commandError(ExitValidationError, "config_invalid", "Cannot render updated propagate.yaml", err)
	}
	result.ConfigModified = rendered != project.Raw
	result.NextSteps = scopeCreateNextSteps(scopeName, envFiles)
	if opts.DryRun {
		result.Status = "dry_run"
		return result, nil
	}
	if err := config.WriteRaw(configPath, rendered); err != nil {
		return ScopeCreateResult{}, commandError(ExitPartialLocalFailure, "partial_local_failure", "Could not write updated propagate.yaml", err)
	}
	return result, nil
}

func (f *envFileFlags) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(*f, ",")
}

func (f *envFileFlags) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("env file path cannot be empty")
	}
	*f = append(*f, value)
	return nil
}

func (f envFileFlags) normalized() ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, value := range f {
		path := strings.TrimSpace(value)
		if path == "" {
			return nil, fmt.Errorf("env file path cannot be empty")
		}
		if strings.HasPrefix(path, "/") || path == "." || path == ".." || strings.HasPrefix(path, "../") || strings.Contains(path, "/../") {
			return nil, fmt.Errorf("env file path must be repo-relative and inside the worktree: %s", path)
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	sort.Strings(out)
	return out, nil
}

func scopeCreateNextSteps(scopeName string, envFiles []string) []string {
	steps := []string{
		"Run `propagate config status` to compare the new scope metadata with cloud state.",
	}
	if len(envFiles) == 0 {
		steps = append(steps, fmt.Sprintf("Run `propagate config edit` to add env file mappings or move declaration metadata into scope %s.", scopeName))
	} else {
		steps = append(steps, fmt.Sprintf("Run `propagate config edit` if you need to move declaration metadata into scope %s.", scopeName))
	}
	steps = append(steps, "Run `propagate config push` after review to publish the new scope.")
	steps = append(steps, fmt.Sprintf("After publishing, prepare the mapped env file locally and run `propagate env push --scope %s --dry-run` to seed values.", scopeName))
	return steps
}

func renderScopeCreateResult(w io.Writer, jsonOutput bool, noColor bool, result ScopeCreateResult) {
	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}
	style := newOutputStyle(noColor)
	renderCommandTitle(w, style, "Propagate scope create", result.DryRun)
	switch result.Status {
	case "dry_run":
		renderNote(w, style, "Scope create dry run complete.")
	default:
		renderOK(w, style, "Scope created.")
	}
	fmt.Fprintln(w)
	if result.TeamName != "" {
		fmt.Fprintf(w, "Team: %s (%s)\n", result.TeamName, result.TeamID)
	}
	if result.ProjectConfigPath != "" {
		fmt.Fprintf(w, "Config: %s\n", result.ProjectConfigPath)
	}
	fmt.Fprintf(w, "Scope: %s\n", result.Scope)
	if len(result.EnvFiles) == 0 {
		fmt.Fprintln(w, "Env files: none")
	} else {
		fmt.Fprintln(w, "Env files:")
		for _, path := range result.EnvFiles {
			fmt.Fprintf(w, "  - %s\n", path)
		}
	}
	fmt.Fprintln(w, "Default access:")
	for _, role := range sortedJoinScopes(result.DefaultRoleAccess) {
		fmt.Fprintf(w, "  - %s: %s\n", role, result.DefaultRoleAccess[role])
	}
	fmt.Fprintf(w, "propagate.yaml modified: %t\n", result.ConfigModified)
	renderWarnings(w, style, result.Warnings)
	renderNextSteps(w, style, result.NextSteps)
}

func printScopeHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate scope create NAME [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  create    create an empty scope in propagate.yaml")
}

func printScopeCreateHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate scope create NAME [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --env-file PATH           add an env file mapping; may be repeated")
	fmt.Fprintln(w, "  --dry-run                 preview without writing propagate.yaml")
	fmt.Fprintln(w, "  --json                    render machine-readable JSON")
	fmt.Fprintln(w, "  --non-interactive         fail instead of prompting")
	fmt.Fprintln(w, "  --no-color                disable terminal color")
}

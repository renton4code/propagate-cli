package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"propagate/cli/internal/atomicfile"
	"propagate/cli/internal/config"
	"propagate/cli/internal/gitutil"
	"propagate/cli/internal/identity"
)

type teamJoinOptions struct {
	globalOptions
	Handle          string
	RequestedRole   string
	RequestedScopes scopeFlags
	DryRun          bool
}

type scopeFlags []string

type TeamJoinResult struct {
	OK                  bool              `json:"ok"`
	Command             string            `json:"command"`
	Status              string            `json:"status"`
	DryRun              bool              `json:"dry_run"`
	PendingJoinAdded    bool              `json:"pending_join_added"`
	WouldAddPendingJoin bool              `json:"would_add_pending_join,omitempty"`
	ProjectConfigPath   string            `json:"project_config_path"`
	IdentityCreated     bool              `json:"identity_created"`
	IdentityPath        string            `json:"identity_path,omitempty"`
	Identity            *identity.Summary `json:"identity,omitempty"`
	TeamID              string            `json:"team_id,omitempty"`
	TeamName            string            `json:"team_name,omitempty"`
	RequestedRole       string            `json:"requested_role"`
	RequestedScopes     map[string]string `json:"requested_scopes,omitempty"`
	BackendStatus       string            `json:"backend_status"`
	Warnings            []string          `json:"warnings,omitempty"`
	NextSteps           []string          `json:"next_steps,omitempty"`
}

func runTeamCommand(args []string, global globalOptions, streams Streams) int {
	if len(args) == 0 {
		printTeamHelp(streams.Out)
		return ExitSuccess
	}
	switch args[0] {
	case "join":
		return runTeamJoinCommand(args[1:], global, streams)
	case "status":
		return runTeamStatusCommand(args[1:], global, streams)
	case "help", "--help", "-h":
		printTeamHelp(streams.Out)
		return ExitSuccess
	default:
		err := commandError(ExitUsageError, "usage_error", fmt.Sprintf("Unknown team command %q", args[0]), nil, "Run `propagate team help` to see available team commands.")
		return renderError(streams.Err, global.JSON, err)
	}
}

func runTeamJoinCommand(args []string, global globalOptions, streams Streams) int {
	opts := teamJoinOptions{
		globalOptions: global,
		RequestedRole: "developers",
	}
	fs := flag.NewFlagSet("team join", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &opts.globalOptions)
	fs.StringVar(&opts.Handle, "handle", "", "handle to store with a new local identity")
	fs.StringVar(&opts.RequestedRole, "role", opts.RequestedRole, "requested role: developers or admins")
	fs.Var(&opts.RequestedScopes, "scope", "requested scope, optionally as scope=permission; may be repeated")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "show what would happen without writing propagate.yaml")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printTeamJoinHelp(streams.Out)
			return ExitSuccess
		}
		cmdErr := commandError(ExitUsageError, "usage_error", "Invalid team join flags", err, "Run `propagate team join --help` for usage.")
		return renderError(streams.Err, opts.JSON, cmdErr)
	}
	if fs.NArg() != 0 {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate team join does not accept positional arguments", nil)
		return renderError(streams.Err, opts.JSON, cmdErr)
	}

	result, err := runTeamJoin(opts, streams)
	if err != nil {
		return renderError(streams.Err, opts.JSON, err)
	}
	renderTeamJoinResult(streams.Out, opts.JSON, result)
	return ExitSuccess
}

func runTeamJoin(opts teamJoinOptions, streams Streams) (TeamJoinResult, error) {
	opts.RequestedRole = strings.TrimSpace(opts.RequestedRole)
	if opts.RequestedRole == "" {
		opts.RequestedRole = "developers"
	}
	if err := config.ValidateRole(opts.RequestedRole); err != nil {
		return TeamJoinResult{}, commandError(ExitUsageError, "usage_error", "Invalid requested role", err)
	}

	reader := bufio.NewReader(streams.In)
	result := TeamJoinResult{
		OK:            true,
		Command:       "team join",
		Status:        "success",
		DryRun:        opts.DryRun,
		RequestedRole: opts.RequestedRole,
		BackendStatus: "not_contacted_git_review_only",
		NextSteps: []string{
			"Commit this config change.",
			"Open a pull request.",
			"Ask a Propagate admin to run `propagate config push` after approval.",
		},
	}

	identityDir, err := identity.Directory()
	if err != nil {
		return TeamJoinResult{}, commandError(ExitValidationError, "identity_missing", "Cannot locate local home directory for Propagate identity", err)
	}
	identityPath := filepath.Join(identityDir, identity.IdentityFile)
	identityExists, err := atomicfile.Exists(identityPath)
	if err != nil {
		return TeamJoinResult{}, commandError(ExitValidationError, "identity_missing", "Cannot inspect local Propagate identity", err)
	}
	if !identityExists && strings.TrimSpace(opts.Handle) == "" {
		handle, err := promptRequired(reader, streams.Out, opts.NonInteractive, "Handle (name or email)")
		if err != nil {
			return TeamJoinResult{}, err
		}
		opts.Handle = handle
	}

	var ident identity.Identity
	if opts.DryRun {
		if identityExists {
			loaded, err := identity.Load()
			if err != nil {
				return TeamJoinResult{}, commandError(ExitValidationError, "identity_corrupt", "Existing Propagate identity could not be loaded", err)
			}
			ident = loaded
		} else {
			created, err := identity.New(opts.Handle, time.Now().UTC())
			if err != nil {
				return TeamJoinResult{}, commandError(ExitValidationError, "identity_missing", "Cannot create a dry-run Propagate identity", err)
			}
			ident = created
			result.IdentityCreated = true
			result.IdentityPath = identityPath
		}
	} else {
		ensured, err := identity.Ensure(opts.Handle)
		if err != nil {
			return TeamJoinResult{}, commandError(ExitValidationError, "identity_missing", "Cannot create or load local Propagate identity", err)
		}
		ident = ensured.Identity
		result.IdentityCreated = ensured.Created
		result.IdentityPath = ensured.Path
	}
	summary := ident.Summary()
	result.Identity = &summary

	worktree, err := gitutil.Discover(streams.WorkDir)
	if err != nil {
		return TeamJoinResult{}, commandError(ExitValidationError, "not_git_repo", "Cannot request team access outside a Git worktree", err)
	}
	configPath, exists, err := config.ExistingPath(worktree.Root)
	if err != nil {
		return TeamJoinResult{}, commandError(ExitValidationError, "config_invalid", "Existing Propagate config needs attention", err, "Rename `propagate.yml` to `propagate.yaml` before running team join again.")
	}
	if !exists {
		return TeamJoinResult{}, commandError(
			ExitValidationError,
			"config_missing",
			"propagate.yaml is required before requesting team access",
			nil,
			"Ask an admin to share the repository's Propagate config, or run `propagate init` if you are setting up a new team.",
		)
	}
	result.ProjectConfigPath = configPath

	project, err := config.ReadProject(configPath)
	if err != nil {
		return TeamJoinResult{}, commandError(ExitValidationError, "config_invalid", "Cannot read propagate.yaml", err)
	}
	result.TeamID = project.TeamID
	result.TeamName = project.TeamName

	requestedScopes, err := opts.RequestedScopes.Map()
	if err != nil {
		return TeamJoinResult{}, commandError(ExitUsageError, "usage_error", "Invalid requested scope", err)
	}
	if len(opts.RequestedScopes) == 0 {
		requestedScopes = project.DefaultRequestedScopes(opts.RequestedRole)
	}
	result.RequestedScopes = requestedScopes

	request := config.JoinRequest{
		Handle:              summary.Handle,
		PublicKeySHA:        summary.PublicKeySHA,
		SigningPublicKey:    summary.SigningPublicKey,
		EncryptionPublicKey: summary.EncryptionPublicKey,
		RequestedRole:       opts.RequestedRole,
		RequestedScopes:     requestedScopes,
		CreatedAt:           time.Now().UTC().Format(time.RFC3339),
	}

	nextConfig, err := config.RenderWithPendingJoin(project, request)
	if err != nil {
		switch {
		case errors.Is(err, config.ErrAlreadyMember):
			return TeamJoinResult{}, commandError(ExitValidationError, "config_invalid", "This identity is already an active team member", err, "Run `propagate team status` to inspect current membership.")
		case errors.Is(err, config.ErrDuplicatePendingJoin):
			return TeamJoinResult{}, commandError(ExitConflict, "duplicate_pending_join", "This identity already has a pending join request", err, "Commit the existing propagate.yaml diff or ask an admin to review it.")
		default:
			return TeamJoinResult{}, commandError(ExitValidationError, "config_invalid", "Cannot add pending join request to propagate.yaml", err)
		}
	}

	if opts.DryRun {
		result.Status = "dry_run"
		result.WouldAddPendingJoin = true
		result.NextSteps = []string{
			"Re-run without `--dry-run` to update propagate.yaml.",
			"After writing the request, commit the config diff and open a pull request.",
		}
		return result, nil
	}
	if err := config.WriteRaw(configPath, nextConfig); err != nil {
		return TeamJoinResult{}, commandError(ExitPartialLocalFailure, "partial_local_failure", "Could not write pending join request to propagate.yaml", err)
	}
	result.PendingJoinAdded = true
	return result, nil
}

func (s *scopeFlags) String() string {
	if s == nil {
		return ""
	}
	return strings.Join(*s, ",")
}

func (s *scopeFlags) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("scope cannot be empty")
	}
	*s = append(*s, value)
	return nil
}

func (s scopeFlags) Map() (map[string]string, error) {
	out := map[string]string{}
	for _, raw := range s {
		name := raw
		permission := "read"
		if left, right, ok := strings.Cut(raw, "="); ok {
			name = strings.TrimSpace(left)
			permission = strings.TrimSpace(right)
		}
		if err := config.ValidateScopeName(name); err != nil {
			return nil, err
		}
		if err := config.ValidatePermission(permission); err != nil {
			return nil, err
		}
		out[name] = permission
	}
	return out, nil
}

func renderTeamJoinResult(w io.Writer, jsonOutput bool, result TeamJoinResult) {
	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}

	if result.DryRun {
		fmt.Fprintln(w, "Would add join request to propagate.yaml.")
		fmt.Fprintln(w, "Mode: dry run; no files were written.")
	} else {
		fmt.Fprintln(w, "Join request added to propagate.yaml.")
	}
	fmt.Fprintln(w, "You do not have secret access yet.")
	fmt.Fprintln(w)

	if result.Identity != nil {
		action := "Using existing"
		if result.IdentityCreated {
			action = "Created"
			if result.DryRun {
				action = "Would create"
			}
		}
		fmt.Fprintf(w, "%s local identity: %s (%s)\n", action, result.Identity.Handle, result.Identity.PublicKeySHA)
	}
	if result.TeamName != "" {
		fmt.Fprintf(w, "Team: %s", result.TeamName)
		if result.TeamID != "" {
			fmt.Fprintf(w, " (%s)", result.TeamID)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintf(w, "Requested role: %s\n", result.RequestedRole)
	if len(result.RequestedScopes) == 0 {
		fmt.Fprintln(w, "Requested scopes: none specified")
	} else {
		fmt.Fprintln(w, "Requested scopes:")
		for _, scope := range sortedScopeNames(result.RequestedScopes) {
			fmt.Fprintf(w, "  - %s: %s\n", scope, result.RequestedScopes[scope])
		}
	}
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

func sortedScopeNames(scopes map[string]string) []string {
	names := make([]string, 0, len(scopes))
	for name := range scopes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func printTeamHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate team join [flags]")
	fmt.Fprintln(w, "  propagate team status [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  join      add a Git-reviewed pending join request to propagate.yaml")
	fmt.Fprintln(w, "  status    show local membership and cloud team activity")
}

func printTeamJoinHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate team join [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --handle VALUE           handle for a new local identity")
	fmt.Fprintln(w, "  --role VALUE             requested role: developers or admins")
	fmt.Fprintln(w, "  --scope VALUE            requested scope, optionally scope=permission; may be repeated")
	fmt.Fprintln(w, "  --dry-run                show what would happen without writing propagate.yaml")
	fmt.Fprintln(w, "  --json                   render machine-readable JSON")
	fmt.Fprintln(w, "  --non-interactive        fail instead of prompting")
}

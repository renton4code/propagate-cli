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
	Handle              string
	RequestedManagement bool
	RequestedScopes     scopeFlags
	DryRun              bool
	IncludeInit         bool
	InitAgentGuidance   bool
	InitSkipGuidance    bool
	JoinMode            string
	InviteID            string
	InvitePIN           string
}

type scopeFlags []string

type TeamJoinResult struct {
	OK                  bool              `json:"ok"`
	Command             string            `json:"command"`
	Status              string            `json:"status"`
	DryRun              bool              `json:"dry_run"`
	InitIncluded        bool              `json:"init_included,omitempty"`
	Init                *InitResult       `json:"init,omitempty"`
	PendingJoinAdded    bool              `json:"pending_join_added"`
	WouldAddPendingJoin bool              `json:"would_add_pending_join,omitempty"`
	PreApproved         bool              `json:"pre_approved,omitempty"`
	ProjectConfigPath   string            `json:"project_config_path"`
	IdentityCreated     bool              `json:"identity_created"`
	IdentityPath        string            `json:"identity_path,omitempty"`
	Identity            *identity.Summary `json:"identity,omitempty"`
	TeamID              string            `json:"team_id,omitempty"`
	TeamName            string            `json:"team_name,omitempty"`
	RequestedManagement bool              `json:"requested_management,omitempty"`
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
	case "invite":
		return runTeamInviteCommand(args[1:], global, streams)
	case "status":
		return runTeamStatusCommand(args[1:], global, streams)
	case "help", "--help", "-h":
		printTeamHelp(streams.Out)
		return ExitSuccess
	default:
		err := commandError(ExitUsageError, "usage_error", fmt.Sprintf("Unknown team command %q", args[0]), nil, "Run `propagate team help` to see available team commands.")
		return renderError(streams.Err, global.JSON, global.NoColor, err)
	}
}

func runTeamJoinCommand(args []string, global globalOptions, streams Streams) int {
	opts := teamJoinOptions{
		globalOptions: global,
	}
	fs := flag.NewFlagSet("team join", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &opts.globalOptions)
	fs.StringVar(&opts.Handle, "handle", "", "handle to store with a new local identity")
	fs.BoolVar(&opts.RequestedManagement, "management", false, "request management access for config changes")
	fs.Var(&opts.RequestedScopes, "scope", "requested scope, optionally as scope=permission; may be repeated")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "show what would happen without writing propagate.yaml")
	fs.BoolVar(&opts.IncludeInit, "init", false, "run existing-project init before adding the join request")
	fs.BoolVar(&opts.InitAgentGuidance, "agent-guidance", false, "with --init, create or update generic AGENTS.md Propagate guidance")
	fs.BoolVar(&opts.InitSkipGuidance, "skip-agent-guidance", false, "with --init, skip agent guidance setup")
	fs.StringVar(&opts.JoinMode, "join-mode", "auto", "when invite codes exist: auto, request (git-mediated only), or invite (PIN verification)")
	fs.StringVar(&opts.InviteID, "invite-id", "", "invite to redeem when multiple active invites exist or for non-interactive invite join")
	fs.StringVar(&opts.InvitePIN, "pin", "", "invite PIN for non-interactive join-mode invite (4 digits + letter)")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printTeamJoinHelp(streams.Out)
			return ExitSuccess
		}
		cmdErr := commandError(ExitUsageError, "usage_error", "Invalid team join flags", err, "Run `propagate team join --help` for usage.")
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if fs.NArg() != 0 {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate team join does not accept positional arguments", nil)
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if opts.InitAgentGuidance && opts.InitSkipGuidance {
		cmdErr := commandError(ExitUsageError, "usage_error", "--agent-guidance and --skip-agent-guidance cannot be used together", nil)
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if !opts.IncludeInit && (opts.InitAgentGuidance || opts.InitSkipGuidance) {
		cmdErr := commandError(ExitUsageError, "usage_error", "--agent-guidance and --skip-agent-guidance require --init for team join", nil)
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}

	result, err := runTeamJoin(opts, streams)
	if err != nil {
		return renderError(streams.Err, opts.JSON, opts.NoColor, err)
	}
	renderTeamJoinResult(streams.Out, opts.JSON, opts.NoColor, result)
	return ExitSuccess
}

func runTeamJoin(opts teamJoinOptions, streams Streams) (TeamJoinResult, error) {
	reader := bufio.NewReader(streams.In)
	result := TeamJoinResult{
		OK:                  true,
		Command:             "team join",
		Status:              "success",
		DryRun:              opts.DryRun,
		RequestedManagement: opts.RequestedManagement,
		BackendStatus:       "not_contacted_git_review_only",
		NextSteps: []string{
			"Commit this config change.",
			"Open a pull request.",
			"Ask a Propagate management member to run `propagate config push` after approval.",
		},
	}
	if opts.IncludeInit {
		initResult, err := runInitWithReader(initOptions{
			globalOptions:       opts.globalOptions,
			Handle:              opts.Handle,
			DryRun:              opts.DryRun,
			AgentGuidance:       opts.InitAgentGuidance,
			SkipAgentGuidance:   opts.InitSkipGuidance,
			ExistingProjectOnly: true,
		}, streams, reader)
		if err != nil {
			return TeamJoinResult{}, err
		}
		result.InitIncluded = true
		result.Init = &initResult
		result.Warnings = append(result.Warnings, initResult.Warnings...)
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
		handle, err := promptRequired(reader, streams.In, streams.Out, opts.NonInteractive, "Handle (name or email)")
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
	if result.Init != nil && result.Init.IdentityCreated {
		result.IdentityCreated = true
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
			"Ask a Propagate management member to share the repository's Propagate config, or run `propagate init` if you are setting up a new team.",
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
		requestedScopes = project.DefaultRequestedAccess(opts.RequestedManagement)
	}
	result.RequestedScopes = requestedScopes

	inviteMeta, err := resolveInviteJoinIfNeeded(streams, &opts, project.TeamID, summary, ident, requestedScopes)
	if err != nil {
		return TeamJoinResult{}, err
	}
	if inviteMeta != nil {
		result.Warnings = append(result.Warnings, inviteMeta.Warnings...)
		if inviteMeta.BackendStatus != "" {
			result.BackendStatus = inviteMeta.BackendStatus
		}
	}

	if inviteMeta != nil && inviteMeta.PreApproved {
		member := config.Member{
			Handle:              summary.Handle,
			PublicKeySHA:        summary.PublicKeySHA,
			SigningPublicKey:    summary.SigningPublicKey,
			EncryptionPublicKey: summary.EncryptionPublicKey,
			Management:         opts.RequestedManagement,
			Scopes:             requestedScopes,
		}
		nextConfig, err := config.RenderWithApprovedMember(project, member)
		if err != nil {
			if errors.Is(err, config.ErrAlreadyMember) {
				return TeamJoinResult{}, commandError(ExitValidationError, "config_invalid", "This identity is already an active team member", err, "Run `propagate team status` to inspect current membership.")
			}
			return TeamJoinResult{}, commandError(ExitValidationError, "config_invalid", "Cannot add member to propagate.yaml", err)
		}
		if opts.DryRun {
			result.Status = "dry_run"
			result.NextSteps = []string{"Re-run without `--dry-run` to complete the join."}
			return result, nil
		}
		if err := config.WriteRaw(configPath, nextConfig); err != nil {
			return TeamJoinResult{}, commandError(ExitPartialLocalFailure, "partial_local_failure", "Could not write member to propagate.yaml", err)
		}
		result.PendingJoinAdded = true
		result.PreApproved = true
		result.NextSteps = []string{
			"Access granted. You can now use secrets immediately:",
			"  propagate run --scope dev -- <your command>",
			"  propagate env pull --scope dev",
			"Commit the propagate.yaml change for audit purposes.",
		}
		return result, nil
	}

	request := config.JoinRequest{
		Handle:              summary.Handle,
		PublicKeySHA:        summary.PublicKeySHA,
		SigningPublicKey:    summary.SigningPublicKey,
		EncryptionPublicKey: summary.EncryptionPublicKey,
		RequestedManagement: opts.RequestedManagement,
		RequestedScopes:     requestedScopes,
		CreatedAt:           time.Now().UTC().Format(time.RFC3339),
	}
	if inviteMeta != nil {
		request.SourceInviteID = inviteMeta.SourceInviteID
		request.SourceInviteLabel = inviteMeta.SourceInviteLabel
		request.RedemptionID = inviteMeta.RedemptionID
	}

	nextConfig, err := config.RenderWithPendingJoin(project, request)
	if err != nil {
		switch {
		case errors.Is(err, config.ErrAlreadyMember):
			return TeamJoinResult{}, commandError(ExitValidationError, "config_invalid", "This identity is already an active team member", err, "Run `propagate team status` to inspect current membership.")
		case errors.Is(err, config.ErrDuplicatePendingJoin):
			return TeamJoinResult{}, commandError(ExitConflict, "duplicate_pending_join", "This identity already has a pending join request", err, "Commit the existing propagate.yaml diff or ask a Propagate management member to review it.")
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

func renderTeamJoinResult(w io.Writer, jsonOutput bool, noColor bool, result TeamJoinResult) {
	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}

	style := newOutputStyle(noColor)
	renderCommandTitle(w, style, "Propagate team join", result.DryRun)
	if result.InitIncluded {
		renderOK(w, style, "Init completed before join.")
		renderTeamJoinInitSummary(w, style, result.Init)
		fmt.Fprintln(w)
	}
	if result.DryRun {
		renderNote(w, style, "Would add join request to propagate.yaml.")
		renderNote(w, style, "Mode: dry run; no files were written.")
	} else if result.PreApproved {
		renderOK(w, style, "Access granted. Member added to propagate.yaml.")
	} else {
		renderOK(w, style, "Join request added to propagate.yaml.")
	}
	if !result.PreApproved {
		renderNote(w, style, "You do not have secret access yet.")
	}
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
	if result.RequestedManagement {
		fmt.Fprintln(w, "Requested management: true")
	} else {
		fmt.Fprintln(w, "Requested management: false")
	}
	if len(result.RequestedScopes) == 0 {
		fmt.Fprintln(w, "Requested scopes: none specified")
	} else {
		fmt.Fprintln(w, style.bold("Requested scopes:"))
		for _, scope := range sortedScopeNames(result.RequestedScopes) {
			fmt.Fprintf(w, "  - %s: %s\n", scope, result.RequestedScopes[scope])
		}
	}
	fmt.Fprintf(w, "Backend: %s\n", result.BackendStatus)

	renderWarnings(w, style, result.Warnings)
	renderNextSteps(w, style, result.NextSteps)
}

func renderTeamJoinInitSummary(w io.Writer, style outputStyle, result *InitResult) {
	if result == nil {
		return
	}
	switch {
	case result.ProjectAlreadyConfigured:
		renderOK(w, style, "Project config already existed.")
	case result.ProjectCreated:
		renderOK(w, style, "Project config was initialized.")
	}
	switch result.AgentGuidance.Status {
	case "created", "updated", "unchanged":
		renderOK(w, style, fmt.Sprintf("Agent guidance: %s.", result.AgentGuidance.Status))
	case "failed":
		renderWarning(w, style, "Agent guidance: failed.")
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
	fmt.Fprintln(w, "  invite    create, list, or revoke PIN invites (management)")
	fmt.Fprintln(w, "  status    show local membership and cloud team activity")
}

func printTeamJoinHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate team join [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --handle VALUE           handle for a new local identity")
	fmt.Fprintln(w, "  --management             request config-management access")
	fmt.Fprintln(w, "  --scope VALUE            requested scope, optionally scope=permission; may be repeated")
	fmt.Fprintln(w, "  --dry-run                show what would happen without writing propagate.yaml")
	fmt.Fprintln(w, "  --init                   run existing-project init before adding the join request")
	fmt.Fprintln(w, "  --agent-guidance         with --init, create or update AGENTS.md guidance")
	fmt.Fprintln(w, "  --skip-agent-guidance    with --init, skip AGENTS.md guidance")
	fmt.Fprintln(w, "  --join-mode MODE         auto|request|invite when API reports active invite codes")
	fmt.Fprintln(w, "  --invite-id ID           invite to redeem (non-interactive / multi-invite)")
	fmt.Fprintln(w, "  --pin VALUE              invite PIN for non-interactive invite join")
	fmt.Fprintln(w, "  --json                   render machine-readable JSON")
	fmt.Fprintln(w, "  --non-interactive        fail instead of prompting")
}

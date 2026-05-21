package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"propagate/cli/internal/config"
	"propagate/cli/internal/gitutil"
)

type quickstartOptions struct {
	globalOptions
	Handle              string
	TeamName            string
	InviteLabel         string
	RequestedManagement bool
	RequestedScopes     scopeFlags
	Yes                 bool
	DryRun              bool
	AgentGuidance       bool
	SkipAgentGuidance   bool
	JoinMode            string
	InviteID            string
	InvitePIN           string
}

type QuickstartResult struct {
	OK        bool                     `json:"ok"`
	Command   string                   `json:"command"`
	Mode      string                   `json:"mode"`
	Status    string                   `json:"status"`
	DryRun    bool                     `json:"dry_run"`
	Init      *InitResult              `json:"init,omitempty"`
	Invite    *TeamInviteCreateResult  `json:"invite,omitempty"`
	Invites   []TeamInviteCreateResult `json:"invites,omitempty"`
	Join      *TeamJoinResult          `json:"join,omitempty"`
	Warnings  []string                 `json:"warnings,omitempty"`
	NextSteps []string                 `json:"next_steps,omitempty"`
}

func runQuickstartCommand(args []string, global globalOptions, streams Streams) int {
	opts := quickstartOptions{globalOptions: global}
	fs := flag.NewFlagSet("quickstart", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &opts.globalOptions)
	fs.StringVar(&opts.Handle, "handle", "", "handle for a new local identity")
	fs.StringVar(&opts.TeamName, "team-name", "", "team name for new project setup")
	fs.StringVar(&opts.InviteLabel, "invite-label", "", "human-readable label for the first team member invite")
	fs.StringVar(&opts.InviteLabel, "label", "", "alias for --invite-label")
	fs.BoolVar(&opts.RequestedManagement, "management", false, "default management request for invited team members")
	fs.Var(&opts.RequestedScopes, "scope", "default team member scope permission scope=perm; may be repeated")
	fs.BoolVar(&opts.Yes, "yes", false, "accept safe default setup decisions")
	fs.BoolVar(&opts.DryRun, "dry-run", false, "show what would happen without writing files or creating an invite")
	fs.BoolVar(&opts.AgentGuidance, "agent-guidance", false, "create or update AGENTS.md guidance")
	fs.BoolVar(&opts.SkipAgentGuidance, "skip-agent-guidance", false, "skip AGENTS.md guidance")
	fs.StringVar(&opts.JoinMode, "join-mode", "auto", "with existing config: auto, request, or invite")
	fs.StringVar(&opts.InviteID, "invite-id", "", "with existing config: invite to redeem")
	fs.StringVar(&opts.InvitePIN, "pin", "", "with existing config: invite PIN for non-interactive invite join")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printQuickstartHelp(streams.Out)
			return ExitSuccess
		}
		cmdErr := commandError(ExitUsageError, "usage_error", "Invalid quickstart flags", err, "Run `propagate quickstart --help` for usage.")
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if fs.NArg() != 0 {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate quickstart does not accept positional arguments", nil)
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if opts.AgentGuidance && opts.SkipAgentGuidance {
		cmdErr := commandError(ExitUsageError, "usage_error", "--agent-guidance and --skip-agent-guidance cannot be used together", nil)
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}

	result, err := runQuickstart(opts, streams)
	if err != nil {
		return renderError(streams.Err, opts.JSON, opts.NoColor, err)
	}
	renderQuickstartResult(streams.Out, opts.JSON, opts.NoColor, result)
	return ExitSuccess
}

func runQuickstart(opts quickstartOptions, streams Streams) (QuickstartResult, error) {
	reader := bufio.NewReader(streams.In)
	existingConfig, err := quickstartHasProjectConfig(streams)
	if err != nil {
		return QuickstartResult{}, err
	}
	if existingConfig {
		return runQuickstartJoinExisting(opts, streams)
	}

	if !opts.DryRun && resolveAPIURL(opts.APIURL, streams.WorkDir) == "" {
		return QuickstartResult{}, commandError(
			ExitValidationError,
			"api_url_missing",
			"PROPAGATE_API_URL is required for quickstart",
			nil,
			"Pass `--api-url` or configure PROPAGATE_API_URL before running quickstart.",
		)
	}

	renderQuickstartStep(streams.Out, opts, 1, 2, "Setting up project", []string{
		"Create or reuse your local identity, discover env files, and prepare encrypted project metadata.",
	})
	initResult, err := runInitWithReader(initOptions{
		globalOptions:     opts.globalOptions,
		Handle:            opts.Handle,
		TeamName:          opts.TeamName,
		Yes:               opts.Yes,
		DryRun:            opts.DryRun,
		AgentGuidance:     opts.AgentGuidance,
		SkipAgentGuidance: opts.SkipAgentGuidance,
	}, streams, reader)
	if err != nil {
		return QuickstartResult{}, err
	}

	renderQuickstartStep(streams.Out, opts, 2, 2, "Inviting team", []string{
		"Create teammate invite codes and choose management plus scope access for each person.",
	})
	inviteResults, err := runQuickstartInviteLoop(opts, initResult, reader, streams)
	if err != nil {
		if cmdErr, ok := err.(*CommandError); ok && initResult.ProjectCreated && !opts.DryRun {
			cmdErr.NextSteps = append(cmdErr.NextSteps, "Project setup completed; after resolving the invite error, run `propagate team invite --label VALUE` to create teammate invites.")
		}
		return QuickstartResult{}, err
	}

	result := QuickstartResult{
		OK:      true,
		Command: "quickstart",
		Mode:    "setup_and_invite",
		Status:  "success",
		DryRun:  opts.DryRun,
		Init:    &initResult,
		Invites: inviteResults,
	}
	if len(inviteResults) > 0 {
		result.Invite = &result.Invites[0]
	}
	result.Warnings = append(result.Warnings, initResult.Warnings...)
	for _, inviteResult := range inviteResults {
		result.Warnings = append(result.Warnings, inviteResult.Warnings...)
	}
	result.NextSteps = quickstartNextSteps(result)
	if opts.DryRun {
		result.Status = "dry_run"
	}
	return result, nil
}

func runQuickstartJoinExisting(opts quickstartOptions, streams Streams) (QuickstartResult, error) {
	renderQuickstartStep(streams.Out, opts, 1, 1, "Joining team", []string{
		"Use this existing project config to request or redeem team access for your identity.",
	})
	joinResult, err := runTeamJoin(teamJoinOptions{
		globalOptions:       opts.globalOptions,
		Handle:              opts.Handle,
		RequestedManagement: opts.RequestedManagement,
		RequestedScopes:     opts.RequestedScopes,
		DryRun:              opts.DryRun,
		IncludeInit:         true,
		InitAgentGuidance:   opts.AgentGuidance,
		InitSkipGuidance:    opts.SkipAgentGuidance,
		JoinMode:            opts.JoinMode,
		InviteID:            opts.InviteID,
		InvitePIN:           opts.InvitePIN,
	}, streams)
	if err != nil {
		return QuickstartResult{}, err
	}
	result := QuickstartResult{
		OK:      true,
		Command: "quickstart",
		Mode:    "join_existing_project",
		Status:  joinResult.Status,
		DryRun:  opts.DryRun,
		Join:    &joinResult,
	}
	result.Warnings = append(result.Warnings, joinResult.Warnings...)
	result.NextSteps = append(result.NextSteps, joinResult.NextSteps...)
	return result, nil
}

func runQuickstartInviteLoop(opts quickstartOptions, initResult InitResult, reader *bufio.Reader, streams Streams) ([]TeamInviteCreateResult, error) {
	scopeNames := quickstartScopeNames(initResult)
	var inviteResults []TeamInviteCreateResult
	for inviteIndex := 1; ; inviteIndex++ {
		label := strings.TrimSpace(opts.InviteLabel)
		if inviteIndex > 1 || label == "" {
			var err error
			label, err = promptRequired(reader, streams.In, streams.Out, opts.NonInteractive, quickstartInviteLabelPrompt(inviteIndex))
			if err != nil {
				return nil, err
			}
		}

		management, inviteScopes, err := quickstartInviteAccess(opts, scopeNames, label, reader, streams)
		if err != nil {
			return nil, err
		}
		inviteResult, err := runTeamInviteCreateExec(teamInviteOptions{
			globalOptions:       opts.globalOptions,
			Label:               label,
			RequestedManagement: management,
			RequestedScopes:     inviteScopes,
			DryRun:              opts.DryRun,
		}, streams)
		if err != nil {
			return nil, err
		}
		inviteResults = append(inviteResults, inviteResult)

		if opts.NonInteractive {
			break
		}
		another, err := promptConfirm(reader, streams.In, streams.Out, "Invite another team member?", false)
		if err != nil {
			return nil, err
		}
		if !another {
			break
		}
		opts.InviteLabel = ""
	}
	return inviteResults, nil
}

func quickstartInviteLabelPrompt(inviteIndex int) string {
	if inviteIndex <= 1 {
		return "Team member invite label"
	}
	return fmt.Sprintf("Team member %d invite label", inviteIndex)
}

func quickstartHasProjectConfig(streams Streams) (bool, error) {
	worktree, err := gitutil.Discover(streams.WorkDir)
	if err != nil {
		return false, nil
	}
	_, exists, err := config.ExistingPath(worktree.Root)
	if err != nil {
		return false, commandError(ExitValidationError, "config_invalid", "Existing Propagate config needs attention", err, "Rename `propagate.yml` to `propagate.yaml` before running quickstart again.")
	}
	return exists, nil
}

func quickstartInviteAccess(opts quickstartOptions, scopeNames []string, label string, reader *bufio.Reader, streams Streams) (bool, scopeFlags, error) {
	if len(opts.RequestedScopes) > 0 {
		return opts.RequestedManagement, opts.RequestedScopes, nil
	}
	if opts.RequestedManagement {
		return true, quickstartDefaultInviteScopes(scopeNames, true), nil
	}

	defaults := quickstartDefaultInviteScopes(scopeNames, false)
	if opts.NonInteractive {
		return false, defaults, nil
	}
	if promptCanUseTUI(streams.In, streams.Out) {
		defaultAccess := quickstartScopeFlagMap(defaults)
		accessScopes := make([]tuiAccessScope, 0, len(scopeNames))
		for _, scope := range scopeNames {
			permission := defaultAccess[scope]
			if permission == "" {
				permission = "none"
			}
			accessScopes = append(accessScopes, tuiAccessScope{
				Name:       scope,
				Permission: permission,
			})
		}
		access, err := promptAccessTUI(streams.In, streams.Out, "Access for "+label, []string{
			"Choose what this invite can do when it is redeemed.",
		}, false, accessScopes, true)
		if err != nil {
			return false, nil, err
		}
		return access.Management, quickstartScopeFlagsFromMap(scopeNames, access.Scopes), nil
	}

	management, err := promptConfirm(reader, streams.In, streams.Out, "Grant management access to "+label+"?", false)
	if err != nil {
		return false, nil, err
	}
	defaults = quickstartDefaultInviteScopes(scopeNames, management)
	for {
		input, err := promptOptionalLine(reader, streams.Out, "Scope access for "+label+" (comma-separated scope=read|write|none, default "+strings.Join(defaults, ", ")+")")
		if err != nil {
			return false, nil, err
		}
		if strings.TrimSpace(input) == "" {
			return management, defaults, nil
		}
		out, err := quickstartParseInviteScopes(input)
		if err != nil {
			return false, nil, err
		}
		if management || len(out) > 0 {
			return management, out, nil
		}
		fmt.Fprintln(streams.Out, "Grant management or at least one scope, or press Ctrl+C to cancel.")
	}
}

func quickstartScopeNames(initResult InitResult) []string {
	seen := map[string]bool{}
	var names []string
	for _, scope := range initResult.ScopesCreated {
		scope = strings.TrimSpace(scope)
		if scope == "" || seen[scope] {
			continue
		}
		seen[scope] = true
		names = append(names, scope)
	}
	for _, file := range initResult.EnvFilesMapped {
		scope := defaultInitScope(file.Scope)
		if seen[scope] {
			continue
		}
		seen[scope] = true
		names = append(names, scope)
	}
	if len(names) == 0 {
		names = []string{"dev"}
	}
	return names
}

func quickstartDefaultInviteScopes(scopeNames []string, management bool) scopeFlags {
	if management {
		if len(scopeNames) == 0 {
			return scopeFlags{"dev=write"}
		}
		out := make(scopeFlags, 0, len(scopeNames))
		for _, scope := range scopeNames {
			out = append(out, scope+"=write")
		}
		return out
	}
	return quickstartDefaultDeveloperScopes(scopeNames)
}

func quickstartDefaultDeveloperScopes(scopeNames []string) scopeFlags {
	if len(scopeNames) == 0 {
		return scopeFlags{"dev=read"}
	}
	for _, scope := range scopeNames {
		if scope == "dev" {
			return scopeFlags{"dev=read"}
		}
	}
	return scopeFlags{scopeNames[0] + "=read"}
}

func quickstartScopeFlagMap(flags scopeFlags) map[string]string {
	out := map[string]string{}
	for _, item := range flags {
		name, permission, ok := strings.Cut(item, "=")
		if !ok {
			permission = "read"
		}
		name = strings.TrimSpace(name)
		permission = strings.TrimSpace(permission)
		if name != "" && permission != "" && permission != "none" {
			out[name] = permission
		}
	}
	return out
}

func quickstartScopeFlagsFromMap(scopeNames []string, scopes map[string]string) scopeFlags {
	out := make(scopeFlags, 0, len(scopes))
	seen := map[string]bool{}
	if len(scopeNames) == 0 {
		scopeNames = sortedScopeNames(scopes)
	}
	for _, scope := range scopeNames {
		if permission := scopes[scope]; permission != "" && permission != "none" {
			out = append(out, scope+"="+permission)
			seen[scope] = true
		}
	}
	for scope, permission := range scopes {
		if seen[scope] || permission == "" || permission == "none" {
			continue
		}
		out = append(out, scope+"="+permission)
	}
	return out
}

func quickstartParseInviteScopes(input string) (scopeFlags, error) {
	var out scopeFlags
	for _, part := range strings.Split(input, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "=") {
			part += "=read"
		}
		name, permission, _ := strings.Cut(part, "=")
		if strings.TrimSpace(permission) == "none" {
			if err := out.Set(strings.TrimSpace(name) + "=none"); err != nil {
				return nil, commandError(ExitUsageError, "usage_error", "Invalid invite scope", err)
			}
			continue
		}
		if err := out.Set(part); err != nil {
			return nil, commandError(ExitUsageError, "usage_error", "Invalid invite scope", err)
		}
	}
	if len(out) == 0 {
		return out, nil
	}
	scopeMap, err := out.Map()
	if err != nil {
		return nil, commandError(ExitUsageError, "usage_error", "Invalid invite scope", err)
	}
	return quickstartScopeFlagsFromMap(nil, scopeMap), nil
}

func quickstartNextSteps(result QuickstartResult) []string {
	if result.DryRun {
		return []string{"Re-run without `--dry-run` to set up the project and create teammate invites."}
	}
	pinStep := "Share the PIN with the teammate through a trusted channel."
	if len(result.Invites) > 1 {
		pinStep = "Share each PIN with its teammate through a trusted channel."
	}
	steps := []string{
		pinStep,
		"They can run `propagate team join` and choose Join by invite code.",
	}
	if result.Init != nil && (result.Init.ProjectCreated || result.Init.AgentGuidance.Status == "created" || result.Init.AgentGuidance.Status == "updated") {
		steps = append(steps, "Commit the reviewed propagate.yaml or AGENTS.md changes when ready.")
	}
	return steps
}

func renderQuickstartStep(w io.Writer, opts quickstartOptions, index int, total int, title string, description []string) {
	if opts.JSON {
		return
	}
	style := newOutputStyle(opts.NoColor)
	fmt.Fprintf(w, "%s\n", style.bold(fmt.Sprintf("[%d/%d] %s", index, total, title)))
	for _, line := range description {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fmt.Fprintf(w, "%s %s\n", style.note(), line)
	}
	fmt.Fprintln(w)
}

func renderQuickstartResult(w io.Writer, jsonOutput bool, noColor bool, result QuickstartResult) {
	if result.Join != nil {
		renderTeamJoinResultWithTitle(w, jsonOutput, noColor, *result.Join, "Joining team")
		return
	}
	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}

	style := newOutputStyle(noColor)
	renderCommandTitle(w, style, "Project setup & team invites", result.DryRun)
	if result.Init == nil || len(result.Invites) == 0 {
		renderNote(w, style, "No quickstart work was performed.")
		renderWarnings(w, style, result.Warnings)
		renderNextSteps(w, style, result.NextSteps)
		return
	}
	if result.Init.ProjectAlreadyConfigured {
		renderNote(w, style, "Project already configured.")
	} else if result.Init.ProjectCreated {
		if result.DryRun {
			renderOK(w, style, "Project setup preview complete.")
		} else {
			renderOK(w, style, "Project setup complete.")
		}
	}
	if result.Init.ProjectConfigPath != "" {
		fmt.Fprintf(w, "Project config: %s\n", displayInitPath(result.Init.ProjectConfigPath))
	}
	if result.Init.VariablesDiscoveredCount > 0 {
		fmt.Fprintf(w, "Variables discovered: %d\n", result.Init.VariablesDiscoveredCount)
	}
	if result.Init.BackendStatus == "created" || result.DryRun {
		fmt.Fprintf(w, "Setup backend: %s\n", result.Init.BackendStatus)
	}
	fmt.Fprintln(w)
	if result.DryRun {
		renderNote(w, style, quickstartInviteCountLine(result.Invites, "would be created"))
	} else {
		renderOK(w, style, quickstartInviteCountLine(result.Invites, "created"))
		renderNote(w, style, "Share each PIN out of band. It will not be shown again.")
	}
	renderQuickstartInvites(w, style, result.Invites)
	renderWarnings(w, style, result.Warnings)
	renderNextSteps(w, style, result.NextSteps)
}

func quickstartInviteCountLine(invites []TeamInviteCreateResult, suffix string) string {
	if len(invites) == 1 {
		return "Team member invite " + suffix + "."
	}
	return fmt.Sprintf("%d team member invites %s.", len(invites), suffix)
}

func renderQuickstartInvites(w io.Writer, style outputStyle, invites []TeamInviteCreateResult) {
	for idx, invite := range invites {
		if len(invites) > 1 {
			fmt.Fprintf(w, "\n%s\n", style.bold(fmt.Sprintf("Invite %d", idx+1)))
		}
		if invite.InviteID != "" {
			fmt.Fprintf(w, "Invite id: %s\n", invite.InviteID)
		}
		if invite.PIN != "" {
			fmt.Fprintf(w, "PIN: %s\n", invite.PIN)
		}
		if invite.Label != "" {
			fmt.Fprintf(w, "Label: %s\n", invite.Label)
		}
		renderInviteAccess(w, style, invite.RequestedManagement, invite.RequestedScopes)
	}
}

func printQuickstartHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate quickstart [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "If propagate.yaml exists, quickstart behaves like `propagate team join --init`.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --handle VALUE           handle for a new local identity")
	fmt.Fprintln(w, "  --team-name VALUE        team name for new project setup")
	fmt.Fprintln(w, "  --invite-label VALUE     label for the first team member invite")
	fmt.Fprintln(w, "  --label VALUE            alias for --invite-label")
	fmt.Fprintln(w, "  --management             grant management access when the invite is redeemed")
	fmt.Fprintln(w, "  --scope VALUE            invite scope permission scope=perm; prompts/defaults when omitted")
	fmt.Fprintln(w, "  --yes                    accept safe default setup decisions")
	fmt.Fprintln(w, "  --dry-run                show what would happen without writing files or creating an invite")
	fmt.Fprintln(w, "  --agent-guidance         create or update AGENTS.md guidance")
	fmt.Fprintln(w, "  --skip-agent-guidance    skip AGENTS.md guidance")
	fmt.Fprintln(w, "  --join-mode MODE         with existing config: auto|request|invite")
	fmt.Fprintln(w, "  --invite-id ID           with existing config: invite to redeem")
	fmt.Fprintln(w, "  --pin VALUE              with existing config: invite PIN for non-interactive invite join")
	fmt.Fprintln(w, "  --json                   render machine-readable JSON")
	fmt.Fprintln(w, "  --non-interactive        fail instead of prompting")
	fmt.Fprintln(w, "  --no-color               disable terminal color")
}

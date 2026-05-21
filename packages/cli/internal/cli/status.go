package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"propagate/cli/internal/config"
)

type statusOptions struct {
	globalOptions
	Scope string
}

type StatusResult struct {
	OK        bool                 `json:"ok"`
	Command   string               `json:"command"`
	Status    string               `json:"status"`
	Scope     string               `json:"scope"`
	Config    *ConfigStatusResult  `json:"config,omitempty"`
	Team      *TeamStatusResult    `json:"team,omitempty"`
	Env       *EnvStatusResult     `json:"env,omitempty"`
	Errors    []StatusSectionError `json:"errors,omitempty"`
	NextSteps []string             `json:"next_steps,omitempty"`
}

type StatusSectionError struct {
	Command string        `json:"command"`
	Error   *CommandError `json:"error"`
}

func runStatusCommand(args []string, global globalOptions, streams Streams) int {
	opts := statusOptions{globalOptions: global, Scope: "dev"}
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &opts.globalOptions)
	fs.StringVar(&opts.Scope, "scope", "dev", "scope to inspect for env status")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printStatusHelp(streams.Out)
			return ExitSuccess
		}
		cmdErr := commandError(ExitUsageError, "usage_error", "Invalid status flags", err, "Run `propagate status --help` for usage.")
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if fs.NArg() != 0 {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate status does not accept positional arguments", nil)
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	opts.Scope = strings.TrimSpace(opts.Scope)
	if opts.Scope == "" {
		opts.Scope = "dev"
	}
	if err := config.ValidateScopeName(opts.Scope); err != nil {
		cmdErr := commandError(ExitUsageError, "usage_error", "Invalid status scope", err)
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}

	result := runStatus(opts, streams)
	renderStatusResult(streams.Out, opts.JSON, opts.NoColor, result)
	if len(result.Errors) > 0 {
		if !opts.JSON {
			renderStatusErrors(streams.Err, opts.NoColor, result.Errors)
		}
		return statusExitCode(result.Errors)
	}
	return ExitSuccess
}

func runStatus(opts statusOptions, streams Streams) StatusResult {
	result := StatusResult{
		OK:      true,
		Command: "status",
		Status:  "success",
		Scope:   opts.Scope,
	}

	configResult, configErr := runConfigStatus(configStatusOptions{globalOptions: opts.globalOptions}, streams)
	if configErr != nil {
		if resultHasLocalConfigFacts(configResult) {
			result.Config = &configResult
		}
		appendStatusError(&result, "config status", configErr)
	} else {
		result.Config = &configResult
	}

	teamResult, teamErr := runTeamStatus(teamStatusOptions{globalOptions: opts.globalOptions}, streams)
	if teamErr != nil {
		if teamStatusHasLocalFacts(teamResult) {
			result.Team = &teamResult
		}
		appendStatusError(&result, "team status", teamErr)
	} else {
		result.Team = &teamResult
	}

	envResult, envErr := runEnvStatus(envStatusOptions{globalOptions: opts.globalOptions, Scope: opts.Scope}, streams)
	if envErr != nil {
		appendStatusError(&result, "env status", envErr)
	} else {
		result.Env = &envResult
	}

	if len(result.Errors) > 0 {
		result.OK = false
		if statusHasSectionResults(result) {
			result.Status = "partial"
		} else {
			result.Status = "failed"
		}
	}
	result.NextSteps = statusNextSteps(result)
	return result
}

func appendStatusError(result *StatusResult, command string, err error) {
	cmdErr, ok := err.(*CommandError)
	if !ok {
		cmdErr = commandError(ExitInternalError, "internal_error", "Unexpected internal error", err)
	}
	result.Errors = append(result.Errors, StatusSectionError{
		Command: command,
		Error:   cmdErr,
	})
}

func statusHasSectionResults(result StatusResult) bool {
	return result.Config != nil || result.Team != nil || result.Env != nil
}

func statusNextSteps(result StatusResult) []string {
	var steps []string
	if result.Config != nil {
		steps = append(steps, result.Config.NextSteps...)
	}
	if result.Team != nil {
		steps = append(steps, result.Team.NextSteps...)
	}
	if result.Env != nil {
		steps = append(steps, result.Env.NextSteps...)
	}
	for _, item := range result.Errors {
		if item.Error != nil {
			steps = append(steps, item.Error.NextSteps...)
		}
	}
	return uniqueStrings(steps)
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func renderStatusResult(w io.Writer, jsonOutput bool, noColor bool, result StatusResult) {
	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}

	style := newOutputStyle(noColor)

	if result.OK && statusFullyInSync(result) {
		renderCompactStatus(w, style, result)
		return
	}

	renderCommandTitle(w, style, "Status", false)
	if statusHasSectionResults(result) {
		renderWarning(w, style, "Unified status incomplete; available sections are shown.")
	} else {
		renderWarning(w, style, "Unified status failed.")
	}

	if result.Config != nil {
		fmt.Fprintln(w)
		renderConfigStatusResult(w, false, noColor, *result.Config)
	}
	if result.Team != nil {
		fmt.Fprintln(w)
		renderTeamStatusResult(w, false, noColor, *result.Team)
	}
	if result.Env != nil {
		fmt.Fprintln(w)
		renderEnvStatusResult(w, false, noColor, *result.Env)
	}
}

func statusFullyInSync(result StatusResult) bool {
	if result.Config == nil || result.Team == nil || result.Env == nil {
		return false
	}
	if !configStatusInSync(*result.Config) {
		return false
	}
	if !teamStatusInSync(*result.Team) {
		return false
	}
	if result.Env.Status != "success" && result.Env.Status != "no_change" {
		return false
	}
	if result.Env.ConfigStale {
		return false
	}
	if envStatusHasLocalDrift(result.Env.Variables) {
		return false
	}
	return true
}

func renderCompactStatus(w io.Writer, style outputStyle, result StatusResult) {
	renderCommandTitle(w, style, "Status", false)

	configRes := result.Config
	teamRes := result.Team
	envRes := result.Env

	teamLine := ""
	if configRes.TeamName != "" {
		teamLine = fmt.Sprintf("%s (%s)", configRes.TeamName, configRes.TeamID)
	} else if configRes.TeamID != "" {
		teamLine = configRes.TeamID
	}

	youLine := ""
	if configRes.Identity != nil {
		youLine = configRes.Identity.Handle
		if youLine == "" {
			youLine = configRes.Identity.PublicKeySHA
		}
		if teamRes.CurrentManagement {
			youLine += " [management]"
		}
	}

	configLine := fmt.Sprintf("%s (in sync)", valueOrDash(configRes.LocalRevision))

	scopeLine := result.Scope
	if envRes.CanRead {
		scopeLine += " (read access)"
	} else {
		scopeLine += " (no read access)"
	}

	labelWidth := 7 // "Config:" / "Scope:" / "Team:" / "You:"
	printRow := func(label, value string) {
		if value == "" {
			return
		}
		fmt.Fprintf(w, "%-*s %s\n", labelWidth, label+":", value)
	}
	printRow("Team", teamLine)
	printRow("You", youLine)
	printRow("Config", configLine)
	printRow("Scope", scopeLine)

	handleBySHA := teamHandleBySHA(teamRes.Members)
	if countTeamMembers(teamRes.Members) > 0 {
		fmt.Fprintln(w)
		fmt.Fprintln(w, style.bold("Members"))
		youSHA := ""
		if configRes.Identity != nil {
			youSHA = configRes.Identity.PublicKeySHA
		}
		for _, role := range orderedTeamRoles(teamRes.Members) {
			fmt.Fprintf(w, "  %s\n", role)
			for _, member := range teamRes.Members[role] {
				label := compactMemberLabel(member.Handle, member.PublicKeySHA)
				if youSHA != "" && member.PublicKeySHA == youSHA {
					label += " (you)"
				}
				if member.Status != "" && member.Status != "active" {
					label += " [" + member.Status + "]"
				}
				fmt.Fprintf(w, "    %s\n", label)
			}
		}
	}

	if len(envRes.Variables) > 0 {
		fmt.Fprintln(w)
		header := fmt.Sprintf("Variables (%d", len(envRes.Variables))
		if envRes.LastUpdated != nil && envRes.LastUpdated.At != "" {
			header += ", last update " + formatStatusTimestamp(envRes.LastUpdated.At)
			if envRes.LastUpdated.By != "" {
				header += " by " + compactMemberLabel(handleBySHA[envRes.LastUpdated.By], envRes.LastUpdated.By)
			}
		}
		header += ")"
		fmt.Fprintln(w, style.bold(header))

		nameWidth, valueWidth := 0, 0
		for _, v := range envRes.Variables {
			if len(v.Name) > nameWidth {
				nameWidth = len(v.Name)
			}
			if len(v.MaskedValue) > valueWidth {
				valueWidth = len(v.MaskedValue)
			}
		}
		for _, v := range envRes.Variables {
			fmt.Fprintf(w, "  %-*s  %-*s  %s\n", nameWidth, v.Name, valueWidth, v.MaskedValue, v.Path)
		}
	}

	fmt.Fprintln(w)
	renderOK(w, style, "Everything in sync.")
}

func renderStatusErrors(w io.Writer, noColor bool, errors []StatusSectionError) {
	style := newOutputStyle(noColor)
	for _, item := range errors {
		if item.Error == nil {
			continue
		}
		fmt.Fprintf(w, "%s %s failed: %s\n", style.warn(), item.Command, item.Error.Message)
		if item.Error.Err != nil {
			fmt.Fprintf(w, "Detail: %s\n", item.Error.Err)
		}
		renderNextSteps(w, style, item.Error.NextSteps)
	}
}

func statusExitCode(errors []StatusSectionError) int {
	for _, item := range errors {
		if item.Error != nil {
			return item.Error.Code
		}
	}
	return ExitSuccess
}

func compactMemberLabel(handle, sha string) string {
	handle = strings.TrimSpace(handle)
	if handle != "" {
		return handle
	}
	return sha
}

func teamHandleBySHA(members map[string][]TeamMember) map[string]string {
	out := map[string]string{}
	for _, group := range members {
		for _, m := range group {
			if m.Handle != "" {
				out[m.PublicKeySHA] = m.Handle
			}
		}
	}
	return out
}

func formatStatusTimestamp(value string) string {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return t.Local().Format("2006-01-02 15:04")
}

func printStatusHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate status [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Runs config status, team status, and env status for one scope.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --scope VALUE       scope to inspect for env status (default dev)")
	fmt.Fprintln(w, "  --api-url VALUE     override Propagate API URL")
	fmt.Fprintln(w, "  --json              render one machine-readable JSON status envelope")
	fmt.Fprintln(w, "  --non-interactive   fail instead of prompting")
}

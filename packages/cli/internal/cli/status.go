package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

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
	renderCommandTitle(w, style, "Propagate status", false)
	switch {
	case result.OK:
		renderOK(w, style, "Unified status complete.")
	case statusHasSectionResults(result):
		renderWarning(w, style, "Unified status incomplete; available sections are shown.")
	default:
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

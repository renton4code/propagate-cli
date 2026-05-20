package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"propagate/cli/internal/config"
	"propagate/cli/internal/envfile"
	"propagate/cli/internal/gitutil"
)

type configEditOptions struct {
	globalOptions
	DryRun bool
}

type ConfigEditResult struct {
	OK                    bool                       `json:"ok"`
	Command               string                     `json:"command"`
	Status                string                     `json:"status"`
	DryRun                bool                       `json:"dry_run"`
	ProjectConfigPath     string                     `json:"project_config_path"`
	TeamID                string                     `json:"team_id"`
	TeamName              string                     `json:"team_name"`
	VariablesBeforeCount  int                        `json:"variables_before_count"`
	VariablesAfterCount   int                        `json:"variables_after_count"`
	VariablesChangedCount int                        `json:"variables_changed_count"`
	VariablesMovedCount   int                        `json:"variables_moved_count"`
	VariablesRemovedCount int                        `json:"variables_removed_count"`
	EnvFileMappingsAdded  []ConfigEditEnvFileMapping `json:"env_file_mappings_added,omitempty"`
	Changes               []ConfigEditVariableChange `json:"changes,omitempty"`
	ConfigModified        bool                       `json:"config_modified"`
	Warnings              []string                   `json:"warnings,omitempty"`
	NextSteps             []string                   `json:"next_steps,omitempty"`
}

type ConfigEditEnvFileMapping struct {
	Scope       string `json:"scope"`
	EnvFilePath string `json:"env_file_path"`
}

type ConfigEditVariableChange struct {
	Action         string `json:"action"`
	Name           string `json:"name"`
	EnvFilePath    string `json:"env_file_path"`
	OldScope       string `json:"old_scope,omitempty"`
	NewScope       string `json:"new_scope,omitempty"`
	OldSensitivity string `json:"old_sensitivity,omitempty"`
	NewSensitivity string `json:"new_sensitivity,omitempty"`
}

type configEditVariableRef struct {
	ScopeIndex    int
	VariableIndex int
}

type configEditVariableItem struct {
	configEditVariableRef
	Number   int
	Scope    string
	Variable config.VariableDeclaration
}

func runConfigEditCommand(args []string, global globalOptions, streams Streams) int {
	opts := configEditOptions{globalOptions: global}
	fs := flag.NewFlagSet("config edit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	addGlobalFlags(fs, &opts.globalOptions)
	fs.BoolVar(&opts.DryRun, "dry-run", false, "preview config edits without writing propagate.yaml")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printConfigEditHelp(streams.Out)
			return ExitSuccess
		}
		cmdErr := commandError(ExitUsageError, "usage_error", "Invalid config edit flags", err, "Run `propagate config edit --help` for usage.")
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}
	if fs.NArg() != 0 {
		cmdErr := commandError(ExitUsageError, "usage_error", "propagate config edit does not accept positional arguments", nil)
		return renderError(streams.Err, opts.JSON, opts.NoColor, cmdErr)
	}

	result, err := runConfigEdit(opts, streams)
	if err != nil {
		return renderError(streams.Err, opts.JSON, opts.NoColor, err)
	}
	renderConfigEditResult(streams.Out, opts.JSON, opts.NoColor, result)
	return ExitSuccess
}

func runConfigEdit(opts configEditOptions, streams Streams) (ConfigEditResult, error) {
	if opts.NonInteractive {
		return ConfigEditResult{}, commandError(
			ExitConfirmationRequired,
			"confirmation_required",
			"config edit requires interactive input",
			nil,
			"Run `propagate config edit` in an interactive terminal, or edit propagate.yaml in reviewable metadata-only form.",
		)
	}

	result := ConfigEditResult{
		OK:      true,
		Command: "config edit",
		Status:  "success",
		DryRun:  opts.DryRun,
	}

	worktree, err := gitutil.Discover(streams.WorkDir)
	if err != nil {
		return ConfigEditResult{}, commandError(ExitValidationError, "not_git_repo", "Cannot edit config outside a Git worktree", err)
	}
	configPath, exists, err := config.ExistingPath(worktree.Root)
	if err != nil {
		return ConfigEditResult{}, commandError(ExitValidationError, "config_invalid", "Existing Propagate config needs attention", err, "Rename `propagate.yml` to `propagate.yaml` before running config edit again.")
	}
	if !exists {
		return ConfigEditResult{}, commandError(ExitValidationError, "config_missing", "propagate.yaml is required before config edit", nil, "Run `propagate init` or pull the repository config first.")
	}
	result.ProjectConfigPath = configPath

	project, err := config.ReadProject(configPath)
	if err != nil {
		return ConfigEditResult{}, commandError(ExitValidationError, "config_invalid", "Cannot read propagate.yaml", err)
	}
	result.TeamID = project.TeamID
	result.TeamName = project.TeamName
	result.VariablesBeforeCount = countConfigVariables(project)
	result.VariablesAfterCount = result.VariablesBeforeCount

	if result.VariablesBeforeCount == 0 {
		result.Status = "no_change"
		result.NextSteps = []string{"No variable declarations are present in propagate.yaml yet."}
		return result, nil
	}

	target := cloneParsedProjectForConfigEdit(project)
	reader := bufio.NewReader(streams.In)
	changes, mappingsAdded, err := runConfigEditLoopWithRoot(worktree.Root, reader, streams.In, streams.Out, &target)
	if err != nil {
		return ConfigEditResult{}, err
	}
	result.Changes = changes
	result.EnvFileMappingsAdded = mappingsAdded
	applyConfigEditStats(&result)
	result.VariablesAfterCount = countConfigVariables(target)

	rendered, err := config.RenderParsed(target)
	if err != nil {
		return ConfigEditResult{}, commandError(ExitValidationError, "config_invalid", "Cannot render edited propagate.yaml", err)
	}
	result.ConfigModified = rendered != project.Raw
	if !result.ConfigModified {
		result.Status = "no_change"
		result.NextSteps = []string{"No config changes were saved."}
		return result, nil
	}
	if opts.DryRun {
		result.Status = "dry_run"
		result.NextSteps = []string{"Re-run without `--dry-run` to write the reviewed config edits."}
		return result, nil
	}
	if err := config.WriteRaw(configPath, rendered); err != nil {
		return ConfigEditResult{}, commandError(ExitPartialLocalFailure, "partial_local_failure", "Could not write edited propagate.yaml", err)
	}
	result.NextSteps = []string{
		"Run `propagate config status` to compare the edited metadata with cloud state.",
		"Run `propagate config push` after review to publish accepted config changes.",
	}
	return result, nil
}

func runConfigEditLoop(reader *bufio.Reader, out io.Writer, project *config.ParsedProject) ([]ConfigEditVariableChange, []ConfigEditEnvFileMapping, error) {
	return runConfigEditLoopWithRoot("", reader, nil, out, project)
}

func runConfigEditLoopWithRoot(root string, reader *bufio.Reader, in io.Reader, out io.Writer, project *config.ParsedProject) ([]ConfigEditVariableChange, []ConfigEditEnvFileMapping, error) {
	var changes []ConfigEditVariableChange
	var mappingsAdded []ConfigEditEnvFileMapping
	for {
		items := editableConfigVariables(*project)
		action, index, err := promptConfigEditMainAction(reader, in, out, *project, items)
		if err != nil {
			return nil, nil, err
		}
		switch action {
		case "save":
			return changes, mappingsAdded, nil
		case "cancel":
			return nil, nil, commandError(ExitUserCanceled, "user_canceled", "Config edit was canceled before saving", nil)
		case "continue":
			continue
		}
		ref := items[index].configEditVariableRef
		if err := runConfigEditVariableMenuWithRoot(root, reader, in, out, project, ref, &changes, &mappingsAdded); err != nil {
			return nil, nil, err
		}
	}
}

func runConfigEditVariableMenu(reader *bufio.Reader, out io.Writer, project *config.ParsedProject, ref configEditVariableRef, changes *[]ConfigEditVariableChange, mappingsAdded *[]ConfigEditEnvFileMapping) error {
	return runConfigEditVariableMenuWithRoot("", reader, nil, out, project, ref, changes, mappingsAdded)
}

func runConfigEditVariableMenuWithRoot(root string, reader *bufio.Reader, in io.Reader, out io.Writer, project *config.ParsedProject, ref configEditVariableRef, changes *[]ConfigEditVariableChange, mappingsAdded *[]ConfigEditEnvFileMapping) error {
	for {
		variable, scopeName, ok := configEditVariable(project, ref)
		if !ok {
			fmt.Fprintln(out, "That variable is no longer available.")
			return nil
		}
		action, err := promptConfigEditVariableAction(reader, in, out, scopeName, variable)
		if err != nil {
			return err
		}
		switch action {
		case "back":
			return nil
		case "toggle":
			target := &project.Scopes[ref.ScopeIndex].Variables[ref.VariableIndex]
			oldSensitivity := effectiveConfigSensitivity(target.Sensitivity)
			newSensitivity := config.SensitivityNonSensitive
			if oldSensitivity == config.SensitivityNonSensitive {
				newSensitivity = config.SensitivitySensitive
				target.Literal = ""
				target.Preview = ""
			} else {
				literal, err := configEditLiteralValue(root, *target)
				if err != nil {
					fmt.Fprintf(out, "Cannot mark %s non-sensitive: %s\n", target.Name, err)
					continue
				}
				target.Digest = ""
				target.Literal = literal
				target.Preview = ""
			}
			target.Sensitivity = newSensitivity
			*changes = append(*changes, ConfigEditVariableChange{
				Action:         "sensitivity",
				Name:           target.Name,
				EnvFilePath:    target.EnvFilePath,
				OldScope:       scopeName,
				NewScope:       scopeName,
				OldSensitivity: oldSensitivity,
				NewSensitivity: newSensitivity,
			})
			fmt.Fprintf(out, "Updated %s sensitivity: %s -> %s\n", target.Name, oldSensitivity, newSensitivity)
		case "move":
			targetScope, ok, err := promptConfigEditScope(reader, in, out, *project, scopeName)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			moved, mappingAdded, err := moveConfigEditVariable(project, ref, targetScope)
			if err != nil {
				fmt.Fprintf(out, "Cannot move variable: %s\n", err)
				continue
			}
			*changes = append(*changes, ConfigEditVariableChange{
				Action:         "move",
				Name:           moved.Name,
				EnvFilePath:    moved.EnvFilePath,
				OldScope:       scopeName,
				NewScope:       targetScope,
				OldSensitivity: effectiveConfigSensitivity(moved.Sensitivity),
				NewSensitivity: effectiveConfigSensitivity(moved.Sensitivity),
			})
			if mappingAdded {
				*mappingsAdded = append(*mappingsAdded, ConfigEditEnvFileMapping{Scope: targetScope, EnvFilePath: moved.EnvFilePath})
				fmt.Fprintf(out, "Added %s to scope %s env file mappings.\n", moved.EnvFilePath, targetScope)
			}
			fmt.Fprintf(out, "Moved %s to scope %s.\n", moved.Name, targetScope)
			return nil
		case "remove":
			ok, err := promptConfirm(reader, in, out, fmt.Sprintf("Remove %s from scope %s config metadata?", variable.Name, scopeName), false)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			removed, oldScope, ok := removeConfigEditVariable(project, ref)
			if !ok {
				fmt.Fprintln(out, "That variable is no longer available.")
				return nil
			}
			*changes = append(*changes, ConfigEditVariableChange{
				Action:         "remove",
				Name:           removed.Name,
				EnvFilePath:    removed.EnvFilePath,
				OldScope:       oldScope,
				OldSensitivity: effectiveConfigSensitivity(removed.Sensitivity),
			})
			fmt.Fprintf(out, "Removed %s from scope %s.\n", removed.Name, oldScope)
			return nil
		default:
			continue
		}
	}
}

func configEditLiteralValue(root string, variable config.VariableDeclaration) (string, error) {
	absPath, err := repoFilePath(root, variable.EnvFilePath)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("local env file %s is missing", variable.EnvFilePath)
		}
		return "", fmt.Errorf("cannot inspect local env file %s: %w", variable.EnvFilePath, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("local env file %s is a directory", variable.EnvFilePath)
	}
	parsed, err := envfile.ParseAssignments(absPath)
	if err != nil {
		return "", fmt.Errorf("cannot parse local env file %s: %w", variable.EnvFilePath, err)
	}
	if containsString(parsed.DuplicateVariables, variable.Name) {
		return "", fmt.Errorf("%s has duplicate assignments in %s", variable.Name, variable.EnvFilePath)
	}
	value, ok := parsed.Values[variable.Name]
	if !ok {
		return "", fmt.Errorf("%s is not present in %s", variable.Name, variable.EnvFilePath)
	}
	if !isShortOneLine(value) {
		return "", fmt.Errorf("%s is not a short one-line value", variable.Name)
	}
	return value, nil
}

func renderConfigEditScreen(w io.Writer, project config.ParsedProject, items []configEditVariableItem) {
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Config variable editor: %s (%s)\n", project.TeamName, project.TeamID)
	fmt.Fprintln(w, "Variables:")
	if len(items) == 0 {
		fmt.Fprintln(w, "  No variable declarations remain.")
		return
	}
	for _, item := range items {
		fmt.Fprintf(
			w,
			"  %d. scope=%s file=%s name=%s sensitivity=%s\n",
			item.Number,
			item.Scope,
			item.Variable.EnvFilePath,
			item.Variable.Name,
			effectiveConfigSensitivity(item.Variable.Sensitivity),
		)
	}
}

func renderConfigEditVariable(w io.Writer, scopeName string, variable config.VariableDeclaration) {
	fmt.Fprintln(w)
	fmt.Fprintf(w, "Editing %s\n", variable.Name)
	fmt.Fprintf(w, "Scope: %s\n", scopeName)
	fmt.Fprintf(w, "Env file: %s\n", variable.EnvFilePath)
	fmt.Fprintf(w, "Sensitivity: %s\n", effectiveConfigSensitivity(variable.Sensitivity))
}

func promptConfigEditMainAction(reader *bufio.Reader, in io.Reader, out io.Writer, project config.ParsedProject, items []configEditVariableItem) (string, int, error) {
	if promptCanUseTUI(in, out) {
		choices := make([]tuiChoice, 0, len(items)+2)
		for idx, item := range items {
			key := ""
			if idx < 9 {
				key = strconv.Itoa(idx + 1)
			}
			choices = append(choices, tuiChoice{
				Key:   key,
				Label: fmt.Sprintf("%s / %s / %s", item.Scope, item.Variable.EnvFilePath, item.Variable.Name),
				Description: fmt.Sprintf(
					"sensitivity=%s",
					effectiveConfigSensitivity(item.Variable.Sensitivity),
				),
				Value: fmt.Sprintf("variable:%d", idx),
			})
		}
		choices = append(choices,
			tuiChoice{Key: "s", Label: "Save changes", Value: "save"},
			tuiChoice{Key: "q", Label: "Cancel without saving", Value: "cancel"},
		)
		selected, err := promptChoiceTUI(
			in,
			out,
			"Config edit",
			[]string{
				fmt.Sprintf("Team: %s", project.TeamName),
				fmt.Sprintf("Variables: %d", len(items)),
			},
			choices,
			0,
		)
		if err != nil {
			return "", 0, err
		}
		switch selected {
		case "save", "cancel":
			return selected, 0, nil
		default:
			indexText := strings.TrimPrefix(selected, "variable:")
			index, err := strconv.Atoi(indexText)
			if err != nil || index < 0 || index >= len(items) {
				return "continue", 0, nil
			}
			return "select", index, nil
		}
	}

	renderConfigEditScreen(out, project, items)
	input, err := promptConfigEditLine(reader, out, "Select a variable number, s to save, or q to cancel")
	if err != nil {
		return "", 0, err
	}
	switch strings.ToLower(input) {
	case "s", "save":
		return "save", 0, nil
	case "q", "quit", "cancel":
		return "cancel", 0, nil
	case "":
		return "continue", 0, nil
	}
	number, err := strconv.Atoi(input)
	if err != nil || number < 1 || number > len(items) {
		fmt.Fprintln(out, "Choose a listed variable number, s, or q.")
		return "continue", 0, nil
	}
	return "select", number - 1, nil
}

func promptConfigEditVariableAction(reader *bufio.Reader, in io.Reader, out io.Writer, scopeName string, variable config.VariableDeclaration) (string, error) {
	if promptCanUseTUI(in, out) {
		return promptChoiceTUI(
			in,
			out,
			"Editing "+variable.Name,
			[]string{
				fmt.Sprintf("Scope: %s", scopeName),
				fmt.Sprintf("Env file: %s", variable.EnvFilePath),
				fmt.Sprintf("Sensitivity: %s", effectiveConfigSensitivity(variable.Sensitivity)),
			},
			[]tuiChoice{
				{Key: "t", Label: "Toggle sensitivity", Value: "toggle"},
				{Key: "m", Label: "Move to another scope", Value: "move"},
				{Key: "r", Label: "Remove from config metadata", Value: "remove"},
				{Key: "b", Label: "Back", Value: "back"},
			},
			0,
		)
	}

	renderConfigEditVariable(out, scopeName, variable)
	input, err := promptConfigEditLine(reader, out, "Action: t toggle sensitivity, m move scope, r remove, b back")
	if err != nil {
		return "", err
	}
	switch strings.ToLower(input) {
	case "b", "back", "":
		return "back", nil
	case "t", "toggle", "sensitivity":
		return "toggle", nil
	case "m", "move", "scope":
		return "move", nil
	case "r", "remove", "delete":
		return "remove", nil
	default:
		fmt.Fprintln(out, "Choose t, m, r, or b.")
		return "continue", nil
	}
}

func promptConfigEditScope(reader *bufio.Reader, in io.Reader, out io.Writer, project config.ParsedProject, currentScope string) (string, bool, error) {
	if promptCanUseTUI(in, out) {
		choices := make([]tuiChoice, 0, len(project.Scopes)+1)
		for idx, scope := range project.Scopes {
			key := ""
			if idx < 9 {
				key = strconv.Itoa(idx + 1)
			}
			description := ""
			if scope.Name == currentScope {
				description = "current"
			}
			choices = append(choices, tuiChoice{
				Key:         key,
				Label:       scope.Name,
				Description: description,
				Value:       "scope:" + scope.Name,
			})
		}
		choices = append(choices, tuiChoice{Key: "b", Label: "Back", Value: "back"})
		selected, err := promptChoiceTUI(in, out, "Choose target scope", nil, choices, 0)
		if err != nil {
			return "", false, err
		}
		if selected == "back" {
			return "", false, nil
		}
		return strings.TrimPrefix(selected, "scope:"), true, nil
	}

	fmt.Fprintln(out, "Scopes:")
	for idx, scope := range project.Scopes {
		current := ""
		if scope.Name == currentScope {
			current = " (current)"
		}
		fmt.Fprintf(out, "  %d. %s%s\n", idx+1, scope.Name, current)
	}
	for {
		input, err := promptConfigEditLine(reader, out, "Choose target scope number, or b to go back")
		if err != nil {
			return "", false, err
		}
		switch strings.ToLower(input) {
		case "b", "back", "":
			return "", false, nil
		}
		number, err := strconv.Atoi(input)
		if err != nil || number < 1 || number > len(project.Scopes) {
			fmt.Fprintln(out, "Choose a listed scope number or b.")
			continue
		}
		return project.Scopes[number-1].Name, true, nil
	}
}

func promptConfigEditLine(reader *bufio.Reader, out io.Writer, label string) (string, error) {
	fmt.Fprint(out, label+": ")
	value, err := reader.ReadString('\n')
	if err != nil && len(value) == 0 {
		return "", commandError(ExitUserCanceled, "user_canceled", "Prompt could not read input", err)
	}
	return strings.TrimSpace(value), nil
}

func editableConfigVariables(project config.ParsedProject) []configEditVariableItem {
	var items []configEditVariableItem
	for scopeIdx, scope := range project.Scopes {
		for variableIdx, variable := range scope.Variables {
			items = append(items, configEditVariableItem{
				configEditVariableRef: configEditVariableRef{ScopeIndex: scopeIdx, VariableIndex: variableIdx},
				Number:                len(items) + 1,
				Scope:                 scope.Name,
				Variable:              variable,
			})
		}
	}
	return items
}

func configEditVariable(project *config.ParsedProject, ref configEditVariableRef) (config.VariableDeclaration, string, bool) {
	if ref.ScopeIndex < 0 || ref.ScopeIndex >= len(project.Scopes) {
		return config.VariableDeclaration{}, "", false
	}
	scope := project.Scopes[ref.ScopeIndex]
	if ref.VariableIndex < 0 || ref.VariableIndex >= len(scope.Variables) {
		return config.VariableDeclaration{}, "", false
	}
	return scope.Variables[ref.VariableIndex], scope.Name, true
}

func moveConfigEditVariable(project *config.ParsedProject, ref configEditVariableRef, targetScope string) (config.VariableDeclaration, bool, error) {
	variable, sourceScope, ok := configEditVariable(project, ref)
	if !ok {
		return config.VariableDeclaration{}, false, fmt.Errorf("variable is no longer available")
	}
	if sourceScope == targetScope {
		return variable, false, fmt.Errorf("variable is already in scope %s", targetScope)
	}
	targetIdx := -1
	for idx := range project.Scopes {
		if project.Scopes[idx].Name == targetScope {
			targetIdx = idx
			break
		}
	}
	if targetIdx < 0 {
		return config.VariableDeclaration{}, false, fmt.Errorf("unknown scope %s", targetScope)
	}
	for _, existing := range project.Scopes[targetIdx].Variables {
		if existing.Name == variable.Name && existing.EnvFilePath == variable.EnvFilePath {
			return config.VariableDeclaration{}, false, fmt.Errorf("%s already exists in scope %s for %s", variable.Name, targetScope, variable.EnvFilePath)
		}
	}

	sourceVariables := project.Scopes[ref.ScopeIndex].Variables
	project.Scopes[ref.ScopeIndex].Variables = append(append([]config.VariableDeclaration{}, sourceVariables[:ref.VariableIndex]...), sourceVariables[ref.VariableIndex+1:]...)
	project.Scopes[targetIdx].Variables = append(project.Scopes[targetIdx].Variables, variable)

	mappingAdded := false
	if !containsString(project.Scopes[targetIdx].EnvFiles, variable.EnvFilePath) {
		project.Scopes[targetIdx].EnvFiles = append(project.Scopes[targetIdx].EnvFiles, variable.EnvFilePath)
		sort.Strings(project.Scopes[targetIdx].EnvFiles)
		mappingAdded = true
	}
	return variable, mappingAdded, nil
}

func removeConfigEditVariable(project *config.ParsedProject, ref configEditVariableRef) (config.VariableDeclaration, string, bool) {
	variable, scopeName, ok := configEditVariable(project, ref)
	if !ok {
		return config.VariableDeclaration{}, "", false
	}
	variables := project.Scopes[ref.ScopeIndex].Variables
	project.Scopes[ref.ScopeIndex].Variables = append(append([]config.VariableDeclaration{}, variables[:ref.VariableIndex]...), variables[ref.VariableIndex+1:]...)
	return variable, scopeName, true
}

func countConfigVariables(project config.ParsedProject) int {
	var count int
	for _, scope := range project.Scopes {
		count += len(scope.Variables)
	}
	return count
}

func applyConfigEditStats(result *ConfigEditResult) {
	for _, change := range result.Changes {
		switch change.Action {
		case "sensitivity":
			result.VariablesChangedCount++
		case "move":
			result.VariablesMovedCount++
		case "remove":
			result.VariablesRemovedCount++
		}
	}
}

func effectiveConfigSensitivity(value string) string {
	if strings.TrimSpace(value) == "" {
		return config.SensitivitySensitive
	}
	return value
}

func cloneParsedProjectForConfigEdit(project config.ParsedProject) config.ParsedProject {
	out := project
	out.Scopes = make([]config.ScopeSummary, len(project.Scopes))
	for idx, scope := range project.Scopes {
		copied := scope
		copied.EnvFiles = append([]string(nil), scope.EnvFiles...)
		copied.Variables = append([]config.VariableDeclaration(nil), scope.Variables...)
		out.Scopes[idx] = copied
	}
	out.Members = make([]config.Member, len(project.Members))
	for idx, member := range project.Members {
		copied := member
		copied.Scopes = copyConfigEditStringMap(member.Scopes)
		out.Members[idx] = copied
	}
	out.ActiveMemberSHAs = copyConfigEditBoolMap(project.ActiveMemberSHAs)
	return out
}

func copyConfigEditStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func copyConfigEditBoolMap(in map[string]bool) map[string]bool {
	if in == nil {
		return nil
	}
	out := make(map[string]bool, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func renderConfigEditResult(w io.Writer, jsonOutput bool, noColor bool, result ConfigEditResult) {
	if jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return
	}
	style := newOutputStyle(noColor)
	renderCommandTitle(w, style, "Config edit", result.DryRun)
	switch result.Status {
	case "dry_run":
		renderNote(w, style, "Config edit dry run complete.")
	case "no_change":
		renderNote(w, style, "No config changes to save.")
	default:
		renderOK(w, style, "Config edit saved.")
	}
	fmt.Fprintln(w)
	if result.TeamName != "" {
		fmt.Fprintf(w, "Team: %s (%s)\n", result.TeamName, result.TeamID)
	}
	if result.ProjectConfigPath != "" {
		fmt.Fprintf(w, "Config: %s\n", result.ProjectConfigPath)
	}
	fmt.Fprintf(w, "Variables: %d -> %d\n", result.VariablesBeforeCount, result.VariablesAfterCount)
	fmt.Fprintf(w, "Sensitivity changes: %d\n", result.VariablesChangedCount)
	fmt.Fprintf(w, "Scope changes: %d\n", result.VariablesMovedCount)
	fmt.Fprintf(w, "Removed variables: %d\n", result.VariablesRemovedCount)
	fmt.Fprintf(w, "propagate.yaml modified: %t\n", result.ConfigModified)
	if len(result.EnvFileMappingsAdded) > 0 {
		fmt.Fprintln(w, style.bold("Env file mappings added:"))
		for _, mapping := range result.EnvFileMappingsAdded {
			fmt.Fprintf(w, "  - %s -> %s\n", mapping.EnvFilePath, mapping.Scope)
		}
	}
	renderNextSteps(w, style, result.NextSteps)
}

func printConfigEditHelp(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate config edit [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --dry-run                 preview edits without writing propagate.yaml")
	fmt.Fprintln(w, "  --json                    render machine-readable JSON after the editor exits")
	fmt.Fprintln(w, "  --non-interactive         fail instead of opening the editor")
	fmt.Fprintln(w, "  --no-color                disable terminal color")
}

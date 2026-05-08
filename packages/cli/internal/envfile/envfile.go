package envfile

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"propagate/cli/internal/atomicfile"
	"propagate/cli/internal/gitutil"
)

var assignmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var safeUnquotedValue = regexp.MustCompile(`^[A-Za-z0-9_./:@%+=,-]*$`)

var knownNames = []string{
	".env",
	".env.local",
	".env.development",
	".env.dev",
	".env.staging",
	".env.stage",
	".env.production",
	".env.prod",
}

type Candidate struct {
	Path               string   `json:"path"`
	Scope              string   `json:"scope"`
	Tracked            bool     `json:"tracked"`
	ParentTracked      bool     `json:"parent_tracked"`
	Variables          []string `json:"variables,omitempty"`
	VariableCount      int      `json:"variables_count"`
	DuplicateVariables []string `json:"duplicate_variables,omitempty"`
	UnknownLineCount   int      `json:"unknown_line_count,omitempty"`
}

func Scan(worktree gitutil.Worktree) ([]Candidate, []string, error) {
	tracked := map[string]bool{}
	for _, file := range worktree.TrackedFiles {
		tracked[file] = true
	}

	var warnings []string
	seen := map[string]bool{}
	var candidates []Candidate
	for _, dir := range gitutil.ProjectDirs(worktree) {
		for _, name := range knownNames {
			rel := filepath.ToSlash(filepath.Join(dir, name))
			if dir == "." {
				rel = name
			}
			if seen[rel] {
				continue
			}
			seen[rel] = true
			abs := filepath.Join(worktree.Root, filepath.FromSlash(rel))
			info, err := os.Stat(abs)
			if err != nil || info.IsDir() {
				continue
			}

			parsed, err := Parse(abs)
			if err != nil {
				return nil, nil, fmt.Errorf("parse %s: %w", rel, err)
			}
			parent := filepath.ToSlash(filepath.Dir(rel))
			if parent == "" {
				parent = "."
			}
			c := Candidate{
				Path:               rel,
				Scope:              ScopeForFile(name),
				Tracked:            tracked[rel],
				ParentTracked:      worktree.TrackedDirs[parent],
				Variables:          parsed.Variables,
				VariableCount:      len(parsed.Variables),
				DuplicateVariables: parsed.DuplicateVariables,
				UnknownLineCount:   parsed.UnknownLineCount,
			}
			if c.Tracked {
				warnings = append(warnings, fmt.Sprintf("%s is tracked by Git; env files should usually be ignored", rel))
			}
			if len(c.DuplicateVariables) > 0 {
				warnings = append(warnings, fmt.Sprintf("%s contains duplicate variables: %s", rel, strings.Join(c.DuplicateVariables, ", ")))
			}
			if c.UnknownLineCount > 0 {
				warnings = append(warnings, fmt.Sprintf("%s contains %d line(s) Propagate did not classify as simple env assignments", rel, c.UnknownLineCount))
			}
			candidates = append(candidates, c)
		}
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Path < candidates[j].Path
	})
	return candidates, warnings, nil
}

type ParseResult struct {
	Variables          []string
	DuplicateVariables []string
	UnknownLineCount   int
}

type AssignmentParseResult struct {
	Values             map[string]string
	Variables          []string
	DuplicateVariables []string
	UnknownLineCount   int
}

type MergePlan struct {
	Content              []byte
	Changed              bool
	Created              bool
	VariablesAdded       int
	VariablesUpdated     int
	VariablesUnchanged   int
	VariablesPreserved   int
	DuplicateManagedVars []string
}

func (p MergePlan) VariablesWritten() int {
	return p.VariablesAdded + p.VariablesUpdated
}

func (p MergePlan) ManagedVariables() int {
	return p.VariablesAdded + p.VariablesUpdated + p.VariablesUnchanged
}

func ValidateVariableName(name string) error {
	if !assignmentName.MatchString(name) {
		return fmt.Errorf("invalid variable name %q", name)
	}
	return nil
}

func Parse(path string) (ParseResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return ParseResult{}, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	seen := map[string]bool{}
	duplicates := map[string]bool{}
	var variables []string
	var unknown int
	for scanner.Scan() {
		line := scanner.Text()
		name, ok := assignmentFromLine(line)
		if !ok {
			if isIgnorable(line) {
				continue
			}
			unknown++
			continue
		}
		if seen[name] {
			duplicates[name] = true
			continue
		}
		seen[name] = true
		variables = append(variables, name)
	}
	if err := scanner.Err(); err != nil {
		return ParseResult{}, err
	}

	duplicateNames := make([]string, 0, len(duplicates))
	for name := range duplicates {
		duplicateNames = append(duplicateNames, name)
	}
	sort.Strings(duplicateNames)

	return ParseResult{
		Variables:          variables,
		DuplicateVariables: duplicateNames,
		UnknownLineCount:   unknown,
	}, nil
}

func ParseAssignments(path string) (AssignmentParseResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return AssignmentParseResult{}, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	seen := map[string]bool{}
	duplicates := map[string]bool{}
	result := AssignmentParseResult{Values: map[string]string{}}
	for scanner.Scan() {
		line := scanner.Text()
		name, value, _, ok := assignmentDetails(line)
		if !ok {
			if isIgnorable(line) {
				continue
			}
			result.UnknownLineCount++
			continue
		}
		if seen[name] {
			duplicates[name] = true
			continue
		}
		seen[name] = true
		result.Values[name] = value
		result.Variables = append(result.Variables, name)
	}
	if err := scanner.Err(); err != nil {
		return AssignmentParseResult{}, err
	}

	for name := range duplicates {
		result.DuplicateVariables = append(result.DuplicateVariables, name)
	}
	sort.Strings(result.DuplicateVariables)
	return result, nil
}

func PlanMerge(existing []byte, exists bool, values map[string]string) (MergePlan, error) {
	remaining := map[string]string{}
	for name, value := range values {
		if !assignmentName.MatchString(name) {
			return MergePlan{}, fmt.Errorf("invalid variable name %q", name)
		}
		if strings.ContainsAny(value, "\r\n") {
			return MergePlan{}, fmt.Errorf("variable %s contains a newline and cannot be written to a single-line env file", name)
		}
		remaining[name] = value
	}

	plan := MergePlan{Created: !exists && len(values) > 0}
	seenManaged := map[string]bool{}
	lines := splitEnvLines(string(existing))
	var b strings.Builder
	for _, line := range lines {
		body, ending := trimEnding(line)
		name, currentValue, exported, ok := assignmentDetails(body)
		if !ok {
			b.WriteString(line)
			continue
		}
		desired, managed := values[name]
		if !managed {
			plan.VariablesPreserved++
			b.WriteString(line)
			continue
		}
		if seenManaged[name] {
			plan.DuplicateManagedVars = append(plan.DuplicateManagedVars, name)
			b.WriteString(line)
			continue
		}
		seenManaged[name] = true
		delete(remaining, name)
		if currentValue == desired {
			plan.VariablesUnchanged++
			b.WriteString(line)
			continue
		}
		rendered, err := renderAssignment(name, desired, exported)
		if err != nil {
			return MergePlan{}, err
		}
		plan.Changed = true
		plan.VariablesUpdated++
		b.WriteString(rendered)
		b.WriteString(ending)
	}
	if len(plan.DuplicateManagedVars) > 0 {
		sort.Strings(plan.DuplicateManagedVars)
		return plan, fmt.Errorf("env file contains duplicate managed variables: %s", strings.Join(plan.DuplicateManagedVars, ", "))
	}

	if len(remaining) > 0 {
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
			b.WriteString("\n")
		}
		for _, name := range sortedValueNames(remaining) {
			rendered, err := renderAssignment(name, remaining[name], false)
			if err != nil {
				return MergePlan{}, err
			}
			b.WriteString(rendered)
			b.WriteString("\n")
			plan.VariablesAdded++
		}
		plan.Changed = true
	}
	if len(lines) == 0 && len(remaining) == 0 {
		plan.Content = existing
	} else {
		plan.Content = []byte(b.String())
	}
	return plan, nil
}

func WriteMerged(path string, content []byte, perm os.FileMode) error {
	return atomicfile.Write(path, content, perm)
}

func ScopeForFile(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "prod") || strings.Contains(lower, "production"):
		return "prod"
	case strings.Contains(lower, "stag"):
		return "staging"
	default:
		return "dev"
	}
}

func Mask(value string) string {
	switch len(value) {
	case 0:
		return ""
	case 1:
		return "*"
	case 2:
		return value[:1] + "*"
	default:
		return value[:1] + strings.Repeat("*", len(value)-2) + value[len(value)-1:]
	}
}

func assignmentFromLine(line string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "export ") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
	}
	if strings.HasPrefix(trimmed, "#") || trimmed == "" {
		return "", false
	}
	idx := strings.Index(trimmed, "=")
	if idx <= 0 {
		return "", false
	}
	name := strings.TrimSpace(trimmed[:idx])
	if !assignmentName.MatchString(name) {
		return "", false
	}
	return name, true
}

func assignmentDetails(line string) (string, string, bool, bool) {
	trimmed := strings.TrimSpace(line)
	exported := false
	if strings.HasPrefix(trimmed, "export ") {
		exported = true
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
	}
	if strings.HasPrefix(trimmed, "#") || trimmed == "" {
		return "", "", false, false
	}
	idx := strings.Index(trimmed, "=")
	if idx <= 0 {
		return "", "", false, false
	}
	name := strings.TrimSpace(trimmed[:idx])
	if !assignmentName.MatchString(name) {
		return "", "", false, false
	}
	rawValue := strings.TrimSpace(trimmed[idx+1:])
	return name, parseValue(rawValue), exported, true
}

func parseValue(raw string) string {
	if raw == "" {
		return ""
	}
	if strings.HasPrefix(raw, "\"") {
		if value, err := strconv.Unquote(raw); err == nil {
			return value
		}
	}
	if before, _, found := strings.Cut(raw, " #"); found {
		raw = before
	}
	return strings.TrimSpace(raw)
}

func renderAssignment(name string, value string, exported bool) (string, error) {
	prefix := ""
	if exported {
		prefix = "export "
	}
	if value == "" || safeUnquotedValue.MatchString(value) {
		return prefix + name + "=" + value, nil
	}
	return prefix + name + "=" + strconv.Quote(value), nil
}

func splitEnvLines(raw string) []string {
	if raw == "" {
		return nil
	}
	var lines []string
	start := 0
	for i, r := range raw {
		if r == '\n' {
			lines = append(lines, raw[start:i+1])
			start = i + 1
		}
	}
	if start < len(raw) {
		lines = append(lines, raw[start:])
	}
	return lines
}

func trimEnding(line string) (string, string) {
	if strings.HasSuffix(line, "\r\n") {
		return strings.TrimSuffix(line, "\r\n"), "\r\n"
	}
	if strings.HasSuffix(line, "\n") {
		return strings.TrimSuffix(line, "\n"), "\n"
	}
	return line, ""
}

func sortedValueNames(values map[string]string) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func isIgnorable(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed == "" || strings.HasPrefix(trimmed, "#")
}

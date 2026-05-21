package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type tuiChoice struct {
	Label       string
	Description string
	Value       string
	Key         string
}

type choicePromptModel struct {
	title       string
	description []string
	choices     []tuiChoice
	cursor      int
	selected    string
	submitted   bool
	canceled    bool
}

type tuiMultiChoice struct {
	Label       string
	Description string
	Value       string
	Selected    bool
}

type multiChoicePromptModel struct {
	title            string
	description      []string
	choices          []tuiMultiChoice
	cursor           int
	requireSelection bool
	errText          string
	selected         []string
	submitted        bool
	canceled         bool
}

type tuiAccessScope struct {
	Name       string
	Permission string
}

type tuiAccessResult struct {
	Management bool
	Scopes     map[string]string
}

type accessPromptModel struct {
	title            string
	description      []string
	management       bool
	scopes           []tuiAccessScope
	cursor           int
	requireAnyAccess bool
	errText          string
	result           tuiAccessResult
	submitted        bool
	canceled         bool
}

func newChoicePromptModel(title string, description []string, choices []tuiChoice, defaultIndex int) choicePromptModel {
	if defaultIndex < 0 || defaultIndex >= len(choices) {
		defaultIndex = 0
	}
	return choicePromptModel{
		title:       title,
		description: description,
		choices:     choices,
		cursor:      defaultIndex,
	}
}

func newMultiChoicePromptModel(title string, description []string, choices []tuiMultiChoice, requireSelection bool) multiChoicePromptModel {
	return multiChoicePromptModel{
		title:            title,
		description:      description,
		choices:          append([]tuiMultiChoice(nil), choices...),
		requireSelection: requireSelection,
	}
}

func newAccessPromptModel(title string, description []string, management bool, scopes []tuiAccessScope, requireAnyAccess bool) accessPromptModel {
	modelScopes := make([]tuiAccessScope, 0, len(scopes))
	for _, scope := range scopes {
		scope.Permission = normalizeAccessPermission(scope.Permission)
		modelScopes = append(modelScopes, scope)
	}
	return accessPromptModel{
		title:            title,
		description:      description,
		management:       management,
		scopes:           modelScopes,
		requireAnyAccess: requireAnyAccess,
	}
}

func (m choicePromptModel) Init() tea.Cmd {
	return nil
}

func (m multiChoicePromptModel) Init() tea.Cmd {
	return nil
}

func (m accessPromptModel) Init() tea.Cmd {
	return nil
}

func (m choicePromptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch key.String() {
	case "ctrl+c", "esc":
		m.canceled = true
		return m, tea.Quit
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down":
		if m.cursor < len(m.choices)-1 {
			m.cursor++
		}
	case "enter":
		return m.submit()
	default:
		shortcut := strings.ToLower(key.String())
		for idx, choice := range m.choices {
			if choice.Key != "" && strings.ToLower(choice.Key) == shortcut {
				m.cursor = idx
				return m.submit()
			}
		}
	}
	return m, nil
}

func (m multiChoicePromptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch key.String() {
	case "ctrl+c", "esc":
		m.canceled = true
		return m, tea.Quit
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down":
		if m.cursor < len(m.choices)-1 {
			m.cursor++
		}
	case " ":
		if len(m.choices) > 0 {
			m.choices[m.cursor].Selected = !m.choices[m.cursor].Selected
			m.errText = ""
		}
	case "enter":
		return m.submit()
	}
	return m, nil
}

func (m accessPromptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	submitCursor := len(m.scopes) + 1
	switch key.String() {
	case "ctrl+c", "esc":
		m.canceled = true
		return m, tea.Quit
	case "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down":
		if m.cursor < submitCursor {
			m.cursor++
		}
	case " ":
		m.errText = ""
		switch {
		case m.cursor == 0:
			m.management = !m.management
		case m.cursor <= len(m.scopes):
			m.scopes[m.cursor-1].Permission = nextAccessPermission(m.scopes[m.cursor-1].Permission)
		}
	case "enter":
		if m.cursor == submitCursor {
			return m.submit()
		}
		if m.cursor < submitCursor {
			m.cursor++
		}
	}
	return m, nil
}

func (m choicePromptModel) submit() (tea.Model, tea.Cmd) {
	if len(m.choices) == 0 {
		m.canceled = true
		return m, tea.Quit
	}
	m.selected = m.choices[m.cursor].Value
	m.submitted = true
	return m, tea.Quit
}

func (m multiChoicePromptModel) submit() (tea.Model, tea.Cmd) {
	if len(m.choices) == 0 {
		m.canceled = true
		return m, tea.Quit
	}
	m.selected = nil
	for _, choice := range m.choices {
		if choice.Selected {
			m.selected = append(m.selected, choice.Value)
		}
	}
	if m.requireSelection && len(m.selected) == 0 {
		m.errText = "Select at least one item, or press Esc to cancel."
		return m, nil
	}
	m.submitted = true
	return m, tea.Quit
}

func (m accessPromptModel) submit() (tea.Model, tea.Cmd) {
	scopes := map[string]string{}
	for _, scope := range m.scopes {
		permission := normalizeAccessPermission(scope.Permission)
		if permission == "none" {
			continue
		}
		scopes[scope.Name] = permission
	}
	if m.requireAnyAccess && !m.management && len(scopes) == 0 {
		m.errText = "Grant management or at least one scope, or press Esc to cancel."
		return m, nil
	}
	m.result = tuiAccessResult{Management: m.management, Scopes: scopes}
	m.submitted = true
	return m, tea.Quit
}

func (m choicePromptModel) View() string {
	var b strings.Builder
	if m.title != "" {
		b.WriteString(m.title)
		b.WriteString("\n")
	}
	for _, line := range m.description {
		if strings.TrimSpace(line) == "" {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	if len(m.description) > 0 {
		b.WriteString("\n")
	}
	for idx, choice := range m.choices {
		cursor := " "
		if idx == m.cursor {
			cursor = ">"
		}
		key := " "
		if choice.Key != "" {
			key = choice.Key
		}
		fmt.Fprintf(&b, "%s [%s] %s\n", cursor, key, choice.Label)
		if choice.Description != "" {
			fmt.Fprintf(&b, "      %s\n", choice.Description)
		}
	}
	b.WriteString("\n")
	b.WriteString("↑/↓ move · Enter confirm · Esc cancel\n")
	return b.String()
}

func (m multiChoicePromptModel) View() string {
	var b strings.Builder
	if m.title != "" {
		b.WriteString(m.title)
		b.WriteString("\n")
	}
	for _, line := range m.description {
		if strings.TrimSpace(line) == "" {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	if len(m.description) > 0 {
		b.WriteString("\n")
	}
	for idx, choice := range m.choices {
		cursor := " "
		if idx == m.cursor {
			cursor = ">"
		}
		marker := " "
		if choice.Selected {
			marker = "x"
		}
		fmt.Fprintf(&b, "%s [%s] %s\n", cursor, marker, choice.Label)
		if choice.Description != "" {
			fmt.Fprintf(&b, "      %s\n", choice.Description)
		}
	}
	if m.errText != "" {
		b.WriteString("\n")
		b.WriteString(m.errText)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString("↑/↓ move · Space toggle · Enter confirm · Esc cancel\n")
	return b.String()
}

func (m accessPromptModel) View() string {
	var b strings.Builder
	if m.title != "" {
		b.WriteString(m.title)
		b.WriteString("\n")
	}
	for _, line := range m.description {
		if strings.TrimSpace(line) == "" {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	if len(m.description) > 0 {
		b.WriteString("\n")
	}

	cursor := " "
	if m.cursor == 0 {
		cursor = ">"
	}
	marker := " "
	if m.management {
		marker = "x"
	}
	fmt.Fprintf(&b, "%s [%s] Management\n", cursor, marker)
	fmt.Fprintf(&b, "      Can manage project config, invites, and team access.\n")

	for idx, scope := range m.scopes {
		cursor := " "
		if m.cursor == idx+1 {
			cursor = ">"
		}
		fmt.Fprintf(&b, "%s [%-5s] %s\n", cursor, normalizeAccessPermission(scope.Permission), scope.Name)
		fmt.Fprintf(&b, "      Space cycles none → read → write.\n")
	}

	submitCursor := " "
	if m.cursor == len(m.scopes)+1 {
		submitCursor = ">"
	}
	fmt.Fprintf(&b, "\n%s [ Continue ]\n", submitCursor)

	if m.errText != "" {
		b.WriteString("\n")
		b.WriteString(m.errText)
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString("↑/↓ move · Space toggle/cycle · Enter advances (confirms on Continue) · Esc cancel\n")
	return b.String()
}

type textPromptModel struct {
	label      string
	input      textinput.Model
	allowEmpty bool
	trimSpace  bool
	errText    string
	value      string
	submitted  bool
	canceled   bool
}

func newTextPromptModel(label string, allowEmpty bool, trimSpace bool, echoMode textinput.EchoMode) textPromptModel {
	input := textinput.New()
	input.Prompt = label + ": "
	input.EchoMode = echoMode
	input.Width = 80
	_ = input.Focus()
	return textPromptModel{
		label:      label,
		input:      input,
		allowEmpty: allowEmpty,
		trimSpace:  trimSpace,
	}
}

func (m textPromptModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m textPromptModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	switch key.String() {
	case "ctrl+c", "esc":
		m.canceled = true
		return m, tea.Quit
	case "enter":
		value := m.input.Value()
		if m.trimSpace {
			value = strings.TrimSpace(value)
		}
		if value == "" && !m.allowEmpty {
			m.errText = "Please enter a value."
			return m, nil
		}
		m.value = value
		m.submitted = true
		return m, tea.Quit
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m textPromptModel) View() string {
	var b strings.Builder
	b.WriteString(m.input.View())
	if m.errText != "" {
		b.WriteString("\n")
		b.WriteString(m.errText)
	}
	b.WriteString("\n")
	return b.String()
}

func promptCanUseTUI(in io.Reader, out io.Writer) bool {
	inFile, ok := in.(*os.File)
	if !ok || !isCharDevice(inFile) {
		return false
	}
	outFile, ok := out.(*os.File)
	if !ok || !isCharDevice(outFile) {
		return false
	}
	return true
}

func runTUIPrompt(in io.Reader, out io.Writer, model tea.Model) (tea.Model, error) {
	return tea.NewProgram(
		model,
		tea.WithInput(in),
		tea.WithOutput(out),
		tea.WithoutSignalHandler(),
	).Run()
}

func promptChoiceTUI(in io.Reader, out io.Writer, title string, description []string, choices []tuiChoice, defaultIndex int) (string, error) {
	if len(choices) == 0 {
		return "", commandError(ExitValidationError, "validation_failed", "Prompt has no choices", nil)
	}
	final, err := runTUIPrompt(in, out, newChoicePromptModel(title, description, choices, defaultIndex))
	if err != nil {
		return "", commandError(ExitUserCanceled, "user_canceled", "Prompt could not read input", err)
	}
	model, ok := final.(choicePromptModel)
	if !ok || model.canceled || !model.submitted {
		return "", commandError(ExitUserCanceled, "user_canceled", "Prompt was canceled", nil)
	}
	return model.selected, nil
}

func promptAccessTUI(in io.Reader, out io.Writer, title string, description []string, management bool, scopes []tuiAccessScope, requireAnyAccess bool) (tuiAccessResult, error) {
	final, err := runTUIPrompt(in, out, newAccessPromptModel(title, description, management, scopes, requireAnyAccess))
	if err != nil {
		return tuiAccessResult{}, commandError(ExitUserCanceled, "user_canceled", "Prompt could not read input", err)
	}
	model, ok := final.(accessPromptModel)
	if !ok || model.canceled || !model.submitted {
		return tuiAccessResult{}, commandError(ExitUserCanceled, "user_canceled", "Prompt was canceled", nil)
	}
	return model.result, nil
}

func promptMultiChoiceTUI(in io.Reader, out io.Writer, title string, description []string, choices []tuiMultiChoice, requireSelection bool) ([]string, error) {
	if len(choices) == 0 {
		return nil, commandError(ExitValidationError, "validation_failed", "Prompt has no choices", nil)
	}
	final, err := runTUIPrompt(in, out, newMultiChoicePromptModel(title, description, choices, requireSelection))
	if err != nil {
		return nil, commandError(ExitUserCanceled, "user_canceled", "Prompt could not read input", err)
	}
	model, ok := final.(multiChoicePromptModel)
	if !ok || model.canceled || !model.submitted {
		return nil, commandError(ExitUserCanceled, "user_canceled", "Prompt was canceled", nil)
	}
	return model.selected, nil
}

func promptTextTUI(in io.Reader, out io.Writer, label string, allowEmpty bool, trimSpace bool, echoMode textinput.EchoMode) (string, error) {
	final, err := runTUIPrompt(in, out, newTextPromptModel(label, allowEmpty, trimSpace, echoMode))
	if err != nil {
		return "", commandError(ExitUserCanceled, "user_canceled", "Prompt could not read input", err)
	}
	model, ok := final.(textPromptModel)
	if !ok || model.canceled || !model.submitted {
		return "", commandError(ExitUserCanceled, "user_canceled", "Prompt was canceled", nil)
	}
	return model.value, nil
}

func normalizeAccessPermission(permission string) string {
	switch strings.ToLower(strings.TrimSpace(permission)) {
	case "read":
		return "read"
	case "write", "admin":
		return "write"
	default:
		return "none"
	}
}

func nextAccessPermission(permission string) string {
	switch normalizeAccessPermission(permission) {
	case "none":
		return "read"
	case "read":
		return "write"
	default:
		return "none"
	}
}

func promptRequired(reader *bufio.Reader, in io.Reader, out io.Writer, nonInteractive bool, label string) (string, error) {
	if nonInteractive {
		return "", commandError(ExitConfirmationRequired, "confirmation_required", label+" is required in non-interactive mode", nil)
	}
	if promptCanUseTUI(in, out) {
		return promptTextTUI(in, out, label, false, true, textinput.EchoNormal)
	}
	return promptRequiredLine(reader, out, label)
}

func promptOptional(reader *bufio.Reader, in io.Reader, out io.Writer, label string) (string, error) {
	if promptCanUseTUI(in, out) {
		return promptTextTUI(in, out, label, true, true, textinput.EchoNormal)
	}
	return promptOptionalLine(reader, out, label)
}

func promptHiddenText(in io.Reader, out io.Writer, label string, allowEmpty bool) (string, error) {
	return promptTextTUI(in, out, label, allowEmpty, false, textinput.EchoNone)
}

func promptRequiredLine(reader *bufio.Reader, out io.Writer, label string) (string, error) {
	for {
		fmt.Fprintf(out, "%s: ", label)
		value, err := reader.ReadString('\n')
		if err != nil && len(value) == 0 {
			return "", commandError(ExitUserCanceled, "user_canceled", "Prompt could not read input", err)
		}
		value = strings.TrimSpace(value)
		if value != "" {
			return value, nil
		}
		fmt.Fprintln(out, "Please enter a value.")
	}
}

func promptOptionalLine(reader *bufio.Reader, out io.Writer, label string) (string, error) {
	fmt.Fprintf(out, "%s: ", label)
	value, err := reader.ReadString('\n')
	if err != nil && len(value) == 0 {
		return "", commandError(ExitUserCanceled, "user_canceled", "Prompt could not read input", err)
	}
	return strings.TrimSpace(value), nil
}

func promptConfirm(reader *bufio.Reader, in io.Reader, out io.Writer, label string, defaultYes bool) (bool, error) {
	if promptCanUseTUI(in, out) {
		defaultIndex := 1
		if defaultYes {
			defaultIndex = 0
		}
		selected, err := promptChoiceTUI(in, out, label, nil, []tuiChoice{
			{Key: "y", Label: "Yes", Value: "yes"},
			{Key: "n", Label: "No", Value: "no"},
		}, defaultIndex)
		if err != nil {
			return false, err
		}
		return selected == "yes", nil
	}
	return promptConfirmLine(reader, out, label, defaultYes)
}

func promptConfirmLine(reader *bufio.Reader, out io.Writer, label string, defaultYes bool) (bool, error) {
	suffix := " [y/N]: "
	if defaultYes {
		suffix = " [Y/n]: "
	}
	fmt.Fprint(out, label+suffix)
	value, err := reader.ReadString('\n')
	if err != nil && len(value) == 0 {
		return false, commandError(ExitUserCanceled, "user_canceled", "Prompt could not read input", err)
	}
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return defaultYes, nil
	}
	switch value {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	default:
		return false, commandError(ExitUserCanceled, "user_canceled", "Prompt was not confirmed", nil)
	}
}

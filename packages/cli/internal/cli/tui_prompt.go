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

func (m choicePromptModel) Init() tea.Cmd {
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
	case "up", "k", "shift+tab":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j", "tab":
		if m.cursor < len(m.choices)-1 {
			m.cursor++
		}
	case "home":
		m.cursor = 0
	case "end":
		m.cursor = len(m.choices) - 1
	case "enter", " ":
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

func (m choicePromptModel) submit() (tea.Model, tea.Cmd) {
	if len(m.choices) == 0 {
		m.canceled = true
		return m, tea.Quit
	}
	m.selected = m.choices[m.cursor].Value
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
	b.WriteString("Use arrow keys, shortcuts, or enter. Esc cancels.\n")
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

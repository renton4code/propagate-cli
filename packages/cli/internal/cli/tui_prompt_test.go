package cli

import (
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

func TestChoicePromptModelShortcutSelectsChoice(t *testing.T) {
	model := newChoicePromptModel("Confirm", nil, []tuiChoice{
		{Key: "y", Label: "Yes", Value: "yes"},
		{Key: "n", Label: "No", Value: "no"},
	}, 0)

	final, _ := model.Update(keyRune('n'))
	got := final.(choicePromptModel)
	if !got.submitted || got.selected != "no" {
		t.Fatalf("selected = %q submitted=%v, want no submitted", got.selected, got.submitted)
	}
}

func TestChoicePromptModelMovesAndSubmits(t *testing.T) {
	model := newChoicePromptModel("Pick", nil, []tuiChoice{
		{Label: "One", Value: "one"},
		{Label: "Two", Value: "two"},
	}, 0)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	final, _ := updated.(choicePromptModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := final.(choicePromptModel)
	if !got.submitted || got.selected != "two" {
		t.Fatalf("selected = %q submitted=%v, want two submitted", got.selected, got.submitted)
	}
}

func TestTextPromptModelRequiresValue(t *testing.T) {
	model := newTextPromptModel("Handle", false, true, textinput.EchoNormal)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	empty := updated.(textPromptModel)
	if empty.submitted || empty.errText == "" {
		t.Fatalf("empty submit submitted=%v err=%q, want validation error", empty.submitted, empty.errText)
	}

	updated, _ = empty.Update(keyRune('a'))
	final, _ := updated.(textPromptModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := final.(textPromptModel)
	if !got.submitted || got.value != "a" {
		t.Fatalf("value = %q submitted=%v, want a submitted", got.value, got.submitted)
	}
}

func TestMultiChoicePromptModelTogglesAndSubmitsSelection(t *testing.T) {
	model := newMultiChoicePromptModel("Pick", nil, []tuiMultiChoice{
		{Label: "One", Value: "one", Selected: true},
		{Label: "Two", Value: "two", Selected: true},
	}, true)

	updated, _ := model.Update(keyRune(' '))
	updated, _ = updated.(multiChoicePromptModel).Update(tea.KeyMsg{Type: tea.KeyDown})
	final, _ := updated.(multiChoicePromptModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := final.(multiChoicePromptModel)
	if !got.submitted || len(got.selected) != 1 || got.selected[0] != "two" {
		t.Fatalf("selected = %+v submitted=%v, want only two submitted", got.selected, got.submitted)
	}
}

func TestMultiChoicePromptModelRequiresSelection(t *testing.T) {
	model := newMultiChoicePromptModel("Pick", nil, []tuiMultiChoice{
		{Label: "One", Value: "one"},
	}, true)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	empty := updated.(multiChoicePromptModel)
	if empty.submitted || empty.errText == "" {
		t.Fatalf("empty submit submitted=%v err=%q, want validation error", empty.submitted, empty.errText)
	}

	updated, _ = empty.Update(keyRune(' '))
	final, _ := updated.(multiChoicePromptModel).Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := final.(multiChoicePromptModel)
	if !got.submitted || len(got.selected) != 1 || got.selected[0] != "one" {
		t.Fatalf("selected = %+v submitted=%v, want one submitted", got.selected, got.submitted)
	}
}

func keyRune(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

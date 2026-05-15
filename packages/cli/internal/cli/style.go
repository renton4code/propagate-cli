package cli

import (
	"fmt"
	"io"
)

type outputStyle struct {
	enabled bool
}

type initStyle = outputStyle

func newOutputStyle(noColor bool) outputStyle {
	return outputStyle{enabled: !noColor}
}

func (s outputStyle) wrap(code, text string) string {
	if !s.enabled || text == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (s outputStyle) bold(text string) string   { return s.wrap("1", text) }
func (s outputStyle) green(text string) string  { return s.wrap("32", text) }
func (s outputStyle) yellow(text string) string { return s.wrap("33", text) }
func (s outputStyle) blue(text string) string   { return s.wrap("36", text) }
func (s outputStyle) ok() string                { return s.green("✓") }
func (s outputStyle) note() string              { return s.blue("•") }
func (s outputStyle) warn() string              { return s.yellow("!") }

func renderCommandTitle(w io.Writer, style outputStyle, title string, dryRun bool) {
	if dryRun {
		title += " (dry run)"
	}
	fmt.Fprintln(w, style.bold(title))
	fmt.Fprintln(w)
}

func renderOK(w io.Writer, style outputStyle, text string) {
	fmt.Fprintf(w, "%s %s\n", style.ok(), text)
}

func renderNote(w io.Writer, style outputStyle, text string) {
	fmt.Fprintf(w, "%s %s\n", style.note(), text)
}

func renderWarning(w io.Writer, style outputStyle, text string) {
	fmt.Fprintf(w, "%s %s\n", style.warn(), text)
}

func renderWarnings(w io.Writer, style outputStyle, warnings []string) {
	if len(warnings) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s\n", style.yellow("Warnings:"))
	for _, warning := range warnings {
		fmt.Fprintf(w, "- %s\n", warning)
	}
}

func renderNextSteps(w io.Writer, style outputStyle, steps []string) {
	if len(steps) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s\n", style.bold("Next steps:"))
	for i, step := range steps {
		fmt.Fprintf(w, "%d. %s\n", i+1, step)
	}
}

func renderListSection(w io.Writer, style outputStyle, label string, values []string) {
	if len(values) == 0 {
		return
	}
	fmt.Fprintf(w, "\n%s\n", style.bold(label+":"))
	for _, value := range values {
		fmt.Fprintf(w, "- %s\n", value)
	}
}

package agents

import (
	"strings"
	"testing"
)

func TestRenderBlockIncludesCommandGuidance(t *testing.T) {
	block := RenderBlock()
	for _, want := range []string{
		"propagate --help",
		"propagate <group> --help",
		"--non-interactive",
		"propagate config edit",
		"--approve-join",
		"--decline-join",
		"--skip-join",
		"propagate run --scope dev -- COMMAND [args...]",
		"propagate run --scope prod",
		"Child process output from `propagate run` is not sanitized",
		"--value-stdin --yes --non-interactive",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("RenderBlock missing %q:\n%s", want, block)
		}
	}
}

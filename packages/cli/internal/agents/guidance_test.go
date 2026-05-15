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
		"--value-stdin --yes --non-interactive",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("RenderBlock missing %q:\n%s", want, block)
		}
	}
}

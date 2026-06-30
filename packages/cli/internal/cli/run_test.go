package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunWithoutCommandPrintsBannerAndHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(nil, Streams{
		In:      strings.NewReader(""),
		Out:     &stdout,
		Err:     &stderr,
		WorkDir: t.TempDir(),
	})
	if code != ExitSuccess {
		t.Fatalf("Run() exit = %d, want %d; stderr:\n%s", code, ExitSuccess, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr should be empty:\n%s", stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{
		"______                                  _",
		"| ___ \\                                | |",
		"Propagate CLI",
		"Usage:",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("stdout missing %q:\n%s", want, output)
		}
	}
	if strings.Index(output, "______") > strings.Index(output, "Propagate CLI") {
		t.Fatalf("banner should appear before help text:\n%s", output)
	}
}

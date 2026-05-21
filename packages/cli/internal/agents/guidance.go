package agents

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"propagate/cli/internal/atomicfile"
)

const (
	FileName = "AGENTS.md"
	begin    = "<!-- BEGIN PROPAGATE MANAGED BLOCK v1 -->"
	end      = "<!-- END PROPAGATE MANAGED BLOCK v1 -->"
)

type Result struct {
	Status string `json:"status"`
	Path   string `json:"path,omitempty"`
}

func ApplyGeneric(root string) (Result, error) {
	path := filepath.Join(root, FileName)
	block := RenderBlock()

	exists, err := atomicfile.Exists(path)
	if err != nil {
		return Result{}, err
	}
	if !exists {
		if err := atomicfile.Write(path, []byte(block), 0o644); err != nil {
			return Result{}, err
		}
		return Result{Status: "created", Path: path}, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	next, changed, err := upsertBlock(data, []byte(block))
	if err != nil {
		return Result{}, err
	}
	if !changed {
		return Result{Status: "unchanged", Path: path}, nil
	}
	if err := atomicfile.Write(path, next, 0o644); err != nil {
		return Result{}, err
	}
	return Result{Status: "updated", Path: path}, nil
}

func RenderBlock() string {
	return strings.Join([]string{
		begin,
		"### Propagate Environment Handling",
		"",
		"Template: propagate-agent-guidance-v1",
		"",
		"Propagate manages this repository's environment values. The values live encrypted in the Propagate cloud; only `propagate.yaml` (metadata, member keys, declarations) is committed. Never read, copy, paste, or invent env values — always go through Propagate. Treat every env value as confidential, including values that look local-only or harmless.",
		"",
		"**Common tasks**",
		"",
		"- Inspect state: `propagate status --json --non-interactive`. Use `propagate config status`, `propagate team status`, or `propagate env status --scope <scope>` for one area.",
		"- Run code with env values: `propagate run --scope dev -- COMMAND [args...]` — values are injected only into the child process; no `.env` file is written.",
		"- Pull to a `.env` file (only when tooling requires it): `propagate env pull --scope <scope> --dry-run --json --non-interactive`, then `--yes --non-interactive` after human approval of file creation, overwrites, or prod writes.",
		"- Push local env file changes: `propagate env push --scope <scope> --dry-run` first; `--yes --non-interactive` after human approval of additions, changes, and removals.",
		"- Set one cloud value: `propagate env set NAME --scope <scope>` (interactive prompt). For automation only with an approved input channel, pipe the value via `--value-stdin --yes --non-interactive`. Never pass the value as a positional argument.",
		"- Sync `propagate.yaml` from cloud: `propagate config pull --dry-run` first; `--yes --non-interactive` only after the human approves local overwrite risk.",
		"- Push approved config: `propagate config push --dry-run`. Each pending join needs an explicit `--approve-join`, `--decline-join`, or `--skip-join` decision; require human approval before adding `--yes`.",
		"",
		"**Safety rules**",
		"",
		"- Never write env values to `propagate.yaml`, docs, prompts, tests, fixtures, commits, issues, pull request text, or agent instructions.",
		"- Do not run commands inside `propagate run` that print or capture environment variables (`env`, `printenv`, debug-dump scripts). Child process output from `propagate run` is not sanitized by Propagate.",
		"- For prod injection (`propagate run --scope prod`), require explicit human approval before adding `--yes`.",
		"- `propagate config edit` is interactive-only. In automation, propose the intended metadata change instead of trying to drive the editor.",
		"- `propagate team join` and `propagate scope create` modify local `propagate.yaml`. Use `--dry-run` first and report the expected change.",
		"- When a command reports a permission, pending-join, stale-revision, missing-identity, or missing-`propagate.yaml` error, stop and report the next safe command from the CLI output. Do not retry with broader flags.",
		"",
		"**Discover commands**",
		"",
		"Use `propagate --help`, `propagate <group> --help`, or `propagate <group> <command> --help` when usage is unclear. Add `--json` for machine-readable output, `--non-interactive` so commands fail instead of prompting, and `--no-color` for plain terminal styling.",
		end,
		"",
	}, "\n")
}

func upsertBlock(data, block []byte) ([]byte, bool, error) {
	beginBytes := []byte(begin)
	endBytes := []byte(end)
	beginIndex := bytes.Index(data, beginBytes)
	endIndex := bytes.Index(data, endBytes)
	if (beginIndex == -1) != (endIndex == -1) {
		return nil, false, fmt.Errorf("%s has malformed Propagate managed block markers", FileName)
	}
	if beginIndex == -1 {
		next := append(bytes.TrimRight(data, "\n"), '\n', '\n')
		next = append(next, block...)
		return next, true, nil
	}
	endIndex += len(endBytes)
	for endIndex < len(data) && (data[endIndex] == '\r' || data[endIndex] == '\n') {
		endIndex++
	}
	next := append([]byte{}, data[:beginIndex]...)
	next = append(next, block...)
	next = append(next, data[endIndex:]...)
	if bytes.Equal(next, data) {
		return data, false, nil
	}
	return next, true, nil
}

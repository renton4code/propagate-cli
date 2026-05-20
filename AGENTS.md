<!-- BEGIN PROPAGATE MANAGED BLOCK v1 -->
### Propagate Environment Handling

Template: propagate-agent-guidance-v1

- Use Propagate CLI commands instead of reading, copying, inventing, or committing env values.
- Treat all env values as confidential, including values that look public or local-only.
- Never write env values to `propagate.yaml`, docs, prompts, tests, fixtures, commits, or agent instructions.
- Use `propagate --help`, `propagate <group> --help`, or `propagate <group> <command> --help` when command usage or flags are unclear.
- Prefer `propagate config status`, `propagate team status`, and `propagate env status` for discovery.
- Prefer `--json` for machine-readable status output and `--dry-run` before write-capable commands.
- Prefer `--non-interactive` for automation so commands fail instead of prompting.
- Treat `propagate version`, `propagate help`, command-group help, `propagate config status`, `propagate team status`, and `propagate env status` as read-only inspection commands.
- Treat `propagate team join` and `propagate scope create` as Git-reviewed local metadata changes; use `--dry-run` first and report the expected `propagate.yaml` change.
- Treat `propagate config edit` as interactive-only; in automation, propose the intended metadata change instead of trying to drive the editor.
- For `propagate config pull`, run `--dry-run` first and require human approval before using `--yes` to overwrite local config changes.
- For `propagate config push`, every pending join needs an explicit `--approve-join`, `--decline-join`, or `--skip-join` decision; require human approval before adding `--yes`.
- For `propagate run --scope dev -- COMMAND [args...]`, inject values only into the intended child process; do not use it to inspect, print, or capture secret values.
- Child process output from `propagate run` is not sanitized by Propagate. Do not run commands that are expected to print env values.
- Ask for human confirmation before running `propagate env pull`, `propagate env push`, `propagate env set`, `propagate config push`, or `propagate run --scope prod`.
- For `propagate env set`, never pass the value as a positional argument. In non-interactive runs, use `--value-stdin --yes --non-interactive` only when the human provided an approved secure input channel.
- Report permission errors and pending join requirements clearly instead of attempting workarounds.
<!-- END PROPAGATE MANAGED BLOCK v1 -->

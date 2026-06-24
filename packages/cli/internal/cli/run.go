package cli

import (
	"flag"
	"fmt"
	"io"
	"os"

	"propagate/cli/internal/identity"
)

const Version = "0.1.0-dev"

var BakedDefaultAPIURL = identity.DefaultAPIURL

type Streams struct {
	In      io.Reader
	Out     io.Writer
	Err     io.Writer
	WorkDir string
}

type globalOptions struct {
	JSON           bool
	NonInteractive bool
	NoColor        bool
	Debug          bool
	APIURL         string
}

func Run(args []string, streams Streams) int {
	if streams.In == nil {
		streams.In = os.Stdin
	}
	if streams.Out == nil {
		streams.Out = os.Stdout
	}
	if streams.Err == nil {
		streams.Err = os.Stderr
	}
	if streams.WorkDir == "" {
		if cwd, err := os.Getwd(); err == nil {
			streams.WorkDir = cwd
		}
	}
	loadLocalDotenv(streams.WorkDir)

	global, rest, err := parseLeadingGlobal(args)
	if err != nil {
		return renderError(streams.Err, global.JSON, global.NoColor, err)
	}
	if len(rest) == 0 {
		printRootHelp(streams.Out)
		return ExitSuccess
	}
	switch rest[0] {
	case "quickstart":
		return runQuickstartCommand(rest[1:], global, streams)
	case "init":
		return runInitCommand(rest[1:], global, streams)
	case "status":
		return runStatusCommand(rest[1:], global, streams)
	case "run":
		return runProcessCommand(rest[1:], global, streams)
	case "team":
		return runTeamCommand(rest[1:], global, streams)
	case "scope":
		return runScopeCommand(rest[1:], global, streams)
	case "config":
		return runConfigCommand(rest[1:], global, streams)
	case "env":
		return runEnvCommand(rest[1:], global, streams)
	case "version", "--version", "-v":
		fmt.Fprintf(streams.Out, "propagate %s\n", Version)
		return ExitSuccess
	case "help", "--help", "-h":
		printRootHelp(streams.Out)
		return ExitSuccess
	default:
		err := commandError(ExitUsageError, "usage_error", fmt.Sprintf("Unknown command %q", rest[0]), nil, "Run `propagate help` to see available commands.")
		return renderError(streams.Err, global.JSON, global.NoColor, err)
	}
}

func parseLeadingGlobal(args []string) (globalOptions, []string, error) {
	var opts globalOptions
	var rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--json":
			opts.JSON = true
		case "--non-interactive":
			opts.NonInteractive = true
		case "--no-color":
			opts.NoColor = true
		case "--debug":
			opts.Debug = true
		case "--api-url":
			if i+1 >= len(args) {
				return opts, nil, commandError(ExitUsageError, "usage_error", "--api-url requires a value", nil)
			}
			i++
			opts.APIURL = args[i]
		default:
			rest = append(rest, args[i:]...)
			return opts, rest, nil
		}
	}
	return opts, rest, nil
}

func addGlobalFlags(fs *flag.FlagSet, opts *globalOptions) {
	fs.BoolVar(&opts.JSON, "json", opts.JSON, "render machine-readable JSON")
	fs.BoolVar(&opts.NonInteractive, "non-interactive", opts.NonInteractive, "fail instead of prompting")
	fs.BoolVar(&opts.NoColor, "no-color", opts.NoColor, "disable terminal color")
	fs.BoolVar(&opts.Debug, "debug", opts.Debug, "enable safe debug diagnostics")
	fs.StringVar(&opts.APIURL, "api-url", opts.APIURL, "override Propagate API URL")
}

type multiFlag []string

func (m *multiFlag) String() string { return fmt.Sprintf("%v", *m) }
func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func printRootHelp(w io.Writer) {
	fmt.Fprintln(w, "Propagate CLI")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  propagate quickstart [flags]")
	fmt.Fprintln(w, "  propagate init [flags]")
	fmt.Fprintln(w, "  propagate status [flags]")
	fmt.Fprintln(w, "  propagate run [flags] -- COMMAND [args...]")
	fmt.Fprintln(w, "  propagate team join [flags]")
	fmt.Fprintln(w, "  propagate team status [flags]")
	fmt.Fprintln(w, "  propagate scope create NAME [flags]")
	fmt.Fprintln(w, "  propagate config status [flags]")
	fmt.Fprintln(w, "  propagate config pull [flags]")
	fmt.Fprintln(w, "  propagate config push [flags]")
	fmt.Fprintln(w, "  propagate config edit [flags]")
	fmt.Fprintln(w, "  propagate env pull [flags]")
	fmt.Fprintln(w, "  propagate env push [flags]")
	fmt.Fprintln(w, "  propagate env set NAME [flags]")
	fmt.Fprintln(w, "  propagate env status [flags]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Global flags:")
	fmt.Fprintln(w, "  --json              render machine-readable JSON")
	fmt.Fprintln(w, "  --non-interactive   fail instead of prompting")
	fmt.Fprintln(w, "  --no-color          disable terminal color")
	fmt.Fprintln(w, "  --api-url VALUE     override Propagate API URL")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  quickstart  guided setup, invite, or join for the current project")
	fmt.Fprintln(w, "  init      create or load local identity and initialize project metadata")
	fmt.Fprintln(w, "  status    show config, team, and env status together")
	fmt.Fprintln(w, "  run       inject decrypted env values into a child process")
	fmt.Fprintln(w, "  team      team membership commands")
	fmt.Fprintln(w, "  scope     scope metadata commands")
	fmt.Fprintln(w, "  config    config synchronization commands")
	fmt.Fprintln(w, "  env       environment variable commands")
	fmt.Fprintln(w, "  version   print CLI version")
}

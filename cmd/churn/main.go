// Command churn is the work dependency & resource tracker (DESIGN.md).
//
// One binary, seven subcommands:
//
//	serve       run the workspace server (lock, replay, writer, HTTP API)
//	export-log  stream the event log as canonical JSONL (§5.4)
//	import-log  restore a JSONL log into an empty data directory (§5.4)
//	backup      write an online, transactionally consistent snapshot
//	reindex     rebuild the derived event_refs table
//	seed-demo   create a demo workspace in an empty data directory
//	version     print version and build information
//
// The workspace directory is the --data flag, or the CHURN_DATA environment
// variable, or the current directory — in that order, so a bare `churn serve`
// runs against the directory you are in.
//
// The command layer is deliberately thin: all real logic lives in the
// internal packages (store, writer, interchange, server); main wires flags,
// files, and exit codes.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"churn/internal/store"
)

const usageText = `usage: churn <command> [flags]

commands:
  serve       run the workspace server
  export-log  stream the event log as canonical JSONL
  import-log  restore a JSONL log into an empty data directory
  backup      write a consistent online snapshot of the workspace database
  reindex     rebuild the derived event_refs table
  seed-demo   create a demo workspace in an empty data directory
  version     print version and build information

The workspace directory defaults to the current directory; override it with
--data or the CHURN_DATA environment variable.

Run 'churn <command> -h' for command flags.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "churn: %v\n", err)
		os.Exit(1)
	}
}

// run dispatches one CLI invocation. It is main minus process concerns
// (signal context, exit codes), so tests drive commands in-process.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return errors.New("no command given")
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "serve":
		return cmdServe(ctx, rest, stdout, stderr)
	case "export-log":
		return cmdExportLog(rest, stdout, stderr)
	case "import-log":
		return cmdImportLog(rest, stdin, stdout, stderr)
	case "backup":
		return cmdBackup(rest, stdout, stderr)
	case "reindex":
		return cmdReindex(rest, stdout, stderr)
	case "seed-demo":
		return cmdSeedDemo(rest, stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, versionString())
		return nil
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usageText)
		return nil
	default:
		fmt.Fprint(stderr, usageText)
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// newFlagSet builds a subcommand FlagSet with a --data flag, the one flag
// every command shares.
func newFlagSet(name, synopsis string, stderr io.Writer) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: churn %s\n\n", synopsis)
		fs.PrintDefaults()
	}
	data := fs.String("data", "", "workspace data directory (default: current directory; env CHURN_DATA)")
	return fs, data
}

// resolveDataDir picks the workspace directory: the --data flag if given,
// else the CHURN_DATA environment variable, else the current directory. So a
// bare command runs against the directory you are in, and CHURN_DATA lets you
// set it once per shell.
func resolveDataDir(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if env := os.Getenv("CHURN_DATA"); env != "" {
		return env
	}
	return "."
}

// openWorkspace opens the data directory exclusively (lock, schema), mapping
// ErrLocked to an operator-readable message. Only serve (which may create a
// fresh workspace) and import-log (which restores into an empty directory)
// create files; maintenance commands pre-check with requireWorkspace so a
// typo'd --data path errors instead of minting an empty workspace.
func openWorkspace(dir string) (*store.Store, error) {
	st, err := store.Open(dir)
	if errors.Is(err, store.ErrLocked) {
		return nil, fmt.Errorf("data directory %s is in use by another churn process (%s is held)",
			dir, store.LockFileName)
	}
	return st, err
}

// requireWorkspace fails unless dir already contains a workspace database.
func requireWorkspace(dir string) error {
	path := filepath.Join(dir, store.DBFileName)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("no workspace database at %s (is --data right?): %w", path, err)
	}
	return nil
}

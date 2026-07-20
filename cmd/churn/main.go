// Command churn is the work dependency & resource tracker (DESIGN.md).
//
// One binary, eight subcommands:
//
//	serve       run the workspace server (lock, replay, writer, HTTP API)
//	ls          list projects, things, or resources (read-only)
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
	"syscall"

	"churn/internal/store"
)

const usageText = `usage: churn <command> [flags]

commands:
  serve       run the workspace server
  ls          list projects, things, or resources
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

// shutdownSignals trigger a graceful shutdown. SIGTERM matters as much as
// SIGINT: it is what `docker stop`, systemd, and a plain `kill` send, and
// without it serve dies on the default disposition — no drain, no writer
// stop, no clean database close. (Windows never delivers it; asking is
// harmless there.)
var shutdownSignals = []os.Signal{os.Interrupt, syscall.SIGTERM}

// watchSecondSignal turns a second shutdown signal into an immediate exit.
// The first starts a drain that is bounded but not instant, and an operator
// who signals again means "now" — without this the repeat is swallowed,
// because signal.NotifyContext keeps its handler installed after firing and
// the default kill-the-process disposition never comes back. The channel is
// registered up front rather than after the first signal, so a fast double
// Ctrl-C cannot slip through the gap.
func watchSecondSignal(stderr io.Writer) {
	ch := make(chan os.Signal, 2)
	signal.Notify(ch, shutdownSignals...)
	go func() {
		<-ch // the one that started the graceful shutdown
		<-ch
		fmt.Fprintln(stderr, "churn: second signal: exiting immediately")
		os.Exit(130) // 128 + SIGINT, the shell convention for signal death
	}()
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals...)
	defer stop()
	watchSecondSignal(os.Stderr)
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
	case "ls":
		return cmdList(rest, stdout, stderr)
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
		// `churn help <command>` shows that command's own usage. Explicitly
		// requested help is redirect-friendly output: send it to stdout by
		// giving the subcommand (whose usage prints to its error writer)
		// stdout as both writers.
		if len(rest) > 0 && isCommand(rest[0]) {
			return run(ctx, []string{rest[0], "-h"}, stdin, stdout, stdout)
		}
		fmt.Fprint(stdout, usageText)
		return nil
	default:
		fmt.Fprint(stderr, usageText)
		if s := suggestCommand(cmd); s != "" {
			return fmt.Errorf("unknown command %q (did you mean %q?)", cmd, s)
		}
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// commandNames is the set of subcommands, for help routing and did-you-mean.
var commandNames = []string{
	"serve", "ls", "export-log", "import-log", "backup", "reindex",
	"seed-demo", "version", "help",
}

func isCommand(name string) bool {
	for _, c := range commandNames {
		if c == name {
			return true
		}
	}
	return false
}

// suggestCommand returns the closest command name to a mistyped one, or "" if
// none is close enough (edit distance within a third of the name's length, so
// only genuine typos are suggested, never a wild guess).
func suggestCommand(cmd string) string {
	best, bestDist := "", 1<<30
	for _, c := range commandNames {
		if d := levenshtein(cmd, c); d < bestDist {
			best, bestDist = c, d
		}
	}
	if limit := (len(cmd) + 2) / 3; bestDist <= limit {
		return best
	}
	return ""
}

// levenshtein is the classic edit distance (two-row DP), used only for the
// typo suggestion above.
func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
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
// ErrLocked to an operator-readable message. Only serve --init (see
// checkServeDataDir) and import-log (which restores into an empty directory)
// create files; every other path pre-checks with requireWorkspace or
// checkServeDataDir, so a typo'd --data errors instead of minting an empty
// workspace.
func openWorkspace(dir string) (*store.Store, error) {
	st, err := store.Open(dir)
	if errors.Is(err, store.ErrLocked) {
		return nil, fmt.Errorf("data directory %s is in use by another churn process (%s is held)",
			dir, store.LockFileName)
	}
	return st, err
}

// checkServeDataDir applies serve's workspace-creation policy to dir and
// reports whether serve is about to create one.
//
// Creating a workspace is explicit: without --init, an absent or empty
// directory is an error. --data defaults to the current directory, so the
// permissive alternative turns a typo'd path — or simply the wrong working
// directory — into a brand-new empty workspace that is indistinguishable
// from having lost the real one. Failing loudly costs one flag and removes
// that whole class of scare.
//
// A leftover restore staging file refuses outright, --init or not: it means
// an import-log was interrupted, and opening (or creating) a workspace
// beside it would quietly paper over the wreckage of a restore the operator
// believes succeeded, possibly after the JSONL source is already gone.
func checkServeDataDir(dir string, init bool, stderr io.Writer) (creating bool, err error) {
	partial := filepath.Join(dir, store.RestoreDBFileName)
	if _, err := os.Stat(partial); err == nil {
		// A restore that is RUNNING right now looks identical on disk to one
		// that was interrupted — both hold this file. Only the directory lock
		// tells them apart, so ask before diagnosing: telling an operator to
		// delete the database of a live import would cause exactly the data
		// loss this guard exists to prevent.
		inUse, uerr := store.InUse(dir)
		if uerr != nil {
			return false, fmt.Errorf("checking %s: %w", dir, uerr)
		}
		if inUse {
			return false, fmt.Errorf("data directory %s is in use by another churn process (%s is held)",
				dir, store.LockFileName)
		}
		return false, fmt.Errorf("%s is a partial restore left by an interrupted import-log; "+
			"re-run the import into an empty directory, or delete the file if the restore is not wanted", partial)
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("checking %s: %w", partial, err)
	}

	db := filepath.Join(dir, store.DBFileName)
	switch _, err := os.Stat(db); {
	case err == nil:
		if init {
			fmt.Fprintf(stderr, "churn: --init ignored: %s already contains a workspace\n", dir)
		}
		return false, nil
	case !errors.Is(err, os.ErrNotExist):
		return false, fmt.Errorf("checking %s: %w", db, err)
	case !init:
		return false, fmt.Errorf("no workspace at %s (is --data right? pass --init to create one here)", db)
	}
	return true, nil
}

// requireWorkspace fails unless dir already contains a workspace database.
func requireWorkspace(dir string) error {
	path := filepath.Join(dir, store.DBFileName)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("no workspace database at %s (is --data right?): %w", path, err)
	}
	return nil
}

package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"runtime"
	"strconv"
	"strings"
	"time"

	"churn/internal/interchange"
	"churn/internal/server"
	"churn/internal/store"
	"churn/internal/writer"
)

// cmdServe opens the workspace (lock, replay, writer) and serves the HTTP
// API (§5.1) until ctx is cancelled. The actor stamped on every write comes
// from --actor (default: OS username) — phase 3 replaces this with
// server-side sessions (§6). Creating a workspace requires --init; see
// checkServeDataDir for why. Shutdown is graceful: SSE streams are ended,
// then in-flight requests drain (up to shutdownGrace), then the writer stops
// and the database closes.
func cmdServe(ctx context.Context, args []string, stdout, stderr io.Writer) (err error) {
	fs, data := newFlagSet("serve", "serve [--data <dir>] [--init] [--port <n>] [--listen <addr>] [--actor <name>] [--no-open] [--verbose]", stderr)
	initWS := fs.Bool("init", false, "create the workspace if the data directory has none")
	port := fs.Int("port", defaultPort, "port to listen on (env CHURN_PORT)")
	listen := fs.String("listen", "", "full listen address host:port (advanced; overrides --port)")
	actor := fs.String("actor", "", "actor stamped on every write (default: OS username)")
	noOpen := fs.Bool("no-open", false, "do not open the UI in a browser on start")
	verbose := fs.Bool("verbose", false, "log every request (method, path, status, duration) to stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	dir := resolveDataDir(*data)
	if *actor == "" {
		*actor = defaultActor()
	}
	addr, err := resolveListenAddr(*listen, *port, set["port"])
	if err != nil {
		return err
	}
	creating, err := checkServeDataDir(dir, *initWS, stderr)
	if err != nil {
		return err
	}

	st, err := openWorkspace(dir)
	if err != nil {
		return err
	}
	// Closing the database is where a failed WAL checkpoint surfaces, so the
	// error is reported rather than swallowed — but never over an error that
	// already went wrong earlier, which is the more useful one.
	defer func() {
		if cerr := st.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("closing workspace: %w", cerr)
		}
	}()
	w, err := writer.Open(st, writer.Options{Actor: *actor})
	if err != nil {
		return err
	}
	// Deferred after the store's, so it runs BEFORE it: the writer must stop
	// before the database it writes to goes away.
	defer w.Close()
	if creating {
		fmt.Fprintf(stdout, "churn: created a new workspace in %s\n", dir)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w (is the port already in use? set --port or CHURN_PORT)", addr, err)
	}
	url := fmt.Sprintf("http://%s", ln.Addr())
	fmt.Fprintf(stdout, "churn: workspace %s: listening on %s\n",
		w.Projection().WorkspaceID, url)
	fmt.Fprintf(stdout, "churn: acting as %s\n", *actor)

	api := server.New(w, st, server.Options{
		DataDir: dir, Actor: *actor, Verbose: *verbose, LogWriter: stderr,
	})
	srv := &http.Server{Handler: api.Handler()}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	if !*noOpen {
		fmt.Fprintln(stdout, "churn: opening the UI in your browser (--no-open to disable)")
		openBrowser(url)
	}
	select {
	case <-ctx.Done():
		fmt.Fprintln(stdout, "churn: shutting down (signal again to exit immediately)")
		api.Shutdown() // end SSE streams so the drain below can complete
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		serr := srv.Shutdown(shutCtx)
		// Shutdown closes the listeners before it starts draining, so Serve
		// has already returned whether the drain finished or timed out;
		// collect it unconditionally instead of leaving its result dropped.
		//
		// On the timeout path handlers are still running, and the deferred
		// closes below do pull the database out from under them — they fail
		// with "database is closed" toward clients that, by definition of the
		// timeout, are no longer waiting. The log cannot tear: writer.Close
		// waits for the batch in progress and every later Submit is rejected,
		// so a wedged request delays the exit but never corrupts anything.
		<-errc // http.ErrServerClosed
		if serr != nil {
			return fmt.Errorf("shutdown: %w (in-flight requests did not finish within %s)", serr, shutdownGrace)
		}
		return nil
	case err := <-errc:
		api.Shutdown() // the listener died; release the SSE streams too
		return fmt.Errorf("serve: %w", err)
	}
}

// shutdownGrace bounds the post-signal drain of in-flight requests. Every
// churn handler is an in-memory computation over the projection measured in
// single-digit milliseconds (README performance envelope), so this is orders
// of magnitude more than a healthy drain needs — it exists to cap a wedged
// one, not to accommodate slow work.
const shutdownGrace = 5 * time.Second

// defaultPort is the serve port when neither --port nor CHURN_PORT is set. It
// deliberately avoids 8080 (which many dev tools grab) and sits below the
// Linux ephemeral range, so it rarely collides; 24876 also spells "CHURN" on a
// phone keypad, which makes the URL easy to remember.
const defaultPort = 24876

// resolveListenAddr builds the serve bind address. --listen wins outright —
// full host:port control, e.g. 0.0.0.0:9000 to bind every interface.
// Otherwise the port comes from --port when set, else CHURN_PORT, else the
// default, bound to loopback. portSet reports whether --port was passed, so an
// explicit flag beats the environment.
func resolveListenAddr(listen string, port int, portSet bool) (string, error) {
	if listen != "" {
		return listen, nil
	}
	p := port
	if !portSet {
		if env := os.Getenv("CHURN_PORT"); env != "" {
			v, err := strconv.Atoi(env)
			if err != nil {
				return "", fmt.Errorf("CHURN_PORT %q is not a number", env)
			}
			p = v
		}
	}
	if p < 0 || p > 65535 {
		return "", fmt.Errorf("port %d is out of range (0–65535)", p)
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(p)), nil
}

// openBrowser best-effort launches the default browser at url. Failures are
// ignored: a headless machine simply has no launcher, and serve keeps running
// regardless (the address is always printed).
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// The empty title arg stops `start` treating the URL as a window title.
		cmd = exec.Command("cmd", "/c", "start", "", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err == nil {
		go func() { _ = cmd.Wait() }() // reap the launcher so it never lingers defunct
	}
}

// defaultActor is the fallback --actor value: the OS username (domain
// qualifiers stripped on Windows), or "local" when even that is unknown.
func defaultActor() string {
	u, err := user.Current()
	if err != nil || u.Username == "" {
		return "local"
	}
	name := u.Username
	if i := strings.LastIndexAny(name, `\/`); i >= 0 {
		name = name[i+1:]
	}
	if name == "" {
		return "local"
	}
	return name
}

// cmdExportLog streams the log as canonical JSONL to stdout or --out. It
// opens the workspace read-only, without the lock, so it works against a
// live server (WAL snapshot: a consistent complete-batch prefix).
func cmdExportLog(args []string, stdout, stderr io.Writer) error {
	fs, data := newFlagSet("export-log", "export-log [--data <dir>] [file]", stderr)
	out := fs.String("out", "", "output file (alias for the positional argument)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := resolveDataDir(*data)

	// The output path is the positional argument (consistent with backup and
	// import-log); --out is a back-compat alias. "-" or omission means stdout.
	outPath := *out
	if fs.NArg() > 1 {
		return errors.New("export-log takes at most one output file")
	}
	if fs.NArg() == 1 {
		if *out != "" && *out != fs.Arg(0) {
			return errors.New("export-log: give the output file once, as an argument OR --out, not both")
		}
		outPath = fs.Arg(0)
	}

	st, err := store.OpenReadOnly(dir)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	dst := stdout
	var f *os.File
	tmpPath := ""
	if outPath != "" && outPath != "-" {
		// Stage to a temp file and rename on success (the same discipline as
		// import-log's .partial): a failed or interrupted export must never
		// truncate or delete a file the user already had at outPath.
		tmpPath = outPath + ".partial"
		if f, err = os.Create(tmpPath); err != nil {
			return err
		}
		dst = f
	}
	bw := bufio.NewWriterSize(dst, 1<<16)
	err = interchange.Export(st, bw)
	if err == nil {
		err = bw.Flush()
	}
	if f != nil {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Rename(tmpPath, outPath) // atomic publish over any existing file
		}
		if err != nil {
			_ = os.Remove(tmpPath) // discard the partial; the user's file is untouched
		}
	}
	return err
}

// cmdImportLog restores a JSONL stream (a file, or "-" for stdin) into an
// empty data directory. All-or-nothing: any envelope-hygiene or domain
// validation failure aborts with a line-numbered error and nothing written.
func cmdImportLog(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs, data := newFlagSet("import-log", "import-log [--data <dir>] <file|->", stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := resolveDataDir(*data)
	if fs.NArg() != 1 {
		return errors.New("import-log needs exactly one argument: the JSONL file, or - for stdin")
	}

	src := stdin
	if name := fs.Arg(0); name != "-" {
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }() // input file, opened read-only
		src = f
	}
	events, batches, err := interchange.Import(dir, src)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "churn: imported %d events (%d batches) into %s\n", events, batches, dir)
	return nil
}

// cmdBackup writes an online, transactionally consistent snapshot of the
// workspace database. Like export-log it opens read-only without the lock,
// so it works while a server is running.
func cmdBackup(args []string, stdout, stderr io.Writer) error {
	fs, data := newFlagSet("backup", "backup [--data <dir>] <dest.db>", stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := resolveDataDir(*data)
	if fs.NArg() != 1 {
		return errors.New("backup needs exactly one argument: the destination database file")
	}

	st, err := store.OpenReadOnly(dir)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	if err := st.Backup(fs.Arg(0)); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "churn: backup written to %s\n", fs.Arg(0))
	return nil
}

// cmdReindex rebuilds event_refs from events. It opens the workspace
// exclusively — reindex rewrites a table, so it refuses to run while another
// process holds the workspace lock — and requires an existing workspace: a
// typo'd --data must error, not mint an empty one.
func cmdReindex(args []string, stdout, stderr io.Writer) error {
	fs, data := newFlagSet("reindex", "reindex [--data <dir>]", stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir := resolveDataDir(*data)
	if err := requireWorkspace(dir); err != nil {
		return err
	}

	st, err := openWorkspace(dir)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	n, err := st.Reindex()
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "churn: event_refs rebuilt: %d rows\n", n)
	return nil
}

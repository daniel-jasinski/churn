package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"churn/internal/interchange"
	"churn/internal/server"
	"churn/internal/store"
	"churn/internal/writer"
)

// cmdServe opens the workspace (lock, replay, writer) and serves the HTTP
// API until ctx is cancelled. M4 exposes the health endpoint only; the
// server package is the seam M5 fills in.
func cmdServe(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs, data := newFlagSet("serve", "serve --data <dir> [--listen <addr>]", stderr)
	listen := fs.String("listen", "127.0.0.1:8080", "address to listen on")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, err := requireData(*data)
	if err != nil {
		return err
	}

	st, err := openWorkspace(dir)
	if err != nil {
		return err
	}
	defer st.Close()
	w, err := writer.Open(st, writer.Options{})
	if err != nil {
		return err
	}
	defer w.Close()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", *listen, err)
	}
	fmt.Fprintf(stdout, "churn: workspace %s: listening on http://%s\n",
		w.Projection().WorkspaceID, ln.Addr())

	srv := &http.Server{Handler: server.New(w).Handler()}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			return fmt.Errorf("shutdown: %w", err)
		}
		<-errc // http.ErrServerClosed
		return nil
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	}
}

// cmdExportLog streams the log as canonical JSONL to stdout or --out. It
// opens the workspace read-only, without the lock, so it works against a
// live server (WAL snapshot: a consistent complete-batch prefix).
func cmdExportLog(args []string, stdout, stderr io.Writer) error {
	fs, data := newFlagSet("export-log", "export-log --data <dir> [--out <file>]", stderr)
	out := fs.String("out", "", "output file (default: stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, err := requireData(*data)
	if err != nil {
		return err
	}

	st, err := store.OpenReadOnly(dir)
	if err != nil {
		return err
	}
	defer st.Close()

	dst := stdout
	var f *os.File
	if *out != "" {
		if f, err = os.Create(*out); err != nil {
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
		if err != nil {
			os.Remove(*out) // do not leave a truncated export behind
		}
	}
	return err
}

// cmdImportLog restores a JSONL stream (a file, or "-" for stdin) into an
// empty data directory. All-or-nothing: any envelope-hygiene or domain
// validation failure aborts with a line-numbered error and nothing written.
func cmdImportLog(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs, data := newFlagSet("import-log", "import-log --data <dir> <file|->", stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, err := requireData(*data)
	if err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("import-log needs exactly one argument: the JSONL file, or - for stdin")
	}

	src := stdin
	if name := fs.Arg(0); name != "-" {
		f, err := os.Open(name)
		if err != nil {
			return err
		}
		defer f.Close()
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
	fs, data := newFlagSet("backup", "backup --data <dir> <dest.db>", stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, err := requireData(*data)
	if err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("backup needs exactly one argument: the destination database file")
	}

	st, err := store.OpenReadOnly(dir)
	if err != nil {
		return err
	}
	defer st.Close()
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
	fs, data := newFlagSet("reindex", "reindex --data <dir>", stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	dir, err := requireData(*data)
	if err != nil {
		return err
	}
	if err := requireWorkspace(dir); err != nil {
		return err
	}

	st, err := openWorkspace(dir)
	if err != nil {
		return err
	}
	defer st.Close()
	n, err := st.Reindex()
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "churn: event_refs rebuilt: %d rows\n", n)
	return nil
}

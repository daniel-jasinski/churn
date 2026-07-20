package main

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"churn/internal/domain"
	"churn/internal/event"
	"churn/internal/store"
)

// cmdList (churn ls) prints projects, things, or resources — a table by
// default, JSON with --json. It opens the workspace read-only WITHOUT the lock
// (like export-log and backup), so it works against a live server, and folds
// the log to the current projection: the same brain the server computes from,
// never a parallel interpretation of the log.
func cmdList(args []string, stdout, stderr io.Writer) error {
	// The kind is an optional leading positional ("ls things --json"). Pull it
	// off before flag parsing, since Go's flag package stops at the first
	// non-flag argument and would otherwise treat trailing flags as extras.
	kind := "things"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		kind, args = args[0], args[1:]
	}

	fs, data := newFlagSet("ls", "ls [projects|things|resources] [--data <dir>] [--project <id>] [--json]", stderr)
	project := fs.String("project", "", "when listing things, show only this project id")
	asJSON := fs.Bool("json", false, "output JSON instead of a table")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("ls: unexpected argument %q — the kind (projects, things, or resources) comes first, before any flags", fs.Arg(0))
	}
	dir := resolveDataDir(*data)

	p, err := loadProjection(dir)
	if err != nil {
		return err
	}
	switch kind {
	case "projects":
		return listProjects(p, stdout, *asJSON)
	case "things":
		return listThings(p, *project, stdout, *asJSON)
	case "resources":
		return listResources(p, stdout, *asJSON)
	default:
		return fmt.Errorf("ls: unknown kind %q (want projects, things, or resources)", kind)
	}
}

// loadProjection reads the whole log read-only and folds it to the current
// projection.
func loadProjection(dir string) (*domain.Projection, error) {
	st, err := store.OpenReadOnly(dir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = st.Close() }()
	var evs []event.Envelope
	if err := st.Scan(func(ev event.Envelope) error {
		evs = append(evs, ev)
		return nil
	}); err != nil {
		return nil, err
	}
	return domain.Fold(evs)
}

func listProjects(p *domain.Projection, w io.Writer, asJSON bool) error {
	type row struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Things int    `json:"things"`
	}
	rows := []row{}
	for _, id := range sortedKeys(p.Projects) {
		n := 0
		for _, th := range p.Things {
			if th.Project == id {
				n++
			}
		}
		rows = append(rows, row{id, p.Projects[id].Name, n})
	}
	if asJSON {
		return writeJSONValue(w, rows)
	}
	tw := newTab(w)
	fmt.Fprintln(tw, "ID\tNAME\tTHINGS")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%d\n", r.ID, r.Name, r.Things)
	}
	return tw.Flush()
}

func listThings(p *domain.Projection, project string, w io.Writer, asJSON bool) error {
	derived := p.DeriveAll()
	type row struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Type      string `json:"type"`
		Status    string `json:"status"`
		Composite bool   `json:"composite"`
		Project   string `json:"project"`
	}
	rows := []row{}
	for _, id := range sortedKeys(p.Things) {
		th := p.Things[id]
		if project != "" && th.Project != project {
			continue
		}
		rows = append(rows, row{
			ID: id, Name: th.Name, Type: typeName(p, th.Type),
			Status: string(derived[id].Status), Composite: len(th.Children) > 0,
			Project: projectName(p, th.Project),
		})
	}
	if asJSON {
		// Status stays clean (a bare status word); composite is its own field,
		// so JSON consumers never have to parse "ready (composite)".
		return writeJSONValue(w, rows)
	}
	tw := newTab(w)
	fmt.Fprintln(tw, "ID\tNAME\tTYPE\tSTATUS\tPROJECT")
	for _, r := range rows {
		status := r.Status
		if r.Composite {
			status += " (composite)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", r.ID, r.Name, r.Type, status, r.Project)
	}
	return tw.Flush()
}

func listResources(p *domain.Projection, w io.Writer, asJSON bool) error {
	type row struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		Capacity  int    `json:"capacity"`
		Allocated int    `json:"allocated"`
		Available bool   `json:"available"`
	}
	rows := []row{}
	for _, id := range sortedKeys(p.Resources) {
		r := p.Resources[id]
		rows = append(rows, row{id, r.Name, r.Kind, r.Capacity, p.AllocatedQuantity(id), r.Available})
	}
	if asJSON {
		return writeJSONValue(w, rows)
	}
	tw := newTab(w)
	fmt.Fprintln(tw, "ID\tNAME\tKIND\tCAP\tALLOC\tAVAILABLE")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%t\n", r.ID, r.Name, r.Kind, r.Capacity, r.Allocated, r.Available)
	}
	return tw.Flush()
}

func projectName(p *domain.Projection, id string) string {
	if pr, ok := p.Projects[id]; ok {
		return pr.Name
	}
	return id
}

func typeName(p *domain.Projection, id string) string {
	if t, ok := p.Types[id]; ok {
		return t.Name
	}
	return id
}

func newTab(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
}

// writeJSONValue prints v as indented JSON with a trailing newline.
func writeJSONValue(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// sortedKeys returns a string-keyed map's keys in ascending order.
func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

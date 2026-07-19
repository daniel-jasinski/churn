package server

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"churn/web"
)

// TestFrontendBuildFreshness recomputes the hash of the frontend build
// INPUTS (web/src/**, package.json, package-lock.json, tsconfig.json,
// build.mjs) and compares it against the stamp web/build.mjs wrote into
// dist/.buildstamp. A mismatch means someone committed a src change without
// rebuilding dist/ — the reproducibility contract of web/README.md.
//
// The hashing here MUST stay identical to build.mjs: for each input file,
// in byte-sorted order of its forward-slash path relative to web/, hash
// "path\x00" + raw bytes + "\x00".
func TestFrontendBuildFreshness(t *testing.T) {
	webDir := filepath.Join("..", "..", "web")
	if _, err := os.Stat(filepath.Join(webDir, "node_modules")); err != nil {
		t.Skip("web/node_modules absent (npm install not run here) — freshness unchecked; " +
			"the committed dist/ is still served. See web/README.md for the rebuild flow.")
	}

	stamp, err := fs.ReadFile(web.Dist, "dist/.buildstamp")
	if err != nil {
		t.Fatalf("embedded dist/.buildstamp missing — run `node web/build.mjs` and commit dist/: %v", err)
	}

	var files []string
	for _, f := range []string{"build.mjs", "package.json", "package-lock.json", "tsconfig.json"} {
		files = append(files, f)
	}
	err = filepath.WalkDir(filepath.Join(webDir, "src"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(webDir, p)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walking web/src: %v", err)
	}
	sort.Strings(files)

	h := sha256.New()
	for _, f := range files {
		b, err := os.ReadFile(filepath.Join(webDir, filepath.FromSlash(f)))
		if err != nil {
			t.Fatalf("reading %s: %v", f, err)
		}
		h.Write([]byte(f))
		h.Write([]byte{0})
		h.Write(b)
		h.Write([]byte{0})
	}
	want := hex.EncodeToString(h.Sum(nil))
	got := strings.TrimSpace(string(stamp))
	if got != want {
		t.Fatalf("web/dist is STALE: committed frontend sources changed without a rebuild.\n"+
			"  stamp in dist/.buildstamp: %s\n"+
			"  hash of current inputs:    %s\n"+
			"Run: cd web && npm install && node build.mjs — then commit dist/.", got, want)
	}
}

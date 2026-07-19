// Package web embeds the built frontend (web/dist) into the churn binary.
//
// dist/ is COMMITTED: the Go binary builds offline with no Node toolchain.
// npm is needed only to REBUILD the frontend (see web/README.md); the
// freshness test in internal/server catches a committed src change without
// a rebuilt dist.
package web

import "embed"

// Dist is the built frontend: index.html, assets/, and .buildstamp.
//
//go:embed all:dist
var Dist embed.FS

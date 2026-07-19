# churn frontend (web/)

Vanilla TypeScript, no framework. Rendering deps are exactly two runtime
packages — `cytoscape` + `cytoscape-dagre` (with its `dagre` dependency) —
plus `esbuild` as the only build tool. Versions are pinned exactly (no `^`
ranges) and `package-lock.json` is committed.

## The reproducibility contract

**`web/dist/` is committed.** The Go binary embeds it via `embed.FS`
(`web/embed.go`), so `go build` works offline with no Node toolchain — npm
is needed only to *rebuild* the frontend. `node_modules/` is never
committed. No CDN, no network at build or run time (DESIGN.md §5).

## Rebuilding

```
cd web
npm install        # restores the pinned toolchain from package-lock.json
node build.mjs     # production build → dist/ (minified, hashed asset names)
node build.mjs --dev   # dev build (sourcemaps, unminified)
```

Then commit `dist/` together with your `src/` change.

## The freshness gate

`build.mjs` writes `dist/.buildstamp`: a SHA-256 over the build inputs
(`src/**`, `package.json`, `package-lock.json`, `tsconfig.json`,
`build.mjs`), hashed as `path\0` + bytes + `\0` in byte-sorted path order.

`internal/server/freshness_test.go` recomputes exactly that hash and fails
when a committed source change ships without a rebuilt `dist/`. The test
SKIPS when `web/node_modules` is absent (a Go-only checkout can neither
rebuild nor be blamed); run `npm install` to arm it.

## Layout

```
src/main.ts        boot: shell, routing, store wiring, shortcuts
src/api.ts         typed client — mirrors internal/server/dto.go by hand
src/store.ts       cached snapshot; SSE refresh (debounced), 10s poll fallback
src/router.ts      hash routes: #/ready #/graph/:id #/resources #/bottlenecks
                   #/tree #/vocab #/history/:id? #/settings
src/views/*.ts     one module per screen
src/ui/*.ts        shared pieces: transition/proposal flow, thing editor,
                   bulk add, §2.1 promotion conversion, as-of picker
src/styles.css     dense, light+dark (prefers-color-scheme), system fonts
```

TypeScript is written under `strict` settings (`tsconfig.json`), but note
esbuild only strips types — there is deliberately no `tsc` in the pinned
toolchain, so the compiler is not part of the build gate.

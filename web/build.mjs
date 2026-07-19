// build.mjs — bundles web/src into web/dist with esbuild.
//
//   node build.mjs          production build (minified, hashed asset names)
//   node build.mjs --dev    dev build (sourcemaps, no minify; still hashed)
//
// Outputs:
//   dist/index.html            (from src/index.html, asset names substituted)
//   dist/assets/app-<hash>.js  (+ .css, + .map in dev)
//   dist/.buildstamp           hash of the build INPUTS (src/**, package.json,
//                              package-lock.json, tsconfig.json, build.mjs)
//
// The .buildstamp is what internal/server's freshness test recomputes: a
// committed change to any input without a rebuilt dist/ fails the gate.
// Keep the hashing here and in freshness_test.go identical.

import { build } from 'esbuild';
import { createHash } from 'node:crypto';
import { readFileSync, writeFileSync, mkdirSync, rmSync, readdirSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { fileURLToPath } from 'node:url';

const webDir = fileURLToPath(new URL('.', import.meta.url));
const dev = process.argv.includes('--dev');

// ── clean dist ──
rmSync(join(webDir, 'dist'), { recursive: true, force: true });
mkdirSync(join(webDir, 'dist', 'assets'), { recursive: true });

// ── bundle ──
const result = await build({
  entryPoints: [join(webDir, 'src', 'main.ts')],
  bundle: true,
  outdir: join(webDir, 'dist', 'assets'),
  entryNames: 'app-[hash]',
  format: 'iife',
  target: 'es2022',
  minify: !dev,
  sourcemap: dev,
  metafile: true,
  logLevel: 'info',
});

// Find the emitted asset names from the metafile.
let jsName = '', cssName = '';
for (const out of Object.keys(result.metafile.outputs)) {
  const rel = relative(join(webDir, 'dist'), join(webDir, out)).replace(/\\/g, '/');
  // metafile paths are relative to cwd; normalize against dist
  const name = out.replace(/\\/g, '/').split('/').pop();
  if (name.endsWith('.js')) jsName = 'assets/' + name;
  if (name.endsWith('.css')) cssName = 'assets/' + name;
  void rel;
}
if (!jsName || !cssName) {
  console.error('build: missing bundle outputs (js: %s, css: %s)', jsName, cssName);
  process.exit(1);
}

// ── index.html ──
const html = readFileSync(join(webDir, 'src', 'index.html'), 'utf8')
  .replaceAll('{{JS}}', '/' + jsName)
  .replaceAll('{{CSS}}', '/' + cssName);
writeFileSync(join(webDir, 'dist', 'index.html'), html);

// ── buildstamp: sha256 over the sorted build inputs ──
// Contract (mirrored in internal/server/freshness_test.go): for each input
// file, in byte-sorted order of its forward-slash path relative to web/,
// hash "path\x00" + raw file bytes + "\x00".
function inputFiles() {
  const files = ['build.mjs', 'package.json', 'package-lock.json', 'tsconfig.json'];
  const walk = (dir) => {
    for (const name of readdirSync(dir).sort()) {
      const p = join(dir, name);
      if (statSync(p).isDirectory()) walk(p);
      else files.push(relative(webDir, p).replace(/\\/g, '/'));
    }
  };
  walk(join(webDir, 'src'));
  return files.sort();
}
const h = createHash('sha256');
for (const f of inputFiles()) {
  h.update(f + '\x00');
  h.update(readFileSync(join(webDir, f)));
  h.update('\x00');
}
writeFileSync(join(webDir, 'dist', '.buildstamp'), h.digest('hex') + '\n');

console.log(`build: dist/index.html -> ${jsName}, ${cssName}${dev ? ' (dev)' : ''}`);

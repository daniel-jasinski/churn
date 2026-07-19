// vendor.d.ts — minimal ambient typings for the vendored graph libraries.
// esbuild strips types; these keep tsc-aware editors and strict mode honest
// without pulling @types packages (deps are pinned to cytoscape + dagre only).

declare module 'cytoscape' {
  export type Collection = any;
  export type NodeSingular = any;
  export type EdgeSingular = any;
  export type Core = any;
  interface CytoscapeStatic {
    (options?: Record<string, unknown>): Core;
    use(ext: unknown): void;
  }
  const cytoscape: CytoscapeStatic;
  export default cytoscape;
}

declare module 'cytoscape-dagre' {
  const ext: unknown;
  export default ext;
}

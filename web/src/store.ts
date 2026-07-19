// store.ts — one cached snapshot of the workspace read side. Views render
// from it; SSE commit notifications (debounced) or a 10s poll refresh it.

import {
  api, setMutationsDisabled,
  CapabilityDef, Dependency, Project, ReadyEntry, Requirement, Resource,
  ResourceType, Semantic, StateDef, Thing, TypeDef, Weights, Workspace,
} from './api';
import { debounce } from './dom';

export type Listener = () => void;

class Store {
  // collections, in API (deterministic) order
  projects: Project[] = [];
  things: Thing[] = [];
  resources: Resource[] = [];
  dependencies: Dependency[] = [];
  requirements: Requirement[] = [];
  states: StateDef[] = [];
  types: TypeDef[] = [];
  resourceTypes: ResourceType[] = [];
  capabilities: CapabilityDef[] = [];
  ready: ReadyEntry[] = [];
  weights: Weights | null = null;
  workspace: Workspace | null = null;

  // as-of mode: a past cursor (seq or timestamp) for graph/tree; while set,
  // all mutations are disabled.
  asOf: string | null = null;

  // selectedProject is the sticky cross-tab project selection ('' = all),
  // persisted per browser. Views bind their project pickers to it.
  selectedProject: string = localStorage.getItem('churn.project') ?? '';

  /** version bumps on every successful refresh — cheap change detection. */
  version = 0;
  loaded = false;
  /** live is true while the SSE stream is connected. */
  live = false;

  private listeners = new Set<Listener>();
  private es: EventSource | null = null;
  private pollTimer: number | undefined;
  private sseRetryTimer: number | undefined;
  private refreshing = false;
  private refreshQueued = false;

  subscribe(fn: Listener): () => void {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  }

  private notify(): void {
    for (const fn of this.listeners) fn();
  }

  // ── lookups ──

  thing(id: string): Thing | undefined { return this.things.find((t) => t.id === id); }
  project(id: string): Project | undefined { return this.projects.find((p) => p.id === id); }
  resource(id: string): Resource | undefined { return this.resources.find((r) => r.id === id); }
  state(id: string): StateDef | undefined { return this.states.find((s) => s.id === id); }
  type(id: string): TypeDef | undefined { return this.types.find((t) => t.id === id); }
  resourceType(id: string): ResourceType | undefined { return this.resourceTypes.find((t) => t.id === id); }
  capability(id: string): CapabilityDef | undefined { return this.capabilities.find((c) => c.id === id); }

  name(id: string): string {
    return this.thing(id)?.name ?? this.project(id)?.name ?? this.resource(id)?.name
      ?? this.state(id)?.name ?? this.type(id)?.name ?? this.resourceType(id)?.name
      ?? this.capability(id)?.name ?? id;
  }

  requirementsOf(thing: string): Requirement[] {
    return this.requirements.filter((r) => r.thing === thing);
  }

  statesBySemantic(sem: Semantic): StateDef[] {
    return this.states.filter((s) => s.semantic === sem);
  }

  semanticOf(thing: Thing): Semantic | undefined {
    return thing.state ? this.state(thing.state)?.semantic : undefined;
  }

  childrenOf(id: string): Thing[] {
    return this.things.filter((t) => t.parent === id);
  }

  // ── project selection ──

  setSelectedProject(id: string): void {
    this.selectedProject = id;
    try {
      localStorage.setItem('churn.project', id);
    } catch {
      // storage unavailable (private mode): the selection is session-only
    }
  }

  /** concreteProject resolves the sticky selection to an actual project id
   * for views that need one (the graph): the selection if it still exists,
   * else the first project alphabetically by name, else null. */
  concreteProject(): string | null {
    if (this.selectedProject && this.project(this.selectedProject)) return this.selectedProject;
    if (this.projects.length === 0) return null;
    return [...this.projects].sort((a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id))[0]!.id;
  }

  // ── as-of ──

  setAsOf(cursor: string | null): void {
    this.asOf = cursor;
    setMutationsDisabled(cursor !== null);
    document.body.classList.toggle('asof', cursor !== null);
    this.notify();
  }

  // ── refresh machinery ──

  async refresh(): Promise<void> {
    if (this.refreshing) { this.refreshQueued = true; return; }
    this.refreshing = true;
    try {
      const [projects, things, resources, dependencies, requirements,
        states, types, resourceTypes, capabilities, ready, weights, workspace] = await Promise.all([
        api.projects(), api.things(), api.resources(), api.dependencies(),
        api.requirements(), api.states(), api.types(), api.resourceTypes(),
        api.capabilities(), api.ready(), api.settings(), api.workspace(),
      ]);
      this.projects = projects;
      this.things = things;
      this.resources = resources;
      this.dependencies = dependencies;
      this.requirements = requirements;
      this.states = states;
      this.types = types;
      this.resourceTypes = resourceTypes;
      this.capabilities = capabilities;
      this.ready = ready;
      this.weights = weights;
      this.workspace = workspace;
      this.loaded = true;
      this.version++;
      this.notify();
    } finally {
      this.refreshing = false;
      if (this.refreshQueued) {
        this.refreshQueued = false;
        void this.refresh();
      }
    }
  }

  private scheduleRefresh = debounce(() => void this.refresh().catch(console.error), 250);

  start(): void {
    void this.refresh().catch(console.error);
    this.connectSSE();
  }

  private connectSSE(): void {
    if (this.es) this.es.close();
    const es = new EventSource('/api/v1/events/stream');
    this.es = es;
    es.addEventListener('hello', () => {
      this.live = true;
      this.stopPolling();
      this.notify();
    });
    es.addEventListener('commit', () => this.scheduleRefresh());
    es.onerror = () => {
      // SSE unavailable: fall back to a 10s poll, retry SSE every 30s.
      this.live = false;
      es.close();
      if (this.es === es) this.es = null;
      this.startPolling();
      if (this.sseRetryTimer === undefined) {
        this.sseRetryTimer = window.setTimeout(() => {
          this.sseRetryTimer = undefined;
          this.connectSSE();
        }, 30_000);
      }
      this.notify();
    };
  }

  private startPolling(): void {
    if (this.pollTimer !== undefined) return;
    this.pollTimer = window.setInterval(() => void this.refresh().catch(console.error), 10_000);
  }

  private stopPolling(): void {
    if (this.pollTimer !== undefined) {
      clearInterval(this.pollTimer);
      this.pollTimer = undefined;
    }
  }
}

export const store = new Store();

// api.ts — typed client for /api/v1. The interfaces mirror
// internal/server/dto.go field-for-field; keep them in sync by hand.

// ── entity DTOs ──

export type Semantic = 'pending' | 'active' | 'paused' | 'satisfied' | 'abandoned';
export type Status =
  | 'blocked' | 'ready' | 'resource_blocked'
  | 'working' | 'finished' | 'held' | 'dropped';

export interface Project {
  id: string;
  name: string;
  metadata?: unknown;
  version: number;
}

export interface Badges {
  abandoned_dependency: boolean;
  finished_unsatisfied_deps: boolean;
  over_allocated: boolean;
  allocations_out_of_step: boolean;
}

export interface Progress {
  satisfied: number;
  total: number;
  has_abandoned: boolean;
  display: string;
}

export interface Thing {
  id: string;
  project: string;
  name: string;
  type: string;
  parent?: string;
  metadata?: Record<string, unknown>;
  state?: string;
  composite: boolean;
  children?: string[];
  status: Status;
  has_abandoned: boolean;
  resumable_now: boolean;
  badges: Badges;
  progress?: Progress;
  version: number;
}

export interface Dependency {
  id: string;
  from: string;
  to: string;
  on_abandoned: 'block' | 'ignore';
  satisfied: boolean;
  abandoned_tolerated: boolean;
  version: number;
}

export interface Requirement {
  id: string;
  thing: string;
  quantity: number;
  capabilities?: string[];
  resource?: string;
  version: number;
}

export interface Resource {
  id: string;
  name: string;
  kind: 'reusable' | 'consumable';
  named: boolean;
  capacity: number;
  type?: string;
  metadata?: Record<string, unknown>;
  capabilities?: string[];
  available: boolean;
  note?: string;
  effective_capacity: number;
  allocated: number;
  free: number;
  over_allocated: boolean;
  version: number;
}

export interface StateDef {
  id: string;
  name: string;
  semantic: Semantic;
  color?: string;
  description?: string;
  version: number;
}

export interface TypeDef {
  id: string;
  name: string;
  color?: string;
  description?: string;
  version: number;
}

export interface CapabilityDef {
  id: string;
  name: string;
  description?: string;
  version: number;
}

export interface ResourceType {
  id: string;
  name: string;
  color?: string;
  description?: string;
  version: number;
}

export interface WorkspaceCounts {
  projects: number;
  things: number;
  resources: number;
  dependencies: number;
  requirements: number;
  states: number;
  types: number;
  resource_types: number;
  capabilities: number;
  open_allocations: number;
  closed_allocations: number;
}

export interface Workspace {
  workspace_id: string;
  origin: string;
  last_seq: number;
  last_ts: string;
  counts: WorkspaceCounts;
}

// ── analytics DTOs ──

export interface MatchReq {
  id: string;
  quantity: number;
  capabilities?: string[];
  pin?: string;
}

export interface ScoreTerm {
  name: string;
  value: number;
  weight: number;
  contribution: number;
  detail: string;
}

export interface Recommendation {
  thing: string;
  score: number;
  terms: ScoreTerm[];
}

export interface ReadyEntry {
  thing: string;
  project: string;
  type: string;
  requirements: MatchReq[];
  score: Recommendation;
}

export interface Weights {
  immediate_unlock: number;
  downstream_reach: number;
  remaining_depth: number;
  waiting_age: number;
  scarcity_penalty: number;
}

export interface Criticality {
  thing: string;
  downstream_reach: number;
  immediate_unlock: number;
  remaining_depth: number;
}

export interface SignatureContention {
  signature: string;
  things: string[];
  demand: number;
  matched: number;
  unmet: number;
  pressure: number;
}

export interface ResourceContention {
  resource: string;
  free: number;
  used: number;
  over_allocated: boolean;
}

export interface TagRatio {
  capability: string;
  demand_units: number;
  free_units: number;
  ratio: number | null;
  heuristic: boolean;
}

export interface Contention {
  demand: number;
  matched: number;
  unmet: number;
  attribution_indicative: boolean;
  signatures: SignatureContention[];
  resources: ResourceContention[];
  tag_ratios: TagRatio[];
}

export interface Starvation {
  thing: string;
  current_stint_seconds: number;
  credit_seconds: number;
}

export interface Bottlenecks {
  criticality: Criticality[];
  contention: Contention;
  starvation: Starvation[];
}

export interface RecommendationsResponse {
  weights: Weights;
  recommendations: Recommendation[];
}

export interface BoardAllocation {
  id: string;
  thing: string;
  thing_name: string;
  requirement: string;
  quantity: number;
}

export interface BoardQueueEntry {
  thing: string;
  name: string;
  status: Status;
  requirements: string[];
}

export interface ResourceBoardRow {
  resource: Resource;
  open_allocations: BoardAllocation[];
  queue: BoardQueueEntry[];
}

// ── graph ──

export interface AsOf {
  requested: string;
  seq: number;
  ts: string;
}

export interface ExpandedEdge {
  from: string;
  to: string;
  declared: boolean;
}

export interface Graph {
  project: Project;
  as_of?: AsOf;
  things: Thing[];
  dependencies: Dependency[];
  edges: ExpandedEdge[];
}

// ── transitions ──

export interface AllocationRow {
  requirement: string;
  resource: string;
  quantity: number;
}

export interface Proposal {
  token: string;
  thing: string;
  state: string;
  based_on_seq: number;
  allocations: AllocationRow[];
}

export interface TransitionResult {
  committed: boolean;
  thing: string;
  state?: string;
  seq?: number;
  batch?: string;
  proposal?: Proposal;
  opened?: string[];
  closed?: string[];
}

// ── batch ──

// Placeholder syntax: any id-bearing field of an op (its "id" target, or
// payload fields like parent/from/to/thing/resource/state/capabilities) may
// be the string "$N", naming the id minted by the CREATE op at zero-based
// index N earlier in the same operations array. Forward/self references and
// references to non-create ops are 400s. The response's `placeholders` maps
// each "$N" to the minted id.
export interface BatchOp {
  op: 'create' | 'supersede' | 'retract' | 'transition' | 'availability' | 'grant' | 'revoke';
  kind: 'project' | 'thing' | 'resource' | 'dependency' | 'requirement' | 'state' | 'type' | 'capability' | 'resource_type';
  id?: string;
  data?: unknown;
}

export interface BatchResponse {
  mode: 'preview' | 'commit';
  committed: boolean;
  results: { id: string }[];
  placeholders?: Record<string, string>;
  seq?: number;
  batch?: string;
}

// ── history ──

export interface EventEnvelope {
  seq: number;
  id: string;
  origin: string;
  batch: string;
  causes: string | null;
  ts: string;
  actor: string;
  type: string;
  v: number;
  entity: string;
  data: Record<string, unknown>;
}

export interface HistoryFilter {
  entity?: string;
  type?: string;
  actor?: string;
  batch?: string;
  since_seq?: number;
  until_seq?: number;
  limit?: number;
}

// ── error envelope ──

interface ErrorEnvelope {
  error: { kind: string; message: string; ids?: string[]; details?: Record<string, unknown> };
}

/** ApiError carries the structured error envelope of every non-2xx reply. */
export class ApiError extends Error {
  constructor(
    public status: number,
    public kind: string,
    message: string,
    public ids: string[] = [],
    public details: Record<string, unknown> = {},
  ) {
    super(message);
    this.name = 'ApiError';
  }

  /** friendly renders the one place errors become user-facing prose. */
  friendly(): string {
    const withIds = (s: string) => (this.ids.length ? `${s} (${this.ids.join(', ')})` : s);
    switch (this.kind) {
      case 'stale_version':
        return 'Someone else changed this while you were editing — the view has been refreshed; please retry.';
      case 'cycle':
        return withIds('Rejected: this would make the dependency graph cyclic');
      case 'retraction_blocked':
        return withIds('Cannot delete: other entities still reference it');
      case 'semantic_immutable':
        return 'This state is occupied — its semantic is locked while any thing is in it. Name, color and description can still be changed.';
      case 'capacity':
        return 'A resource ran out of free capacity in the meantime.';
      case 'infeasible_allocation':
        return 'No feasible assignment of free resources exists for this right now.';
      case 'allocation_coverage':
        return 'The requirements changed between propose and confirm.';
      case 'containment':
        return this.message;
      case 'undefined_reference':
        return withIds('Reference to an undefined vocabulary entry or entity');
      case 'duplicate_id':
        return withIds('The id already exists');
      case 'pin_violation':
        return this.message;
      case 'composite_state':
        return 'Composites are never worked directly — their state is a rollup of their children.';
      case 'composite_requirement':
        return 'Composites carry no requirements — put them on a child step.';
      case 'not_found':
        return withIds('Not found');
      case 'bad_request':
        return `Invalid request: ${this.message}`;
      default:
        return this.message || this.kind;
    }
  }
}

// ── transport ──

/** mutationsDisabled is set by the store while viewing the past (as-of). */
export let mutationsDisabled = false;
export function setMutationsDisabled(v: boolean): void {
  mutationsDisabled = v;
}

async function req<T>(method: string, path: string, body?: unknown, headers?: Record<string, string>): Promise<T> {
  if (method !== 'GET' && mutationsDisabled) {
    throw new ApiError(0, 'read_only', 'Viewing the past — mutations are disabled. Return to now first.');
  }
  const init: RequestInit = { method, headers: { ...headers } };
  if (body !== undefined) {
    init.body = JSON.stringify(body);
    (init.headers as Record<string, string>)['Content-Type'] = 'application/json';
  }
  let resp: Response;
  try {
    resp = await fetch('/api/v1' + path, init);
  } catch (e) {
    throw new ApiError(0, 'network', `Cannot reach the churn server: ${String(e)}`);
  }
  if (resp.status === 204) return undefined as T;
  let payload: unknown;
  try {
    payload = await resp.json();
  } catch {
    throw new ApiError(resp.status, 'bad_response', `Non-JSON response (HTTP ${resp.status})`);
  }
  if (!resp.ok) {
    const env = payload as ErrorEnvelope;
    const d = env?.error ?? { kind: 'unknown', message: `HTTP ${resp.status}` };
    throw new ApiError(resp.status, d.kind, d.message, d.ids ?? [], d.details ?? {});
  }
  return payload as T;
}

const get = <T>(path: string) => req<T>('GET', path);

// ── endpoints ──

export const api = {
  // reads
  workspace: () => get<Workspace>('/workspace'),
  projects: () => get<Project[]>('/projects'),
  things: (project?: string) => get<Thing[]>('/things' + (project ? `?project=${encodeURIComponent(project)}` : '')),
  thing: (id: string) => get<Thing>(`/things/${id}`),
  resources: () => get<Resource[]>('/resources'),
  dependencies: () => get<Dependency[]>('/dependencies'),
  requirements: () => get<Requirement[]>('/requirements'),
  states: () => get<StateDef[]>('/vocab/states'),
  types: () => get<TypeDef[]>('/vocab/types'),
  capabilities: () => get<CapabilityDef[]>('/vocab/capabilities'),
  resourceTypes: () => get<ResourceType[]>('/vocab/resource-types'),
  graph: (project: string, asOf?: string) =>
    get<Graph>(`/projects/${project}/graph` + (asOf ? `?as_of=${encodeURIComponent(asOf)}` : '')),
  ready: (f: { project?: string; type?: string; subtree?: string; capability?: string } = {}) => {
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(f)) if (v) q.set(k, v);
    const qs = q.toString();
    return get<ReadyEntry[]>('/analytics/ready' + (qs ? '?' + qs : ''));
  },
  bottlenecks: () => get<Bottlenecks>('/analytics/bottlenecks'),
  recommendations: () => get<RecommendationsResponse>('/analytics/recommendations'),
  resourceBoard: () => get<ResourceBoardRow[]>('/analytics/resource-board'),
  history: (f: HistoryFilter = {}) => {
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(f)) if (v !== undefined && v !== '') q.set(k, String(v));
    const qs = q.toString();
    return get<{ events: EventEnvelope[] }>('/history' + (qs ? '?' + qs : ''));
  },
  settings: () => get<Weights>('/settings'),

  // mutations (If-Match versions passed where the caller has one)
  createProject: (data: { name: string; metadata?: unknown }) => req<Project>('POST', '/projects', data),
  updateProject: (id: string, data: { name: string; metadata?: unknown }, version?: number) =>
    req<Project>('PATCH', `/projects/${id}`, data, ifMatch(version)),
  deleteProject: (id: string) => req<unknown>('DELETE', `/projects/${id}`),

  createThing: (data: { project: string; name: string; type: string; parent?: string; metadata?: unknown }) =>
    req<Thing>('POST', '/things', data),
  updateThing: (id: string, data: { name: string; type: string; parent?: string; metadata?: unknown }, version?: number) =>
    req<Thing>('PATCH', `/things/${id}`, data, ifMatch(version)),
  deleteThing: (id: string) => req<unknown>('DELETE', `/things/${id}`),

  createResource: (data: { name: string; kind: string; named: boolean; capacity: number; type?: string; metadata?: unknown }) =>
    req<Resource>('POST', '/resources', data),
  updateResource: (id: string, data: { name: string; kind: string; named: boolean; capacity: number; type?: string; metadata?: unknown }, version?: number) =>
    req<Resource>('PATCH', `/resources/${id}`, data, ifMatch(version)),
  deleteResource: (id: string) => req<unknown>('DELETE', `/resources/${id}`),
  setAvailability: (id: string, available: boolean, note?: string) =>
    req<Resource>('POST', `/resources/${id}/availability`, { available, ...(note ? { note } : {}) }),
  grantCapability: (id: string, capability: string) =>
    req<Resource>('POST', `/resources/${id}/capabilities`, { capability }),
  revokeCapability: (id: string, capability: string) =>
    req<Resource>('DELETE', `/resources/${id}/capabilities/${capability}`),

  createDependency: (data: { from: string; to: string; on_abandoned?: 'block' | 'ignore' }) =>
    req<Dependency>('POST', '/dependencies', data),
  deleteDependency: (id: string) => req<unknown>('DELETE', `/dependencies/${id}`),

  createRequirement: (data: { thing: string; quantity: number; capabilities?: string[]; resource?: string }) =>
    req<Requirement>('POST', '/requirements', data),
  updateRequirement: (id: string, data: { quantity: number; capabilities?: string[]; resource?: string }, version?: number) =>
    req<Requirement>('PATCH', `/requirements/${id}`, data, ifMatch(version)),
  deleteRequirement: (id: string) => req<unknown>('DELETE', `/requirements/${id}`),

  createState: (data: { name: string; semantic: Semantic; color?: string; description?: string }) =>
    req<StateDef>('POST', '/vocab/states', data),
  updateState: (id: string, data: { name: string; semantic: Semantic; color?: string; description?: string }, version?: number) =>
    req<StateDef>('PATCH', `/vocab/states/${id}`, data, ifMatch(version)),
  deleteState: (id: string) => req<unknown>('DELETE', `/vocab/states/${id}`),
  createType: (data: { name: string; color?: string; description?: string }) =>
    req<TypeDef>('POST', '/vocab/types', data),
  updateType: (id: string, data: { name: string; color?: string; description?: string }, version?: number) =>
    req<TypeDef>('PATCH', `/vocab/types/${id}`, data, ifMatch(version)),
  deleteType: (id: string) => req<unknown>('DELETE', `/vocab/types/${id}`),
  createCapability: (data: { name: string; description?: string }) =>
    req<CapabilityDef>('POST', '/vocab/capabilities', data),
  updateCapability: (id: string, data: { name: string; description?: string }, version?: number) =>
    req<CapabilityDef>('PATCH', `/vocab/capabilities/${id}`, data, ifMatch(version)),
  deleteCapability: (id: string) => req<unknown>('DELETE', `/vocab/capabilities/${id}`),
  createResourceType: (data: { name: string; color?: string; description?: string }) =>
    req<ResourceType>('POST', '/vocab/resource-types', data),
  updateResourceType: (id: string, data: { name: string; color?: string; description?: string }, version?: number) =>
    req<ResourceType>('PATCH', `/vocab/resource-types/${id}`, data, ifMatch(version)),
  deleteResourceType: (id: string) => req<unknown>('DELETE', `/vocab/resource-types/${id}`),

  transition: (thing: string, body: { state: string; confirm?: boolean; proposal?: string }) =>
    req<TransitionResult>('POST', `/things/${thing}/transition`, body),
  repropose: (thing: string) => req<TransitionResult>('POST', `/things/${thing}/repropose`),

  batch: (mode: 'preview' | 'commit', operations: BatchOp[], expectedVersions?: Record<string, number>) =>
    req<BatchResponse>('POST', '/batch', {
      mode,
      operations,
      ...(expectedVersions ? { expected_versions: expectedVersions } : {}),
    }),

  putSettings: (w: Weights) => req<Weights>('PUT', '/settings', w),
};

function ifMatch(version?: number): Record<string, string> | undefined {
  return version === undefined ? undefined : { 'If-Match': String(version) };
}

import type {
  CatalogConfig,
  CodeCreateInput,
  CodeEntry,
  CodeEntryInput,
  CodeVersion,
  Dashboard,
  DashboardDetail,
  Dataset,
  DatasetInput,
  Location,
  Page,
  SearchResult,
  StorageBackend,
  StorageEntry,
  TagCount,
  UserSummary,
  VersionInput,
  VersionSummary,
  AssetType,
} from "../types";

// This component is mounted by booth-design's shell, so its requests resolve against the
// shell's origin and must go through booth-core's gateway at /modules/{id}/* — which strips the
// /modules/catalog prefix before forwarding to this repo's own backend routes (/api/...). A bare
// "/api/..." would hit booth-core's own API instead (the mistake booth-module-store's first
// published package made). The dev harness's Vite proxy (vite.config.ts) mimics the same
// prefix-stripping so identical paths work standalone.
const CATALOG = "/modules/catalog/api";
// booth-storage is called through the same gateway, with the user's own token, for the location
// picker and the "check location" action (docs/decisions/0002). The catalog's backend never
// calls storage.
const STORAGE = "/modules/storage/api";
// booth-core's own API, not a module's — the gateway serves it at the shell's origin with no
// /modules/{id} prefix, the same reason booth-design's own client calls a bare "/api/me". A
// lookup failing (an older deployment with no directory yet, or core just being unreachable)
// simply means the owner picker below falls back to free text, the same degradation
// LocationPicker already has for booth-storage being unavailable.
const CORE = "/api";

export type GetAccessToken = () => string | null;

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    /** The request field a validation error is about, when the server named one. */
    public field?: string,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

/** Everything a request needs from the mounting shell (ADR 0031/0033). */
export interface ApiContext {
  workspace: string;
  getAccessToken: GetAccessToken;
}

// workspace sets the X-Workspace header booth-core's gateway requires on every authenticated
// request (ADR 0025). getAccessToken is called fresh immediately before each request, never
// cached — the shell's token can be silently renewed at any time (ADR 0032/0033). A null return
// omits the Authorization header rather than sending the literal string "null".
function buildHeaders(ctx: ApiContext, init?: RequestInit): Headers {
  const headers = new Headers(init?.headers);
  headers.set("X-Workspace", ctx.workspace);
  const token = ctx.getAccessToken();
  if (token !== null) headers.set("Authorization", `Bearer ${token}`);
  return headers;
}

async function toApiError(res: Response): Promise<ApiError> {
  const text = await res.text();
  try {
    const body = JSON.parse(text) as { error?: string; field?: string };
    if (body.error) return new ApiError(res.status, body.error, body.field);
  } catch {
    // not JSON — e.g. a gateway error page; fall through to the raw text
  }
  return new ApiError(res.status, text || res.statusText || `HTTP ${res.status}`);
}

async function request<T>(ctx: ApiContext, base: string, path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(base + path, { ...init, headers: buildHeaders(ctx, init) });
  if (!res.ok) throw await toApiError(res);
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

function jsonInit(method: string, body?: unknown): RequestInit {
  return {
    method,
    headers: { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  };
}

function query(params: Record<string, string | number | string[] | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === "") continue;
    if (Array.isArray(v)) v.forEach((x) => q.append(k, x));
    else q.set(k, String(v));
  }
  const s = q.toString();
  return s ? `?${s}` : "";
}

const seg = encodeURIComponent;

// ---- config / search ---------------------------------------------------------

export const fetchConfig = (ctx: ApiContext) => request<CatalogConfig>(ctx, CATALOG, "/config");

export const search = (ctx: ApiContext, q: string, types?: AssetType[]) =>
  request<SearchResult>(ctx, CATALOG, `/search${query({ q, types: types?.join(",") })}`);

// ---- datasets ----------------------------------------------------------------

export interface DatasetFilter {
  q?: string;
  tags?: string[];
  owner?: string;
  limit?: number;
  offset?: number;
}

export const listDatasets = (ctx: ApiContext, f: DatasetFilter = {}) =>
  request<Page<Dataset>>(ctx, CATALOG, `/datasets${query({ q: f.q, tag: f.tags, owner: f.owner, limit: f.limit, offset: f.offset })}`);

export const getDataset = (ctx: ApiContext, id: string) => request<Dataset>(ctx, CATALOG, `/datasets/${seg(id)}`);

export const createDataset = (ctx: ApiContext, body: DatasetInput) =>
  request<Dataset>(ctx, CATALOG, "/datasets", jsonInit("POST", body));

export const updateDataset = (ctx: ApiContext, id: string, body: DatasetInput) =>
  request<Dataset>(ctx, CATALOG, `/datasets/${seg(id)}`, jsonInit("PUT", body));

export const deleteDataset = (ctx: ApiContext, id: string) =>
  request<void>(ctx, CATALOG, `/datasets/${seg(id)}`, { method: "DELETE" });

/** The dashboards that read a dataset — the downstream half of lineage. */
export const datasetDashboards = (ctx: ApiContext, id: string) =>
  request<{ dashboards: Dashboard[] }>(ctx, CATALOG, `/datasets/${seg(id)}/lineage`).then((r) => r.dashboards);

export const listTags = (ctx: ApiContext) => request<{ tags: TagCount[] }>(ctx, CATALOG, "/tags").then((r) => r.tags);

// ---- code --------------------------------------------------------------------

export interface CodeFilter {
  q?: string;
  owner?: string;
  language?: string;
  limit?: number;
  offset?: number;
}

export const listCode = (ctx: ApiContext, f: CodeFilter = {}) =>
  request<Page<CodeEntry>>(ctx, CATALOG, `/code${query({ q: f.q, owner: f.owner, language: f.language, limit: f.limit, offset: f.offset })}`);

export const getCode = (ctx: ApiContext, id: string) => request<CodeEntry>(ctx, CATALOG, `/code/${seg(id)}`);

export const createCode = (ctx: ApiContext, body: CodeCreateInput) =>
  request<CodeEntry>(ctx, CATALOG, "/code", jsonInit("POST", body));

export const updateCode = (ctx: ApiContext, id: string, body: CodeEntryInput) =>
  request<CodeEntry>(ctx, CATALOG, `/code/${seg(id)}`, jsonInit("PUT", body));

export const deleteCode = (ctx: ApiContext, id: string) =>
  request<void>(ctx, CATALOG, `/code/${seg(id)}`, { method: "DELETE" });

export const codeVersions = (ctx: ApiContext, id: string) =>
  request<{ versions: VersionSummary[] }>(ctx, CATALOG, `/code/${seg(id)}/versions`).then((r) => r.versions);

/** `version` may be the alias "latest". */
export const getCodeVersion = (ctx: ApiContext, id: string, version: string) =>
  request<CodeVersion>(ctx, CATALOG, `/code/${seg(id)}/versions/${seg(version)}`);

export const publishVersion = (ctx: ApiContext, id: string, body: VersionInput) =>
  request<VersionSummary>(ctx, CATALOG, `/code/${seg(id)}/versions`, jsonInit("POST", body));

// ---- dashboards (read-only) ---------------------------------------------------

export interface DashboardFilter {
  q?: string;
  owner?: string;
  source?: string;
  limit?: number;
  offset?: number;
}

export const listDashboards = (ctx: ApiContext, f: DashboardFilter = {}) =>
  request<Page<Dashboard>>(ctx, CATALOG, `/dashboards${query({ q: f.q, owner: f.owner, source: f.source, limit: f.limit, offset: f.offset })}`);

export const getDashboard = (ctx: ApiContext, id: string) => request<DashboardDetail>(ctx, CATALOG, `/dashboards/${seg(id)}`);

// ---- booth-core: owner directory (read-only; used by the owner picker) --------

/** GET /api/users?q= (ADR 0047/0052): people in the caller's active workspace whose name,
 *  username or email contains q. Callers debounce and skip an empty q — there's nothing useful
 *  to suggest before the person has typed something. */
export const searchUsers = (ctx: ApiContext, q: string) =>
  request<UserSummary[]>(ctx, CORE, `/users${query({ q, limit: 8 })}`);

// ---- booth-storage: location picker and live check -----------------------------

export const fetchBackends = (ctx: ApiContext) => request<StorageBackend[]>(ctx, STORAGE, "/backends");

export type LocationCheck =
  | { state: "found"; kind: "folder" | "file" }
  | { state: "missing-path" }
  | { state: "missing-backend" }
  /** booth-storage isn't installed, or couldn't be reached, or refused: the reason is in message. */
  | { state: "unavailable"; message: string };

const parentOf = (p: string) => (p.includes("/") ? p.slice(0, p.lastIndexOf("/")) : "");

/**
 * Resolves a stored {backendId, path} live, through booth-storage's own API with the caller's own
 * token (ADR 0045; docs/decisions/0002). A reference is never a promise the data exists, so this is
 * a check the user asks for, not something the catalog trusts or caches.
 *
 * `path` may name a folder or a single object: a folder is found by listing beneath it, an object by
 * looking for it among its parent's entries.
 */
export async function checkLocation(ctx: ApiContext, loc: Location): Promise<LocationCheck> {
  try {
    await request<StorageBackend>(ctx, STORAGE, `/backends/${seg(loc.backendId)}`);
  } catch (err) {
    if (err instanceof ApiError && err.status === 404 && /backend not found/i.test(err.message)) return { state: "missing-backend" };
    return { state: "unavailable", message: err instanceof Error ? err.message : String(err) };
  }
  if (loc.path === "") return { state: "found", kind: "folder" };

  const list = (prefix: string, cursor?: string) =>
    request<{ entries: StorageEntry[]; nextCursor?: string }>(
      ctx,
      STORAGE,
      `/backends/${seg(loc.backendId)}/objects${query({ prefix, cursor, limit: 200 })}`,
    );
  try {
    if ((await list(loc.path)).entries.length > 0) return { state: "found", kind: "folder" };
    // Not a folder with contents: look for an object of exactly this name beside its siblings.
    let cursor: string | undefined;
    for (let page = 0; page < 5; page++) {
      const res = await list(parentOf(loc.path), cursor);
      const hit = res.entries.find((e) => e.path.replace(/\/$/, "") === loc.path);
      if (hit) return { state: "found", kind: hit.isDir ? "folder" : "file" };
      if (!res.nextCursor) break;
      cursor = res.nextCursor;
    }
    return { state: "missing-path" };
  } catch (err) {
    return { state: "unavailable", message: err instanceof Error ? err.message : String(err) };
  }
}

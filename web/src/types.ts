// Mirrors the JSON shapes in internal/api, internal/data, internal/code and
// internal/dashboards — kept as hand-written types rather than generated, since this repo has
// no shared-schema tooling (the same trade-off booth-storage and booth-module-store made). Keep
// the two in sync by hand.

/** Caller's role in the active workspace (ADR 0025). Matches contracts/ui-integration.md's
 *  NativeModuleProps contract (ADR 0031). */
export type WorkspaceRole = "owner" | "editor" | "viewer";

/** A booth-storage location as the platform-wide {backendId, path} pair (ADR 0045). */
export interface Location {
  backendId: string;
  /** "/"-separated, no leading or trailing slash; "" is the backend root. */
  path: string;
}

export interface Page<T> {
  items: T[];
  /** The full match count, not the page size. */
  total: number;
}

// ---- datasets ---------------------------------------------------------------

export interface Column {
  name: string;
  type: string;
  description?: string;
}

export interface Dataset {
  id: string;
  name: string;
  description: string;
  location: Location;
  schema: Column[];
  tags: string[];
  owner: string;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
}

export interface DatasetInput {
  name: string;
  description: string;
  location: Location;
  schema: Column[];
  tags: string[];
  /** Empty on create means "the registering user"; empty on update means "keep the current owner". */
  owner: string;
}

export interface TagCount {
  tag: string;
  count: number;
}

// ---- code -------------------------------------------------------------------

export interface VersionSummary {
  version: string;
  /** Publication order within the entry; the highest is "latest". */
  seq: number;
  notes: string;
  sizeBytes: number;
  publishedBy: string;
  publishedAt: string;
}

export interface CodeVersion extends VersionSummary {
  source: string;
}

export interface CodeEntry {
  id: string;
  name: string;
  description: string;
  owner: string;
  language: string;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
  latestVersion: VersionSummary | null;
  versionCount: number;
}

export interface CodeCreateInput {
  name: string;
  description: string;
  owner: string;
  language: string;
  version: string;
  source: string;
  notes: string;
}

export interface CodeEntryInput {
  name: string;
  description: string;
  owner: string;
  language: string;
}

export interface VersionInput {
  version: string;
  source: string;
  notes: string;
}

// ---- dashboards -------------------------------------------------------------

export interface Dashboard {
  id: string;
  /** The publishing module's id: "superset", "metabase", "streamlit". */
  sourceModule: string;
  externalId: string;
  name: string;
  description: string;
  owner: string;
  /** A shell-relative path that opens the dashboard, or "". */
  path: string;
  lineageComplete: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface DatasetRef {
  id: string;
  name: string;
}

/** What a dashboard says it reads, as published (docs/decisions/0001). */
export interface Source {
  type: "dataset" | "location" | "external";
  datasetId?: string;
  backendId?: string;
  path?: string;
  system?: string;
  name?: string;
}

/** A source plus the catalog datasets it resolves to; empty means it did not resolve. */
export interface ResolvedSource extends Source {
  datasets: DatasetRef[];
}

export interface DashboardDetail extends Dashboard {
  lineage: { complete: boolean; sources: ResolvedSource[] };
}

// ---- search / config --------------------------------------------------------

export type AssetType = "data" | "code" | "dashboard";

export interface SearchHit {
  type: AssetType;
  id: string;
  name: string;
  description: string;
  owner: string;
  /** The publishing module, for a dashboard. */
  source?: string;
}

export interface SearchResult {
  query: string;
  hits: SearchHit[];
}

export interface CatalogConfig {
  maxCodeSourceBytes: number;
  dashboardEvents: { state: string; detail?: string };
}

// ---- booth-storage (read-only; used by the location picker) ------------------

export interface StorageBackend {
  id: string;
  displayName: string;
  kind: string;
  location: string;
}

export interface StorageEntry {
  path: string;
  isDir?: boolean;
}

/** "Sep 19, 2026" — dates in tables and detail views. Empty for an unparseable value. */
export function formatDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  return d.toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return "";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}

/** How a storage location reads to a person: "lake:warehouse/orders" (the backend root is "lake:/"). */
export function formatLocation(loc: { backendId: string; path: string }): string {
  return `${loc.backendId}:${loc.path === "" ? "/" : loc.path}`;
}

/** The display name of a dashboard-publishing module. Unknown modules show their id. */
export function moduleLabel(id: string): string {
  return ({ superset: "Superset", metabase: "Metabase", streamlit: "Streamlit" } as Record<string, string>)[id] ?? id;
}

/** "about 1,696 rows" / "about 1 row" / "about 0 rows" — ADR 0116 requires "about": this is
 *  Postgres's own planner estimate (pg_class.reltuples), never an exact count, and the catalog
 *  never queries the table to get a real one (ADR 0102 item 4). null/undefined means unknown. */
export function formatApproxRows(n: number | null | undefined): string {
  if (n === null || n === undefined) return "unknown";
  return `about ${n.toLocaleString()} row${n === 1 ? "" : "s"}`;
}

// ---- booth-database deep links (ADR 0116) -------------------------------------------------
//
// Provisional: booth-database's session had not yet reported its real navPath or route shapes
// when this was built (ADR 0116 §1/§3 — the row-preview and SQL-console endpoints are its own
// build item, not this repo's). This assumes the fleet's one-word navPath convention
// ("/database", matching contracts/module-manifest.md's navPath examples) and a plausible
// nested route shape under it. Both are kept in exactly these two functions — nowhere else in
// this package builds a booth-database path by hand — so correcting them once booth-database's
// real routes are known is a one-place change.

const DATABASE_MODULE_NAV_PATH = "/database";

/** The deep link for "Preview rows" on a format: "postgres" dataset. */
export function previewRowsPath(table: { schema: string; name: string }): string {
  return `${DATABASE_MODULE_NAV_PATH}/tables/${encodeURIComponent(table.schema)}/${encodeURIComponent(table.name)}/preview`;
}

/** The deep link for "Open in SQL console" on a format: "postgres" dataset. */
export function sqlConsolePath(table: { schema: string; name: string }): string {
  return `${DATABASE_MODULE_NAV_PATH}/sql?schema=${encodeURIComponent(table.schema)}&table=${encodeURIComponent(table.name)}`;
}

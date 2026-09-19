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

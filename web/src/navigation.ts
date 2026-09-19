import { useSyncExternalStore } from "react";

// The shell (booth-design) matches a module's navPath — and anything beneath it (its
// findModuleForPath does a longest-prefix match) — to the one component registered for the module
// id, but NativeModuleProps (ADR 0031/0033) carries no route. So this package reads
// window.location itself: the shell is a browser-router SPA, so the address bar is the one source
// of truth that's always right, and it needs no contract change. This is the same workaround
// booth-storage uses (its docs/decisions/0002); if the shell ever passes a route prop, this file
// is the only place that changes.

export const DEFAULT_BASE_PATH = "/catalog";

export type Route =
  | { name: "data" }
  | { name: "data-new" }
  | { name: "data-detail"; id: string }
  | { name: "data-edit"; id: string }
  | { name: "code" }
  | { name: "code-new" }
  | { name: "code-detail"; id: string; version?: string }
  | { name: "code-edit"; id: string }
  | { name: "code-publish"; id: string }
  | { name: "dashboards" }
  | { name: "dashboard-detail"; id: string }
  | { name: "search"; q: string };

/** Which top-level section a route belongs to, for the section tabs. */
export type Section = "data" | "code" | "dashboards";

export function sectionOf(route: Route): Section | null {
  if (route.name.startsWith("data")) return "data";
  if (route.name.startsWith("code")) return "code";
  if (route.name.startsWith("dashboard")) return "dashboards";
  return null;
}

/** Parses the address bar into a Route. Anything unrecognised is the dataset list: a stale
 *  bookmark should land somewhere useful, not on an error. */
export function parseRoute(pathname: string, search: string, basePath: string = DEFAULT_BASE_PATH): Route {
  const rel = pathname === basePath ? "" : pathname.startsWith(basePath + "/") ? pathname.slice(basePath.length + 1) : "";
  const parts = rel.split("/").filter(Boolean).map(decodeURIComponent);
  const [area, second, third, fourth] = parts;

  switch (area) {
    case "data":
      if (second === "new" && parts.length === 2) return { name: "data-new" };
      if (second && third === "edit" && parts.length === 3) return { name: "data-edit", id: second };
      if (second && parts.length === 2) return { name: "data-detail", id: second };
      break;
    case "code":
      if (second === "new" && parts.length === 2) return { name: "code-new" };
      if (second && third === "edit" && parts.length === 3) return { name: "code-edit", id: second };
      if (second && third === "publish" && parts.length === 3) return { name: "code-publish", id: second };
      if (second && third === "v" && fourth && parts.length === 4) return { name: "code-detail", id: second, version: fourth };
      if (second && parts.length === 2) return { name: "code-detail", id: second };
      if (parts.length === 1) return { name: "code" };
      break;
    case "dashboards":
      if (second && parts.length === 2) return { name: "dashboard-detail", id: second };
      if (parts.length === 1) return { name: "dashboards" };
      break;
    case "search":
      return { name: "search", q: new URLSearchParams(search).get("q") ?? "" };
  }
  return { name: "data" };
}

const e = encodeURIComponent;

/** The inverse of parseRoute. */
export function routePath(route: Route, basePath: string = DEFAULT_BASE_PATH): string {
  switch (route.name) {
    case "data":
      return `${basePath}/data`;
    case "data-new":
      return `${basePath}/data/new`;
    case "data-detail":
      return `${basePath}/data/${e(route.id)}`;
    case "data-edit":
      return `${basePath}/data/${e(route.id)}/edit`;
    case "code":
      return `${basePath}/code`;
    case "code-new":
      return `${basePath}/code/new`;
    case "code-detail":
      return route.version ? `${basePath}/code/${e(route.id)}/v/${e(route.version)}` : `${basePath}/code/${e(route.id)}`;
    case "code-edit":
      return `${basePath}/code/${e(route.id)}/edit`;
    case "code-publish":
      return `${basePath}/code/${e(route.id)}/publish`;
    case "dashboards":
      return `${basePath}/dashboards`;
    case "dashboard-detail":
      return `${basePath}/dashboards/${e(route.id)}`;
    case "search":
      return `${basePath}/search?q=${e(route.q)}`;
  }
}

const NAVIGATE_EVENT = "booth-catalog:navigate";

function subscribe(onChange: () => void): () => void {
  window.addEventListener("popstate", onChange);
  window.addEventListener(NAVIGATE_EVENT, onChange);
  return () => {
    window.removeEventListener("popstate", onChange);
    window.removeEventListener(NAVIGATE_EVENT, onChange);
  };
}

/** The current pathname+search, re-rendering on back/forward and on this package's own
 *  navigations. A navigation the shell's router makes via pushState doesn't fire an event, but it
 *  re-renders the mounted component, and useSyncExternalStore re-reads the snapshot on every render. */
export function useLocation(): { pathname: string; search: string } {
  const pathname = useSyncExternalStore(subscribe, () => window.location.pathname, () => "/");
  const search = useSyncExternalStore(subscribe, () => window.location.search, () => "");
  return { pathname, search };
}

/** Navigates to `path` inside the shell without a page reload.
 *
 *  A full reload would drop booth-design's in-memory access token (ADR 0032) and force a fresh
 *  login redirect. So: pushState, then a synthetic popstate — which is what the shell's browser
 *  router listens for to notice a location change. Used only when the shell doesn't supply its own
 *  `onNavigate`. */
export function defaultNavigate(path: string): void {
  window.history.pushState({}, "", path);
  window.dispatchEvent(new PopStateEvent("popstate"));
  window.dispatchEvent(new Event(NAVIGATE_EVENT));
}

/** Whether `path` is safe to link to from catalog data: a path inside this origin, never a
 *  scheme-bearing or protocol-relative URL. A dashboard's `path` comes from a third-party module
 *  (the server validates it too — this is the second lock). */
export function isShellPath(path: string): boolean {
  return path.startsWith("/") && !path.startsWith("//") && !path.includes("\\");
}

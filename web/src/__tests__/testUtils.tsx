import { render } from "@testing-library/react";
import { vi } from "vitest";
import { CatalogApp } from "../CatalogApp";
import type { WorkspaceRole } from "../types";

export interface Call {
  method: string;
  path: string;
  query: URLSearchParams;
  headers: Headers;
  body: unknown;
}

export interface Reply {
  status?: number;
  json?: unknown;
  text?: string;
}

type Route = Reply | ((call: Call) => Reply);

/** Replaces global fetch with a router keyed "METHOD /path" (no query string). Anything
 *  unrouted is a 404 that is also recorded, so a test can assert what was — and wasn't — called. */
export function mockFetch(routes: Record<string, Route>) {
  const calls: Call[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), "http://localhost");
    const method = (init?.method ?? "GET").toUpperCase();
    const raw = init?.body;
    const call: Call = {
      method,
      path: url.pathname,
      query: url.searchParams,
      headers: new Headers(init?.headers),
      body: typeof raw === "string" ? JSON.parse(raw) : raw,
    };
    calls.push(call);
    const route = routes[`${method} ${url.pathname}`];
    const reply: Reply = route === undefined ? { status: 404, text: "not routed" } : typeof route === "function" ? route(call) : route;
    const status = reply.status ?? 200;
    const bodyText = reply.text ?? (reply.json === undefined ? "" : JSON.stringify(reply.json));
    return new Response(status === 204 ? null : bodyText, { status });
  });
  vi.stubGlobal("fetch", fn);
  return { calls, fn, called: (method: string, path: string) => calls.filter((c) => c.method === method && c.path === path) };
}

export const CAT = "/modules/catalog/api";
export const STO = "/modules/storage/api";
export const CORE = "/api";

/** Renders the whole app at `path`, as booth-design's shell would mount it. */
export function renderApp(path: string, role: WorkspaceRole = "editor", token: string | null = "tok") {
  window.history.replaceState({}, "", path);
  return render(<CatalogApp workspace="acme" role={role} theme="light" getAccessToken={() => token} />);
}

export const dataset = (over: Record<string, unknown> = {}) => ({
  id: "ds-1",
  name: "orders",
  description: "One row per order",
  location: { backendId: "lake", path: "warehouse/orders" },
  schema: [{ name: "id", type: "bigint", description: "key" }],
  tags: ["finance", "pii"],
  owner: "alice",
  createdBy: "sub-alice",
  createdAt: "2026-09-01T12:00:00Z",
  updatedAt: "2026-09-19T12:00:00Z",
  ...over,
});

export const codeEntry = (over: Record<string, unknown> = {}) => ({
  id: "c-1",
  name: "clean_emails",
  description: "Strips whitespace",
  owner: "bob",
  language: "python",
  createdBy: "sub-bob",
  createdAt: "2026-09-01T12:00:00Z",
  updatedAt: "2026-09-19T12:00:00Z",
  versionCount: 2,
  latestVersion: { version: "1.1.0", seq: 2, notes: "faster", sizeBytes: 30, publishedBy: "bob", publishedAt: "2026-09-19T12:00:00Z" },
  ...over,
});

export const dashboard = (over: Record<string, unknown> = {}) => ({
  id: "b-1",
  sourceModule: "superset",
  externalId: "42",
  name: "Revenue",
  description: "Quarterly revenue",
  owner: "carol",
  path: "/superset/dashboard/42",
  lineageComplete: false,
  createdAt: "2026-09-01T12:00:00Z",
  updatedAt: "2026-09-19T12:00:00Z",
  ...over,
});

export const page = <T,>(items: T[]) => ({ items, total: items.length });

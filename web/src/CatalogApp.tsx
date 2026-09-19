import { useEffect, useMemo, useState, type FormEvent } from "react";
import type { ApiContext, GetAccessToken } from "./api/client";
import { Button, Link, inputClass } from "./components/ui";
import type { ViewCtx } from "./context";
import { DEFAULT_BASE_PATH, defaultNavigate, parseRoute, routePath, sectionOf, useLocation, type Route, type Section } from "./navigation";
import type { WorkspaceRole } from "./types";
import { CodeDetail } from "./views/CodeDetail";
import { CodeCreateForm, CodeEditForm, CodePublishForm } from "./views/CodeForms";
import { CodeList } from "./views/CodeList";
import { DashboardDetail, DashboardList } from "./views/DashboardViews";
import { DataDetail } from "./views/DataDetail";
import { DataList } from "./views/DataList";
import { DatasetForm } from "./views/DatasetForm";
import { SearchView } from "./views/SearchView";

/**
 * Props contract agreed with booth-design and pinned into contracts/ui-integration.md by ADR 0031
 * (workspace/role/theme) and ADR 0033 (getAccessToken): plain React props, not shared context, so
 * this package never depends on anything booth-design exports (that would invert the dependency
 * direction ADR 0030 established).
 *
 * The optional props below the contract four are this package's own, none required.
 */
export interface CatalogAppProps {
  /** Active workspace slug (ADR 0025) — required for every API call this makes. */
  workspace: string;
  /** Caller's role (ADR 0025). Gates which controls this UI offers; booth-catalog's backend
   *  independently enforces the same rules on every request (ADR 0023, ADR 0041), so this is a UX
   *  nicety and never the security boundary. */
  role: WorkspaceRole;
  theme: "dark" | "light";
  /** Returns booth-design's current bearer token (ADR 0032), or null when not authenticated.
   *  Called fresh before every request, never cached (ADR 0033). */
  getAccessToken: GetAccessToken;

  /** The manifest's navPath: every catalog page lives beneath it. Default "/catalog". */
  basePath?: string;
  /** How to move between pages. Defaults to in-place history navigation, which the shell's router
   *  picks up without a page reload; pass the shell's own navigate function if it offers one. */
  onNavigate?: (path: string) => void;
}

const SECTIONS: { section: Section; label: string; route: Route }[] = [
  { section: "data", label: "Datasets", route: { name: "data" } },
  { section: "code", label: "Code", route: { name: "code" } },
  { section: "dashboards", label: "Dashboards", route: { name: "dashboards" } },
];

/**
 * The native-mode component booth-design's shell mounts for booth-catalog (ADR 0030), published as
 * @projectbooth/catalog-ui. One component, three catalogs — datasets, code and dashboards — with a
 * search box that spans all three.
 */
export function CatalogApp({ workspace, role, theme, getAccessToken, basePath = DEFAULT_BASE_PATH, onNavigate = defaultNavigate }: CatalogAppProps) {
  const { pathname, search } = useLocation();
  const route = parseRoute(pathname, search, basePath);

  // Stable across renders unless the workspace or token accessor actually changes, so effects keyed
  // on it don't refire spuriously.
  const api = useMemo<ApiContext>(() => ({ workspace, getAccessToken }), [workspace, getAccessToken]);
  const v = useMemo<ViewCtx>(
    () => ({
      api,
      canWrite: role === "owner" || role === "editor",
      href: (r) => routePath(r, basePath),
      go: (r) => onNavigate(routePath(r, basePath)),
      goPath: onNavigate,
    }),
    [api, role, basePath, onNavigate],
  );

  const active = sectionOf(route);

  return (
    <div data-theme={theme} className="flex flex-col gap-5 text-slate-900 dark:text-slate-100">
      <header className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 pb-3 dark:border-slate-800">
        <nav aria-label="Catalog sections" className="flex gap-1">
          {SECTIONS.map((s) => (
            <Link
              key={s.section}
              href={v.href(s.route)}
              onNavigate={v.goPath}
              aria-current={active === s.section ? "page" : undefined}
              className={`rounded-md px-3 py-1.5 text-sm font-medium ${
                active === s.section
                  ? "bg-indigo-600 text-white"
                  : "text-slate-700 hover:bg-slate-100 dark:text-slate-300 dark:hover:bg-slate-800"
              }`}
            >
              {s.label}
            </Link>
          ))}
        </nav>
        <SearchBox initial={route.name === "search" ? route.q : ""} onSearch={(q) => v.go({ name: "search", q })} />
      </header>
      <main>{renderRoute(route, v)}</main>
    </div>
  );
}

/** The catalog-wide search box. Searching navigates to the results page, so a result set has a
 *  URL of its own (shareable, and back returns to it). */
function SearchBox({ initial, onSearch }: { initial: string; onSearch: (q: string) => void }) {
  const [text, setText] = useState(initial);
  // Following a link to a results page (or back/forward) shows that page's query.
  useEffect(() => setText(initial), [initial]);

  function submit(ev: FormEvent) {
    ev.preventDefault();
    if (text.trim() !== "") onSearch(text.trim());
  }
  return (
    <form role="search" className="flex items-center gap-2" onSubmit={submit}>
      <input
        aria-label="Search the catalog"
        type="search"
        className={`${inputClass} w-64`}
        placeholder="Search datasets, code and dashboards"
        value={text}
        onChange={(e) => setText(e.target.value)}
      />
      <Button type="submit">Search</Button>
    </form>
  );
}

function renderRoute(route: Route, v: ViewCtx) {
  // A write page reached without write access (a bookmark, a role change) shows the read view
  // instead of a form the server would refuse to accept.
  if (!v.canWrite) {
    switch (route.name) {
      case "data-new":
        return <DataList v={v} />;
      case "data-edit":
        return <DataDetail v={v} id={route.id} />;
      case "code-new":
        return <CodeList v={v} />;
      case "code-edit":
      case "code-publish":
        return <CodeDetail v={v} id={route.id} />;
    }
  }
  switch (route.name) {
    case "data":
      return <DataList v={v} />;
    case "data-new":
      return <DatasetForm v={v} />;
    case "data-edit":
      return <DatasetForm v={v} id={route.id} />;
    case "data-detail":
      return <DataDetail v={v} id={route.id} />;
    case "code":
      return <CodeList v={v} />;
    case "code-new":
      return <CodeCreateForm v={v} />;
    case "code-edit":
      return <CodeEditForm v={v} id={route.id} />;
    case "code-publish":
      return <CodePublishForm v={v} id={route.id} />;
    case "code-detail":
      return <CodeDetail v={v} id={route.id} version={route.version} />;
    case "dashboards":
      return <DashboardList v={v} />;
    case "dashboard-detail":
      return <DashboardDetail v={v} id={route.id} />;
    case "search":
      return <SearchView v={v} q={route.q} />;
  }
}

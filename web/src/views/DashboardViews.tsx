import { useEffect, useState } from "react";
import { fetchConfig, getDashboard, listDashboards } from "../api/client";
import { Async, Banner, Chip, EmptyState, Link, Meta, PageHeader, Pager, Section, inputClass, linkClass } from "../components/ui";
import type { ViewCtx } from "../context";
import { formatDate, formatLocation, moduleLabel } from "../format";
import { useDebounced, useLoad } from "../hooks";
import { isShellPath } from "../navigation";
import type { ResolvedSource } from "../types";

const PAGE = 25;
const MODULES = ["superset", "metabase", "streamlit"];

/** Explains a dashboard-event subscription that isn't healthy: without it the list may be stale or
 *  empty for a reason the user can't otherwise see. */
function EventsBanner({ v }: { v: ViewCtx }) {
  const cfg = useLoad(() => fetchConfig(v.api), [v.api.workspace]);
  if (cfg.state.status !== "ready") return null;
  const { state, detail } = cfg.state.data.dashboardEvents;
  if (state === "subscribed") return null;
  if (state === "disabled")
    return <Banner tone="warn">Dashboard indexing is switched off for this deployment (no event bus is configured), so no dashboards will appear here.</Banner>;
  return (
    <Banner tone="warn">
      The catalog isn't currently receiving dashboard updates ({state}
      {detail ? `: ${detail}` : ""}). What's listed may be out of date; changes made in the meantime will arrive once it reconnects.
    </Banner>
  );
}

/** Dashboards indexed from what the dashboard modules publish. There is no create or edit here —
 *  a dashboard is built in its own tool and appears because that tool tells the catalog about it. */
export function DashboardList({ v }: { v: ViewCtx }) {
  const [text, setText] = useState("");
  const [owner, setOwner] = useState("");
  const [source, setSource] = useState("");
  const [offset, setOffset] = useState(0);
  const q = useDebounced(text.trim());
  const ownerQ = useDebounced(owner.trim());
  useEffect(() => setOffset(0), [q, ownerQ, source]);

  const list = useLoad(() => listDashboards(v.api, { q, owner: ownerQ, source, limit: PAGE, offset }), [v.api.workspace, q, ownerQ, source, offset]);
  const filtered = q !== "" || ownerQ !== "" || source !== "";

  return (
    <div className="flex flex-col gap-4">
      <PageHeader title="Dashboards" subtitle="Dashboards and apps from Superset, Metabase and Streamlit, with the datasets they read." />
      <EventsBanner v={v} />

      <div className="flex flex-wrap items-end gap-3">
        <label className="flex min-w-[14rem] flex-1 flex-col gap-1 text-xs font-medium text-slate-600 dark:text-slate-300">
          Filter dashboards
          <input className={inputClass} type="search" placeholder="Name or description" value={text} onChange={(e) => setText(e.target.value)} />
        </label>
        <label className="flex w-44 flex-col gap-1 text-xs font-medium text-slate-600 dark:text-slate-300">
          Tool
          <select className={inputClass} value={source} onChange={(e) => setSource(e.target.value)}>
            <option value="">All tools</option>
            {MODULES.map((m) => (
              <option key={m} value={m}>
                {moduleLabel(m)}
              </option>
            ))}
          </select>
        </label>
        <label className="flex w-44 flex-col gap-1 text-xs font-medium text-slate-600 dark:text-slate-300">
          Owner
          <input className={inputClass} placeholder="Exact owner" value={owner} onChange={(e) => setOwner(e.target.value)} />
        </label>
      </div>

      <Async state={list.state}>
        {(page) =>
          page.items.length === 0 ? (
            filtered ? (
              <EmptyState title="No dashboards match those filters." />
            ) : (
              <EmptyState title="No dashboards indexed yet.">
                Dashboards appear here when Superset, Metabase or Streamlit is installed and publishes them. They can't be added from the catalog.
              </EmptyState>
            )
          ) : (
            <>
              <div className="overflow-x-auto rounded-lg border border-slate-200 dark:border-slate-700">
                <table className="w-full text-left text-sm">
                  <thead className="bg-slate-50 text-xs uppercase tracking-wide text-slate-500 dark:bg-slate-900 dark:text-slate-400">
                    <tr>
                      <th className="px-3 py-2">Name</th>
                      <th className="px-3 py-2">Tool</th>
                      <th className="px-3 py-2">Owner</th>
                      <th className="px-3 py-2">Updated</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-200 dark:divide-slate-800">
                    {page.items.map((d) => (
                      <tr key={d.id}>
                        <td className="px-3 py-2 align-top">
                          <Link className={`font-medium ${linkClass}`} href={v.href({ name: "dashboard-detail", id: d.id })} onNavigate={v.goPath}>
                            {d.name}
                          </Link>
                          {d.description && <p className="mt-0.5 line-clamp-2 max-w-md text-xs text-slate-500 dark:text-slate-400">{d.description}</p>}
                        </td>
                        <td className="px-3 py-2 align-top">
                          <Chip tone="sky">{moduleLabel(d.sourceModule)}</Chip>
                        </td>
                        <td className="px-3 py-2 align-top text-slate-700 dark:text-slate-300">{d.owner || "—"}</td>
                        <td className="whitespace-nowrap px-3 py-2 align-top text-slate-500 dark:text-slate-400">{formatDate(d.updatedAt)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <Pager total={page.total} offset={offset} limit={PAGE} onChange={setOffset} />
            </>
          )
        }
      </Async>
    </div>
  );
}

/** What one lineage source says, in words. */
function describeSource(s: ResolvedSource): string {
  switch (s.type) {
    case "dataset":
      return `Catalog dataset ${s.datasetId}`;
    case "location":
      return `Storage location ${formatLocation({ backendId: s.backendId ?? "", path: s.path ?? "" })}`;
    case "external":
      return `${s.name}${s.system ? ` (${s.system})` : ""}`;
  }
}

/** One dashboard, and the upstream half of lineage: what it reads, resolved against the dataset
 *  catalog as it is now. Sources that don't resolve are shown, and labelled as such, rather than
 *  hidden or guessed at. */
export function DashboardDetail({ v, id }: { v: ViewCtx; id: string }) {
  const d = useLoad(() => getDashboard(v.api, id), [v.api.workspace, id]);
  return (
    <div className="flex flex-col gap-6">
      <Async state={d.state}>
        {(b) => (
          <>
            <PageHeader
              title={b.name}
              subtitle={`Dashboard · ${moduleLabel(b.sourceModule)}`}
              actions={
                // The path comes from another module; the server validates it and this is the second lock.
                b.path !== "" &&
                isShellPath(b.path) && (
                  <Link className="rounded-md bg-indigo-600 px-3 py-1.5 text-sm font-medium text-white hover:bg-indigo-500" href={b.path} onNavigate={v.goPath}>
                    Open in {moduleLabel(b.sourceModule)}
                  </Link>
                )
              }
            />
            {b.description && <p className="max-w-3xl whitespace-pre-wrap text-sm text-slate-700 dark:text-slate-300">{b.description}</p>}
            <dl className="grid grid-cols-2 gap-4 sm:grid-cols-4">
              <Meta label="Owner">{b.owner || "—"}</Meta>
              <Meta label="Tool">{moduleLabel(b.sourceModule)}</Meta>
              <Meta label="Tool's ID">
                <code>{b.externalId}</code>
              </Meta>
              <Meta label="Last changed">{formatDate(b.updatedAt)}</Meta>
            </dl>

            <Section title="Reads from">
              <Banner tone={b.lineage.complete ? "info" : "warn"}>
                {b.lineage.complete
                  ? "The publisher reports this list as complete."
                  : "The publisher doesn't claim this list is complete — the dashboard may read more than is shown."}
              </Banner>
              {b.lineage.sources.length === 0 ? (
                <p className="text-sm text-slate-500 dark:text-slate-400">No data sources were reported for this dashboard.</p>
              ) : (
                <ul className="flex flex-col gap-2">
                  {b.lineage.sources.map((s, i) => (
                    <li key={i} className="rounded-lg border border-slate-200 px-3 py-2 dark:border-slate-700">
                      <p className="text-sm text-slate-800 dark:text-slate-200">{describeSource(s)}</p>
                      {s.datasets.length > 0 ? (
                        <ul className="mt-1 flex flex-col gap-0.5">
                          {s.datasets.map((ds) => (
                            <li key={ds.id} className="text-sm">
                              →{" "}
                              <Link className={linkClass} href={v.href({ name: "data-detail", id: ds.id })} onNavigate={v.goPath}>
                                {ds.name}
                              </Link>
                            </li>
                          ))}
                        </ul>
                      ) : (
                        <p className="mt-1">
                          <Chip tone="amber">Not in the catalog</Chip>{" "}
                          <span className="text-xs text-slate-500 dark:text-slate-400">
                            {s.type === "external"
                              ? "The tool named this itself; the catalog can't tell which dataset it is."
                              : "No registered dataset matches it. Register one to connect them."}
                          </span>
                        </p>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </Section>
          </>
        )}
      </Async>
    </div>
  );
}

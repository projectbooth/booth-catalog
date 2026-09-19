import { useState } from "react";
import { ApiError, datasetDashboards, deleteDataset, getDataset } from "../api/client";
import { LocationCheck } from "../components/LocationPicker";
import { Async, Banner, Button, Chip, Link, Meta, PageHeader, Section, linkClass } from "../components/ui";
import type { ViewCtx } from "../context";
import { formatDate, formatLocation, moduleLabel } from "../format";
import { errorMessage, useLoad } from "../hooks";

/** One dataset: its metadata, schema, where it lives (with a live check), and — the downstream half
 *  of lineage — the dashboards that read it. */
export function DataDetail({ v, id }: { v: ViewCtx; id: string }) {
  const ds = useLoad(() => getDataset(v.api, id), [v.api.workspace, id]);
  // Lineage is secondary: it loads on its own so a slow or failing lineage lookup never hides the dataset.
  const readers = useLoad(() => datasetDashboards(v.api, id), [v.api.workspace, id]);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function remove() {
    setBusy(true);
    setError(null);
    try {
      await deleteDataset(v.api, id);
      v.go({ name: "data" });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : errorMessage(err));
      setBusy(false);
      setConfirming(false);
    }
  }

  return (
    <div className="flex flex-col gap-6">
      <Async state={ds.state}>
        {(d) => (
          <>
            <PageHeader
              title={d.name}
              subtitle="Dataset"
              actions={
                v.canWrite &&
                (confirming ? (
                  <>
                    <span className="text-sm text-slate-600 dark:text-slate-300">Delete this catalog entry? The data in storage is not touched.</span>
                    <Button variant="danger" disabled={busy} onClick={remove}>
                      {busy ? "Deleting…" : "Confirm delete"}
                    </Button>
                    <Button disabled={busy} onClick={() => setConfirming(false)}>
                      Cancel
                    </Button>
                  </>
                ) : (
                  <>
                    <Button onClick={() => v.go({ name: "data-edit", id })}>Edit</Button>
                    <Button variant="danger" onClick={() => setConfirming(true)}>
                      Delete
                    </Button>
                  </>
                ))
              }
            />
            {error && <Banner tone="error">{error}</Banner>}

            {d.description && <p className="max-w-3xl whitespace-pre-wrap text-sm text-slate-700 dark:text-slate-300">{d.description}</p>}

            <dl className="grid grid-cols-2 gap-4 sm:grid-cols-4">
              <Meta label="Owner">{d.owner || "—"}</Meta>
              <Meta label="Registered">{formatDate(d.createdAt)}</Meta>
              <Meta label="Updated">{formatDate(d.updatedAt)}</Meta>
              <Meta label="Tags">
                {d.tags.length === 0 ? (
                  "—"
                ) : (
                  <span className="flex flex-wrap gap-1">
                    {d.tags.map((t) => (
                      <Chip key={t}>{t}</Chip>
                    ))}
                  </span>
                )}
              </Meta>
            </dl>

            <Section title="Storage location">
              <p className="font-mono text-sm text-slate-800 dark:text-slate-200">{formatLocation(d.location)}</p>
              <p className="text-xs text-slate-500 dark:text-slate-400">
                Backend <code>{d.location.backendId}</code>, path <code>{d.location.path === "" ? "(the backend root)" : d.location.path}</code>. This is a reference, not a guarantee the data is there now.
              </p>
              <LocationCheck api={v.api} location={d.location} />
            </Section>

            <Section title={`Schema (${d.schema.length} column${d.schema.length === 1 ? "" : "s"})`}>
              {d.schema.length === 0 ? (
                <p className="text-sm text-slate-500 dark:text-slate-400">No schema has been described for this dataset.</p>
              ) : (
                <div className="overflow-x-auto rounded-lg border border-slate-200 dark:border-slate-700">
                  <table className="w-full text-left text-sm">
                    <thead className="bg-slate-50 text-xs uppercase tracking-wide text-slate-500 dark:bg-slate-900 dark:text-slate-400">
                      <tr>
                        <th className="px-3 py-2">Column</th>
                        <th className="px-3 py-2">Type</th>
                        <th className="px-3 py-2">Description</th>
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-slate-200 dark:divide-slate-800">
                      {d.schema.map((c) => (
                        <tr key={c.name}>
                          <td className="px-3 py-2 font-mono text-xs">{c.name}</td>
                          <td className="px-3 py-2 font-mono text-xs text-slate-600 dark:text-slate-300">{c.type}</td>
                          <td className="px-3 py-2 text-slate-600 dark:text-slate-300">{c.description}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </Section>
          </>
        )}
      </Async>

      <Section title="Read by dashboards">
        <Async state={readers.state}>
          {(list) =>
            list.length === 0 ? (
              <p className="text-sm text-slate-500 dark:text-slate-400">No indexed dashboard is known to read this dataset.</p>
            ) : (
              <ul className="flex flex-col gap-1">
                {list.map((b) => (
                  <li key={b.id} className="flex items-center gap-2 text-sm">
                    <Link className={linkClass} href={v.href({ name: "dashboard-detail", id: b.id })} onNavigate={v.goPath}>
                      {b.name}
                    </Link>
                    <Chip tone="sky">{moduleLabel(b.sourceModule)}</Chip>
                  </li>
                ))}
              </ul>
            )
          }
        </Async>
      </Section>
    </div>
  );
}

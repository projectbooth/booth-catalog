import { useEffect, useState } from "react";
import { listDatasets, listTags } from "../api/client";
import { Async, Button, Chip, EmptyState, Link, PageHeader, Pager, inputClass, linkClass } from "../components/ui";
import type { ViewCtx } from "../context";
import { formatDate, formatLocation } from "../format";
import { useDebounced, useLoad } from "../hooks";

const PAGE = 25;

/** The dataset browser: text search, tag and owner filters, and (for editors and owners) the way
 *  in to registering one. */
export function DataList({ v }: { v: ViewCtx }) {
  const [text, setText] = useState("");
  const [owner, setOwner] = useState("");
  const [tag, setTag] = useState("");
  const [offset, setOffset] = useState(0);
  const q = useDebounced(text.trim());
  const ownerQ = useDebounced(owner.trim());

  // A changed filter starts again from the first page.
  useEffect(() => setOffset(0), [q, ownerQ, tag]);

  const tags = useLoad(() => listTags(v.api), [v.api.workspace]);
  const list = useLoad(
    () => listDatasets(v.api, { q, owner: ownerQ, tags: tag ? [tag] : undefined, limit: PAGE, offset }),
    [v.api.workspace, q, ownerQ, tag, offset],
  );
  const filtered = q !== "" || ownerQ !== "" || tag !== "";

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Datasets"
        subtitle="Data registered in the catalog, each pointing at where its bytes live in storage."
        actions={
          v.canWrite && (
            <Button variant="primary" onClick={() => v.go({ name: "data-new" })}>
              Register dataset
            </Button>
          )
        }
      />

      <div className="flex flex-wrap items-end gap-3">
        <label className="flex min-w-[14rem] flex-1 flex-col gap-1 text-xs font-medium text-slate-600 dark:text-slate-300">
          Filter datasets
          <input className={inputClass} type="search" placeholder="Name, description or tag" value={text} onChange={(e) => setText(e.target.value)} />
        </label>
        <label className="flex w-44 flex-col gap-1 text-xs font-medium text-slate-600 dark:text-slate-300">
          Tag
          <select className={inputClass} value={tag} onChange={(e) => setTag(e.target.value)}>
            <option value="">All tags</option>
            {tags.state.status === "ready" &&
              tags.state.data.map((t) => (
                <option key={t.tag} value={t.tag}>
                  {t.tag} ({t.count})
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
              <EmptyState title="No datasets match those filters." />
            ) : (
              <EmptyState title="No datasets registered yet.">
                {v.canWrite ? "Register one to point the catalog at data in storage." : "An editor or owner in this workspace can register datasets."}
              </EmptyState>
            )
          ) : (
            <>
              <div className="overflow-x-auto rounded-lg border border-slate-200 dark:border-slate-700">
                <table className="w-full text-left text-sm">
                  <thead className="bg-slate-50 text-xs uppercase tracking-wide text-slate-500 dark:bg-slate-900 dark:text-slate-400">
                    <tr>
                      <th className="px-3 py-2">Name</th>
                      <th className="px-3 py-2">Location</th>
                      <th className="px-3 py-2">Tags</th>
                      <th className="px-3 py-2">Owner</th>
                      <th className="px-3 py-2">Updated</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-200 dark:divide-slate-800">
                    {page.items.map((d) => (
                      <tr key={d.id}>
                        <td className="px-3 py-2 align-top">
                          <Link className={`font-medium ${linkClass}`} href={v.href({ name: "data-detail", id: d.id })} onNavigate={v.goPath}>
                            {d.name}
                          </Link>
                          {d.description && <p className="mt-0.5 line-clamp-2 max-w-md text-xs text-slate-500 dark:text-slate-400">{d.description}</p>}
                        </td>
                        <td className="px-3 py-2 align-top font-mono text-xs text-slate-600 dark:text-slate-300">{formatLocation(d.location)}</td>
                        <td className="px-3 py-2 align-top">
                          <div className="flex flex-wrap gap-1">
                            {d.tags.map((t) => (
                              <Chip key={t}>{t}</Chip>
                            ))}
                          </div>
                        </td>
                        <td className="px-3 py-2 align-top text-slate-700 dark:text-slate-300">{d.owner}</td>
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

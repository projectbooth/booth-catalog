import { useEffect, useState } from "react";
import { listCode } from "../api/client";
import { Async, Button, Chip, EmptyState, Link, PageHeader, Pager, inputClass, linkClass } from "../components/ui";
import type { ViewCtx } from "../context";
import { formatDate } from "../format";
import { useDebounced, useLoad } from "../hooks";

const PAGE = 25;

/** The code catalog browser: reusable functions and snippets, each with a version history. */
export function CodeList({ v }: { v: ViewCtx }) {
  const [text, setText] = useState("");
  const [owner, setOwner] = useState("");
  const [language, setLanguage] = useState("");
  const [offset, setOffset] = useState(0);
  const q = useDebounced(text.trim());
  const ownerQ = useDebounced(owner.trim());
  const langQ = useDebounced(language.trim().toLowerCase());

  useEffect(() => setOffset(0), [q, ownerQ, langQ]);

  const list = useLoad(
    () => listCode(v.api, { q, owner: ownerQ, language: langQ, limit: PAGE, offset }),
    [v.api.workspace, q, ownerQ, langQ, offset],
  );
  const filtered = q !== "" || ownerQ !== "" || langQ !== "";

  return (
    <div className="flex flex-col gap-4">
      <PageHeader
        title="Code"
        subtitle="Reusable functions and snippets your team shares, each with a version history. A registry only — nothing here runs."
        actions={
          v.canWrite && (
            <Button variant="primary" onClick={() => v.go({ name: "code-new" })}>
              Publish code
            </Button>
          )
        }
      />

      <div className="flex flex-wrap items-end gap-3">
        <label className="flex min-w-[14rem] flex-1 flex-col gap-1 text-xs font-medium text-slate-600 dark:text-slate-300">
          Filter code
          <input className={inputClass} type="search" placeholder="Name or description" value={text} onChange={(e) => setText(e.target.value)} />
        </label>
        <label className="flex w-40 flex-col gap-1 text-xs font-medium text-slate-600 dark:text-slate-300">
          Language
          <input className={inputClass} placeholder="e.g. python" value={language} onChange={(e) => setLanguage(e.target.value)} />
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
              <EmptyState title="No code entries match those filters." />
            ) : (
              <EmptyState title="No code published yet.">
                {v.canWrite ? "Publish a function or snippet to share it with the workspace." : "An editor or owner in this workspace can publish code."}
              </EmptyState>
            )
          ) : (
            <>
              <div className="overflow-x-auto rounded-lg border border-slate-200 dark:border-slate-700">
                <table className="w-full text-left text-sm">
                  <thead className="bg-slate-50 text-xs uppercase tracking-wide text-slate-500 dark:bg-slate-900 dark:text-slate-400">
                    <tr>
                      <th className="px-3 py-2">Name</th>
                      <th className="px-3 py-2">Latest</th>
                      <th className="px-3 py-2">Versions</th>
                      <th className="px-3 py-2">Owner</th>
                      <th className="px-3 py-2">Updated</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-slate-200 dark:divide-slate-800">
                    {page.items.map((e) => (
                      <tr key={e.id}>
                        <td className="px-3 py-2 align-top">
                          <div className="flex items-center gap-2">
                            <Link className={`font-medium ${linkClass}`} href={v.href({ name: "code-detail", id: e.id })} onNavigate={v.goPath}>
                              {e.name}
                            </Link>
                            {e.language && <Chip tone="indigo">{e.language}</Chip>}
                          </div>
                          {e.description && <p className="mt-0.5 line-clamp-2 max-w-md text-xs text-slate-500 dark:text-slate-400">{e.description}</p>}
                        </td>
                        <td className="px-3 py-2 align-top font-mono text-xs">{e.latestVersion?.version ?? "—"}</td>
                        <td className="px-3 py-2 align-top text-slate-700 dark:text-slate-300">{e.versionCount}</td>
                        <td className="px-3 py-2 align-top text-slate-700 dark:text-slate-300">{e.owner}</td>
                        <td className="whitespace-nowrap px-3 py-2 align-top text-slate-500 dark:text-slate-400">{formatDate(e.updatedAt)}</td>
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

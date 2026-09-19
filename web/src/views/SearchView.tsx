import { search } from "../api/client";
import { Async, Chip, EmptyState, Link, PageHeader, linkClass } from "../components/ui";
import type { ViewCtx } from "../context";
import { moduleLabel } from "../format";
import { useLoad } from "../hooks";
import type { AssetType, SearchHit } from "../types";

const TYPE_LABEL: Record<AssetType, string> = { data: "Dataset", code: "Code", dashboard: "Dashboard" };
const TYPE_TONE: Record<AssetType, "indigo" | "emerald" | "sky"> = { data: "indigo", code: "emerald", dashboard: "sky" };

function hitRoute(h: SearchHit) {
  switch (h.type) {
    case "data":
      return { name: "data-detail", id: h.id } as const;
    case "code":
      return { name: "code-detail", id: h.id } as const;
    case "dashboard":
      return { name: "dashboard-detail", id: h.id } as const;
  }
}

/** Results of a search across datasets, code and dashboards together (ADR 0044). Name matches come
 *  first; there's no ranking beyond that. */
export function SearchView({ v, q }: { v: ViewCtx; q: string }) {
  const query = q.trim();
  const res = useLoad(() => (query ? search(v.api, query) : Promise.resolve({ query: "", hits: [] as SearchHit[] })), [v.api.workspace, query]);

  if (!query) return <EmptyState title="Search the catalog">Type in the box above to search names and descriptions across datasets, code and dashboards.</EmptyState>;

  return (
    <div className="flex flex-col gap-4">
      <PageHeader title={`Results for “${query}”`} />
      <Async state={res.state}>
        {(r) =>
          r.hits.length === 0 ? (
            <EmptyState title="Nothing matched.">Search looks for your words in names and descriptions (and dataset tags). Try a shorter or different word.</EmptyState>
          ) : (
            <ul className="flex flex-col divide-y divide-slate-200 rounded-lg border border-slate-200 dark:divide-slate-800 dark:border-slate-700">
              {r.hits.map((h) => (
                <li key={`${h.type}:${h.id}`} className="flex flex-col gap-0.5 px-3 py-2">
                  <div className="flex flex-wrap items-center gap-2">
                    <Chip tone={TYPE_TONE[h.type]}>{TYPE_LABEL[h.type]}</Chip>
                    <Link className={`font-medium ${linkClass}`} href={v.href(hitRoute(h))} onNavigate={v.goPath}>
                      {h.name}
                    </Link>
                    {h.source && <span className="text-xs text-slate-500 dark:text-slate-400">{moduleLabel(h.source)}</span>}
                    {h.owner && <span className="text-xs text-slate-500 dark:text-slate-400">· {h.owner}</span>}
                  </div>
                  {h.description && <p className="line-clamp-2 max-w-3xl text-sm text-slate-600 dark:text-slate-400">{h.description}</p>}
                </li>
              ))}
            </ul>
          )
        }
      </Async>
    </div>
  );
}

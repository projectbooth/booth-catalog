import { useState } from "react";
import { ApiError, codeVersions, deleteCode, getCode, getCodeVersion } from "../api/client";
import { Async, Banner, Button, Chip, Link, Meta, PageHeader, Section, linkClass } from "../components/ui";
import type { ViewCtx } from "../context";
import { formatBytes, formatDate } from "../format";
import { errorMessage, useLoad } from "../hooks";

/** One code entry: its details, its version history, and one version's source at a time — the
 *  latest by default, or the one named in the URL (`/code/{id}/v/{version}`), so a specific version
 *  can be linked to. */
export function CodeDetail({ v, id, version }: { v: ViewCtx; id: string; version?: string }) {
  const entry = useLoad(() => getCode(v.api, id), [v.api.workspace, id]);
  const versions = useLoad(() => codeVersions(v.api, id), [v.api.workspace, id]);
  const shown = useLoad(() => getCodeVersion(v.api, id, version ?? "latest"), [v.api.workspace, id, version]);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);

  async function remove() {
    setBusy(true);
    setError(null);
    try {
      await deleteCode(v.api, id);
      v.go({ name: "code" });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : errorMessage(err));
      setBusy(false);
      setConfirming(false);
    }
  }

  async function copy(source: string) {
    try {
      await navigator.clipboard.writeText(source);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      setError("Couldn't copy to the clipboard — select the source and copy it manually.");
    }
  }

  return (
    <div className="flex flex-col gap-6">
      <Async state={entry.state}>
        {(e) => (
          <>
            <PageHeader
              title={e.name}
              subtitle="Code"
              actions={
                v.canWrite &&
                (confirming ? (
                  <>
                    <span className="text-sm text-slate-600 dark:text-slate-300">
                      Delete this entry and all {e.versionCount} version{e.versionCount === 1 ? "" : "s"}?
                    </span>
                    <Button variant="danger" disabled={busy} onClick={remove}>
                      {busy ? "Deleting…" : "Confirm delete"}
                    </Button>
                    <Button disabled={busy} onClick={() => setConfirming(false)}>
                      Cancel
                    </Button>
                  </>
                ) : (
                  <>
                    <Button variant="primary" onClick={() => v.go({ name: "code-publish", id })}>
                      Publish new version
                    </Button>
                    <Button onClick={() => v.go({ name: "code-edit", id })}>Edit details</Button>
                    <Button variant="danger" onClick={() => setConfirming(true)}>
                      Delete
                    </Button>
                  </>
                ))
              }
            />
            {error && <Banner tone="error">{error}</Banner>}
            {e.description && <p className="max-w-3xl whitespace-pre-wrap text-sm text-slate-700 dark:text-slate-300">{e.description}</p>}
            <dl className="grid grid-cols-2 gap-4 sm:grid-cols-4">
              <Meta label="Owner">{e.owner || "—"}</Meta>
              <Meta label="Language">{e.language || "—"}</Meta>
              <Meta label="Versions">{e.versionCount}</Meta>
              <Meta label="Updated">{formatDate(e.updatedAt)}</Meta>
            </dl>
          </>
        )}
      </Async>

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-[16rem_1fr]">
        <Section title="Versions">
          <Async state={versions.state}>
            {(list) => (
              <ol className="flex flex-col gap-1" aria-label="Versions">
                {list.map((ver, i) => {
                  // "latest" is the default view, so it is current when no version is named.
                  const current = version ? ver.version === version : i === 0;
                  return (
                    <li key={ver.version}>
                      <Link
                        className={`block rounded-md border px-2 py-1.5 text-sm ${
                          current
                            ? "border-indigo-500 bg-indigo-50 dark:border-indigo-400 dark:bg-indigo-950"
                            : "border-slate-200 hover:bg-slate-50 dark:border-slate-700 dark:hover:bg-slate-900"
                        }`}
                        aria-current={current ? "true" : undefined}
                        href={v.href({ name: "code-detail", id, version: ver.version })}
                        onNavigate={v.goPath}
                      >
                        <span className="flex items-center gap-2">
                          <span className="font-mono">{ver.version}</span>
                          {i === 0 && <Chip tone="emerald">latest</Chip>}
                        </span>
                        <span className="block text-xs text-slate-500 dark:text-slate-400">
                          {formatDate(ver.publishedAt)} · {ver.publishedBy}
                        </span>
                      </Link>
                    </li>
                  );
                })}
              </ol>
            )}
          </Async>
        </Section>

        <Section title="Source">
          <Async state={shown.state}>
            {(s) => (
              <div className="flex min-w-0 flex-col gap-2">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <p className="text-sm text-slate-600 dark:text-slate-300">
                    <span className="font-mono font-medium">{s.version}</span> · {formatBytes(s.sizeBytes)} · published {formatDate(s.publishedAt)} by {s.publishedBy}
                  </p>
                  <Button onClick={() => copy(s.source)}>{copied ? "Copied" : "Copy source"}</Button>
                </div>
                {s.notes && <p className="whitespace-pre-wrap text-sm text-slate-600 dark:text-slate-300">{s.notes}</p>}
                {/* The source is user-supplied text: rendered as text in a <pre>, never as markup. */}
                <pre
                  aria-label={`Source of version ${s.version}`}
                  tabIndex={0}
                  className="max-h-[32rem] overflow-auto rounded-lg border border-slate-200 bg-slate-50 p-3 font-mono text-xs leading-relaxed text-slate-900 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-100"
                >
                  {s.source}
                </pre>
              </div>
            )}
          </Async>
        </Section>
      </div>
      <p className="text-xs text-slate-500 dark:text-slate-400">
        Published versions are immutable — publishing a new one never changes an old one.{" "}
        <Link className={linkClass} href={v.href({ name: "code" })} onNavigate={v.goPath}>
          Back to all code
        </Link>
      </p>
    </div>
  );
}

import { useState, type FormEvent } from "react";
import { ApiError, createCode, fetchConfig, getCode, publishVersion, updateCode } from "../api/client";
import { Async, Banner, Button, Field, PageHeader, inputClass } from "../components/ui";
import type { ViewCtx } from "../context";
import { formatBytes } from "../format";
import { errorMessage, useLoad } from "../hooks";
import type { CodeEntry } from "../types";

const sourceClass = `${inputClass} font-mono`;

/** Shared submit plumbing: busy flag, and API errors mapped to per-field messages or a banner. */
function useSubmit(ownFields: string[]) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | string | null>(null);
  const fieldError = (name: string) => (error instanceof ApiError && error.field === name ? error.message : undefined);
  const banner =
    error === null ? null : typeof error === "string" ? error : error.field && ownFields.includes(error.field) ? null : error.message;
  return { busy, setBusy, setError, fieldError, banner };
}

/** Publish a new code entry together with its first version. */
export function CodeCreateForm({ v }: { v: ViewCtx }) {
  const cfg = useLoad(() => fetchConfig(v.api), [v.api.workspace]);
  const [f, setF] = useState({ name: "", description: "", owner: "", language: "", version: "1.0.0", source: "", notes: "" });
  const s = useSubmit(["name", "description", "owner", "language", "version", "source", "notes"]);
  const set = (k: keyof typeof f, val: string) => setF((cur) => ({ ...cur, [k]: val }));
  // If config can't be loaded the server still enforces its limit; the hint just isn't shown.
  const limit = cfg.state.status === "ready" ? cfg.state.data.maxCodeSourceBytes : null;

  async function submit(ev: FormEvent) {
    ev.preventDefault();
    s.setBusy(true);
    s.setError(null);
    try {
      const e = await createCode(v.api, { ...f, name: f.name.trim(), version: f.version.trim() });
      v.go({ name: "code-detail", id: e.id });
    } catch (err) {
      s.setError(err instanceof ApiError ? err : errorMessage(err));
      s.setBusy(false);
    }
  }

  return (
    <form className="flex max-w-3xl flex-col gap-5" onSubmit={submit} noValidate>
      <PageHeader title="Publish code" subtitle="Create a code entry and publish its first version. Later versions are added from the entry's page." />
      {s.banner && <Banner tone="error">{s.banner}</Banner>}

      <Field id="code-name" label="Name" required error={s.fieldError("name")} help="Unique within the workspace.">
        {(p) => <input {...p} className={inputClass} value={f.name} onChange={(e) => set("name", e.target.value)} />}
      </Field>
      <Field id="code-desc" label="Description" error={s.fieldError("description")}>
        {(p) => <textarea {...p} className={inputClass} rows={2} value={f.description} onChange={(e) => set("description", e.target.value)} />}
      </Field>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <Field id="code-lang" label="Language" error={s.fieldError("language")} help="Optional label, e.g. python or sql.">
          {(p) => <input {...p} className={inputClass} value={f.language} onChange={(e) => set("language", e.target.value)} />}
        </Field>
        <Field id="code-owner" label="Owner" error={s.fieldError("owner")} help="A person or team. Leave empty to make it you.">
          {(p) => <input {...p} className={inputClass} value={f.owner} onChange={(e) => set("owner", e.target.value)} />}
        </Field>
      </div>

      <VersionFields
        version={f.version}
        source={f.source}
        notes={f.notes}
        limit={limit}
        errors={{ version: s.fieldError("version"), source: s.fieldError("source"), notes: s.fieldError("notes") }}
        onChange={(k, val) => set(k, val)}
      />

      <div className="flex gap-2">
        <Button type="submit" variant="primary" disabled={s.busy}>
          {s.busy ? "Publishing…" : "Publish"}
        </Button>
        <Button disabled={s.busy} onClick={() => v.go({ name: "code" })}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

function VersionFields({
  version,
  source,
  notes,
  limit,
  errors,
  onChange,
}: {
  version: string;
  source: string;
  notes: string;
  limit: number | null;
  errors: { version?: string; source?: string; notes?: string };
  onChange: (k: "version" | "source" | "notes", val: string) => void;
}) {
  const bytes = new TextEncoder().encode(source).length;
  const over = limit !== null && bytes > limit;
  return (
    <>
      <Field
        id="ver-label"
        label="Version"
        required
        error={errors.version}
        help="Any label — 1.0.0, 2026-09-19, v3-hotfix. Once published, a version can never be changed."
      >
        {(p) => <input {...p} className={inputClass} value={version} onChange={(e) => onChange("version", e.target.value)} />}
      </Field>
      <Field
        id="ver-source"
        label="Source"
        required
        error={errors.source ?? (over ? `This is ${formatBytes(bytes)}; a version is limited to ${formatBytes(limit!)}.` : undefined)}
        help={limit !== null ? `${formatBytes(bytes)} of ${formatBytes(limit)}` : undefined}
      >
        {(p) => <textarea {...p} className={sourceClass} rows={14} spellCheck={false} value={source} onChange={(e) => onChange("source", e.target.value)} />}
      </Field>
      <Field id="ver-notes" label="Release notes" error={errors.notes} help="Optional: what changed in this version.">
        {(p) => <textarea {...p} className={inputClass} rows={2} value={notes} onChange={(e) => onChange("notes", e.target.value)} />}
      </Field>
    </>
  );
}

/** Publish a new version of an existing entry. */
export function CodePublishForm({ v, id }: { v: ViewCtx; id: string }) {
  const entry = useLoad(() => getCode(v.api, id), [v.api.workspace, id]);
  const cfg = useLoad(() => fetchConfig(v.api), [v.api.workspace]);
  const [f, setF] = useState({ version: "", source: "", notes: "" });
  const s = useSubmit(["version", "source", "notes"]);
  const limit = cfg.state.status === "ready" ? cfg.state.data.maxCodeSourceBytes : null;

  async function submit(ev: FormEvent) {
    ev.preventDefault();
    s.setBusy(true);
    s.setError(null);
    try {
      const published = await publishVersion(v.api, id, { ...f, version: f.version.trim() });
      v.go({ name: "code-detail", id, version: published.version });
    } catch (err) {
      s.setError(err instanceof ApiError ? err : errorMessage(err));
      s.setBusy(false);
    }
  }

  return (
    <Async state={entry.state}>
      {(e) => (
        <form className="flex max-w-3xl flex-col gap-5" onSubmit={submit} noValidate>
          <PageHeader
            title={`Publish a new version of ${e.name}`}
            subtitle={e.latestVersion ? `The latest published version is ${e.latestVersion.version}. Published versions are immutable.` : undefined}
          />
          {s.banner && <Banner tone="error">{s.banner}</Banner>}
          <VersionFields
            version={f.version}
            source={f.source}
            notes={f.notes}
            limit={limit}
            errors={{ version: s.fieldError("version"), source: s.fieldError("source"), notes: s.fieldError("notes") }}
            onChange={(k, val) => setF((cur) => ({ ...cur, [k]: val }))}
          />
          <div className="flex gap-2">
            <Button type="submit" variant="primary" disabled={s.busy}>
              {s.busy ? "Publishing…" : "Publish version"}
            </Button>
            <Button disabled={s.busy} onClick={() => v.go({ name: "code-detail", id })}>
              Cancel
            </Button>
          </div>
        </form>
      )}
    </Async>
  );
}

/** Edit an entry's metadata. Versions are not editable — publish a new one instead. */
export function CodeEditForm({ v, id }: { v: ViewCtx; id: string }) {
  const entry = useLoad(() => getCode(v.api, id), [v.api.workspace, id]);
  return <Async state={entry.state}>{(e) => <MetaEditor v={v} entry={e} />}</Async>;
}

function MetaEditor({ v, entry }: { v: ViewCtx; entry: CodeEntry }) {
  const [f, setF] = useState({ name: entry.name, description: entry.description, owner: entry.owner, language: entry.language });
  const s = useSubmit(["name", "description", "owner", "language"]);
  const set = (k: keyof typeof f, val: string) => setF((cur) => ({ ...cur, [k]: val }));

  async function submit(ev: FormEvent) {
    ev.preventDefault();
    s.setBusy(true);
    s.setError(null);
    try {
      await updateCode(v.api, entry.id, { ...f, name: f.name.trim() });
      v.go({ name: "code-detail", id: entry.id });
    } catch (err) {
      s.setError(err instanceof ApiError ? err : errorMessage(err));
      s.setBusy(false);
    }
  }

  return (
    <form className="flex max-w-3xl flex-col gap-5" onSubmit={submit} noValidate>
      <PageHeader title={`Edit ${entry.name}`} subtitle="Changes the entry's details. Published versions are never modified." />
      {s.banner && <Banner tone="error">{s.banner}</Banner>}
      <Field id="edit-name" label="Name" required error={s.fieldError("name")}>
        {(p) => <input {...p} className={inputClass} value={f.name} onChange={(e) => set("name", e.target.value)} />}
      </Field>
      <Field id="edit-desc" label="Description" error={s.fieldError("description")}>
        {(p) => <textarea {...p} className={inputClass} rows={3} value={f.description} onChange={(e) => set("description", e.target.value)} />}
      </Field>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
        <Field id="edit-lang" label="Language" error={s.fieldError("language")}>
          {(p) => <input {...p} className={inputClass} value={f.language} onChange={(e) => set("language", e.target.value)} />}
        </Field>
        <Field id="edit-owner" label="Owner" error={s.fieldError("owner")} help="Leave empty to keep the current owner.">
          {(p) => <input {...p} className={inputClass} value={f.owner} onChange={(e) => set("owner", e.target.value)} />}
        </Field>
      </div>
      <div className="flex gap-2">
        <Button type="submit" variant="primary" disabled={s.busy}>
          {s.busy ? "Saving…" : "Save changes"}
        </Button>
        <Button disabled={s.busy} onClick={() => v.go({ name: "code-detail", id: entry.id })}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

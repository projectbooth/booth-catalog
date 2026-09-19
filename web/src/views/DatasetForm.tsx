import { useState, type FormEvent } from "react";
import { ApiError, createDataset, getDataset, updateDataset } from "../api/client";
import { LocationPicker } from "../components/LocationPicker";
import { Async, Banner, Button, Field, PageHeader, inputClass } from "../components/ui";
import type { ViewCtx } from "../context";
import { errorMessage, useLoad } from "../hooks";
import type { Column, Dataset, DatasetInput } from "../types";

const EMPTY: DatasetInput = { name: "", description: "", location: { backendId: "", path: "" }, schema: [], tags: [], owner: "" };

const fromDataset = (d: Dataset): DatasetInput => ({
  name: d.name,
  description: d.description,
  location: d.location,
  schema: d.schema.map((c) => ({ ...c })),
  tags: d.tags,
  owner: d.owner,
});

/** Register a dataset, or edit one when `id` is given. */
export function DatasetForm({ v, id }: { v: ViewCtx; id?: string }) {
  // Registering has nothing to load, so it renders at once; only editing waits for the dataset.
  if (!id) return <Editor v={v} initial={EMPTY} />;
  return <EditLoader v={v} id={id} />;
}

function EditLoader({ v, id }: { v: ViewCtx; id: string }) {
  const existing = useLoad(() => getDataset(v.api, id), [v.api.workspace, id]);
  return <Async state={existing.state}>{(d) => <Editor v={v} id={id} initial={fromDataset(d)} />}</Async>;
}

function Editor({ v, id, initial }: { v: ViewCtx; id?: string; initial: DatasetInput }) {
  const [form, setForm] = useState<DatasetInput>(initial);
  // The tags box is text ("finance, pii") so typing a comma doesn't fight a controlled array.
  const [tagText, setTagText] = useState(initial.tags.join(", "));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<ApiError | string | null>(null);

  const set = <K extends keyof DatasetInput>(k: K, val: DatasetInput[K]) => setForm((f) => ({ ...f, [k]: val }));
  const fieldError = (name: string) => (error instanceof ApiError && error.field === name ? error.message : undefined);
  // An error about a field with no control of its own here (or none named) is shown as a banner.
  const ownField = ["name", "description", "owner", "tags", "location.backendId", "location.path"];
  const bannerError =
    error === null ? null : typeof error === "string" ? error : error.field && ownField.includes(error.field) ? null : error.message;

  async function submit(ev: FormEvent) {
    ev.preventDefault();
    setBusy(true);
    setError(null);
    const body: DatasetInput = {
      ...form,
      name: form.name.trim(),
      location: { backendId: form.location.backendId.trim(), path: form.location.path.trim() },
      tags: tagText.split(",").map((t) => t.trim()).filter(Boolean),
      schema: form.schema.filter((c) => c.name.trim() !== "" || c.type.trim() !== ""),
    };
    try {
      const saved = id ? await updateDataset(v.api, id, body) : await createDataset(v.api, body);
      v.go({ name: "data-detail", id: saved.id });
    } catch (err) {
      setError(err instanceof ApiError ? err : errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <form className="flex max-w-3xl flex-col gap-5" onSubmit={submit} noValidate>
      <PageHeader title={id ? "Edit dataset" : "Register dataset"} subtitle="Describe a dataset and say where in storage it lives." />
      {bannerError && <Banner tone="error">{bannerError}</Banner>}

      <Field id="ds-name" label="Name" required error={fieldError("name")} help="Unique within the workspace.">
        {(p) => <input {...p} className={inputClass} value={form.name} onChange={(e) => set("name", e.target.value)} />}
      </Field>

      <Field id="ds-desc" label="Description" error={fieldError("description")}>
        {(p) => <textarea {...p} className={inputClass} rows={3} value={form.description} onChange={(e) => set("description", e.target.value)} />}
      </Field>

      <LocationPicker
        api={v.api}
        value={form.location}
        onChange={(l) => set("location", l)}
        errors={{ backendId: fieldError("location.backendId"), path: fieldError("location.path") }}
      />

      <SchemaEditor columns={form.schema} onChange={(c) => set("schema", c)} />

      <Field id="ds-tags" label="Tags" error={fieldError("tags")} help="Comma-separated. Lowercase letters, digits and . _ : - only; they are lowercased for you.">
        {(p) => <input {...p} className={inputClass} value={tagText} placeholder="finance, pii" onChange={(e) => setTagText(e.target.value)} />}
      </Field>

      <Field
        id="ds-owner"
        label="Owner"
        error={fieldError("owner")}
        help={id ? "Leave empty to keep the current owner." : "A person or team. Leave empty to make it you."}
      >
        {(p) => <input {...p} className={inputClass} value={form.owner} onChange={(e) => set("owner", e.target.value)} />}
      </Field>

      <div className="flex gap-2">
        <Button type="submit" variant="primary" disabled={busy}>
          {busy ? "Saving…" : id ? "Save changes" : "Register dataset"}
        </Button>
        <Button disabled={busy} onClick={() => (id ? v.go({ name: "data-detail", id }) : v.go({ name: "data" }))}>
          Cancel
        </Button>
      </div>
    </form>
  );
}

/** Rows of column name / type / description. Blank rows are dropped on save. */
function SchemaEditor({ columns, onChange }: { columns: Column[]; onChange: (c: Column[]) => void }) {
  const update = (i: number, patch: Partial<Column>) => onChange(columns.map((c, j) => (j === i ? { ...c, ...patch } : c)));
  return (
    <fieldset className="flex flex-col gap-2">
      <legend className="mb-1 text-xs font-medium text-slate-600 dark:text-slate-300">Schema</legend>
      {columns.length === 0 && <p className="text-sm text-slate-500 dark:text-slate-400">No columns described. Optional — add them if you want them browsable.</p>}
      {columns.map((c, i) => (
        <div key={i} className="grid grid-cols-[1fr_1fr_2fr_auto] items-center gap-2">
          <input aria-label={`Column ${i + 1} name`} className={inputClass} placeholder="name" value={c.name} onChange={(e) => update(i, { name: e.target.value })} />
          <input aria-label={`Column ${i + 1} type`} className={inputClass} placeholder="type, e.g. bigint" value={c.type} onChange={(e) => update(i, { type: e.target.value })} />
          <input
            aria-label={`Column ${i + 1} description`}
            className={inputClass}
            placeholder="description (optional)"
            value={c.description ?? ""}
            onChange={(e) => update(i, { description: e.target.value })}
          />
          <Button aria-label={`Remove column ${i + 1}`} onClick={() => onChange(columns.filter((_, j) => j !== i))}>
            Remove
          </Button>
        </div>
      ))}
      <div>
        <Button onClick={() => onChange([...columns, { name: "", type: "", description: "" }])}>Add column</Button>
      </div>
    </fieldset>
  );
}

import { useState, type FormEvent } from "react";
import { ApiError, createDataset, getDataset, updateDataset } from "../api/client";
import { LocationPicker } from "../components/LocationPicker";
import { Async, Banner, Button, Field, PageHeader, inputClass } from "../components/ui";
import type { ViewCtx } from "../context";
import { errorMessage, useLoad } from "../hooks";
import type { Column, Dataset, DatasetInput } from "../types";

const EMPTY_LOCATION = { backendId: "", path: "" };
const EMPTY: DatasetInput = { name: "", description: "", location: EMPTY_LOCATION, schema: [], tags: [], owner: "", format: "file" };

// A format: "iceberg" row never reaches this form (DataDetail hides Edit for one, ADR 0085);
// defaulting an unexpected value to "file" here is just defensive, not a real code path.
const fromDataset = (d: Dataset): DatasetInput => ({
  name: d.name,
  description: d.description,
  location: d.location,
  schema: d.schema.map((c) => ({ ...c })),
  tags: d.tags,
  owner: d.owner,
  format: d.format === "postgres" ? "postgres" : "file",
  postgresTable: d.postgresTable ? { ...d.postgresTable } : undefined,
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
  // An error about a field with no control of its own here (or none named) is shown as a
  // banner. The bare "location"/"postgresTable" fields (a location set together with
  // format: "postgres", or vice versa) have no dedicated control either: submit() below
  // never constructs a body that could trigger them, so they're left to the banner too — a
  // defense-in-depth server check surfacing here would mean something unexpected happened.
  const ownField = ["name", "description", "owner", "tags", "format", "location.backendId", "location.path", "postgresTable.schema", "postgresTable.name"];
  const bannerError =
    error === null ? null : typeof error === "string" ? error : error.field && ownField.includes(error.field) ? null : error.message;

  const isPostgres = form.format === "postgres";
  const postgresTable = form.postgresTable ?? { schema: "", name: "" };
  const setPostgresTable = (patch: Partial<typeof postgresTable>) => set("postgresTable", { ...postgresTable, ...patch });

  // Switching format clears the other format's fields — ADR 0102's write API refuses a mix
  // of the two (a location on a postgres row, a postgresTable on a file row), and clearing
  // here means the switch always produces a request the server actually accepts.
  function setFormat(next: "file" | "postgres") {
    setForm((f) => ({
      ...f,
      format: next,
      location: next === "postgres" ? EMPTY_LOCATION : f.location,
      postgresTable: next === "postgres" ? (f.postgresTable ?? { schema: "", name: "" }) : undefined,
    }));
  }

  async function submit(ev: FormEvent) {
    ev.preventDefault();
    setBusy(true);
    setError(null);
    const body: DatasetInput = {
      ...form,
      name: form.name.trim(),
      location: isPostgres ? EMPTY_LOCATION : { backendId: form.location.backendId.trim(), path: form.location.path.trim() },
      postgresTable: isPostgres ? { schema: postgresTable.schema.trim(), name: postgresTable.name.trim() } : undefined,
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

      <Field id="ds-format" label="Format" error={fieldError("format")} help="Where this dataset's data actually lives.">
        {(p) => (
          <select {...p} className={inputClass} value={form.format ?? "file"} onChange={(e) => setFormat(e.target.value as "file" | "postgres")}>
            <option value="file">A file or folder in storage</option>
            <option value="postgres">A table in this workspace's database</option>
          </select>
        )}
      </Field>

      {isPostgres ? (
        <fieldset className="flex flex-col gap-3">
          <legend className="mb-1 text-xs font-medium text-slate-600 dark:text-slate-300">
            Postgres table<span className="ml-0.5 text-red-500" aria-hidden="true">*</span>
          </legend>
          <p className="-mt-2 text-xs text-slate-500 dark:text-slate-400">
            The catalog doesn't verify this table exists or read its data — it's a pointer, checked by whoever queries it.
          </p>
          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Field id="ds-pg-schema" label="Schema" required error={fieldError("postgresTable.schema")}>
              {(p) => <input {...p} className={inputClass} placeholder="public" value={postgresTable.schema} onChange={(e) => setPostgresTable({ schema: e.target.value })} />}
            </Field>
            <Field id="ds-pg-table" label="Table name" required error={fieldError("postgresTable.name")}>
              {(p) => <input {...p} className={inputClass} placeholder="orders" value={postgresTable.name} onChange={(e) => setPostgresTable({ name: e.target.value })} />}
            </Field>
          </div>
        </fieldset>
      ) : (
        <LocationPicker
          api={v.api}
          value={form.location}
          onChange={(l) => set("location", l)}
          errors={{ backendId: fieldError("location.backendId"), path: fieldError("location.path") }}
        />
      )}

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

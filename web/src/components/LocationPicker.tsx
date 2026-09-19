import { useEffect, useState } from "react";
import { checkLocation, fetchBackends, type ApiContext, type LocationCheck as Check } from "../api/client";
import { errorMessage } from "../hooks";
import type { Location, StorageBackend } from "../types";
import { Banner, Button, Field, inputClass } from "./ui";

/** Shows what a live check of a location found. Shared by the picker and the detail view. */
export function CheckResult({ result }: { result: Check }) {
  switch (result.state) {
    case "found":
      return <Banner tone="success">Found in storage — {result.kind === "folder" ? "a folder with contents" : "an object"}.</Banner>;
    case "missing-path":
      return <Banner tone="warn">The backend exists, but nothing was found at that path. The data may not have been written yet, or it has moved.</Banner>;
    case "missing-backend":
      return <Banner tone="warn">There is no storage backend with that ID in this workspace.</Banner>;
    case "unavailable":
      return <Banner tone="info">Couldn't check against storage: {result.message}</Banner>;
  }
}

/** A "Check location" button and its result. The check runs in the browser with the user's own
 *  token, through booth-storage's API — the catalog's backend never resolves a location itself
 *  (ADR 0045; docs/decisions/0002). It's an action the user asks for, not a guarantee. */
export function LocationCheck({ api, location }: { api: ApiContext; location: Location }) {
  const [state, setState] = useState<{ status: "idle" } | { status: "checking" } | { status: "done"; result: Check }>({ status: "idle" });

  // A different location invalidates any previous answer.
  useEffect(() => setState({ status: "idle" }), [location.backendId, location.path]);

  async function run() {
    setState({ status: "checking" });
    // Same canonicalization the server applies on save: no surrounding space, no trailing "/".
    const loc = { backendId: location.backendId.trim(), path: location.path.trim().replace(/\/+$/, "") };
    setState({ status: "done", result: await checkLocation(api, loc) });
  }

  return (
    <div className="flex flex-col gap-2">
      <div>
        <Button onClick={run} disabled={state.status === "checking" || location.backendId === ""}>
          {state.status === "checking" ? "Checking…" : "Check location"}
        </Button>
      </div>
      {state.status === "done" && <CheckResult result={state.result} />}
    </div>
  );
}

/**
 * The form control for a dataset's storage location: a backend chosen from what booth-storage
 * reports, plus a path. If booth-storage isn't installed (or refuses), it degrades to typing the
 * backend ID — the catalog works without storage; storage only makes picking easier.
 */
export function LocationPicker({
  api,
  value,
  onChange,
  errors,
}: {
  api: ApiContext;
  value: Location;
  onChange: (l: Location) => void;
  errors: { backendId?: string; path?: string };
}) {
  const [backends, setBackends] = useState<{ status: "loading" } | { status: "ready"; list: StorageBackend[] } | { status: "unavailable"; reason: string }>({ status: "loading" });

  useEffect(() => {
    let live = true;
    fetchBackends(api).then(
      (list) => live && setBackends({ status: "ready", list }),
      (err: unknown) => live && setBackends({ status: "unavailable", reason: errorMessage(err) }),
    );
    return () => {
      live = false;
    };
    // The workspace decides which backends exist; the token accessor is read fresh per call, so
    // it deliberately isn't a dependency.
  }, [api.workspace]);

  const known = backends.status === "ready" ? backends.list : [];
  const stale = backends.status === "ready" && value.backendId !== "" && !known.some((b) => b.id === value.backendId);

  return (
    <fieldset className="flex flex-col gap-3">
      <legend className="mb-1 text-xs font-medium text-slate-600 dark:text-slate-300">
        Storage location<span className="ml-0.5 text-red-500" aria-hidden="true">*</span>
      </legend>

      <Field id="loc-backend" label="Backend" required error={errors.backendId}>
        {(p) =>
          backends.status === "loading" ? (
            // Reserve the select's place while the list loads, rather than showing a text box that
            // then swaps to a select under the user's hands.
            <select {...p} className={inputClass} disabled value="">
              <option value="">Loading backends…</option>
            </select>
          ) : backends.status === "ready" ? (
            <select {...p} className={inputClass} value={value.backendId} onChange={(e) => onChange({ ...value, backendId: e.target.value })}>
              <option value="">Choose a backend…</option>
              {known.map((b) => (
                <option key={b.id} value={b.id}>
                  {b.displayName} ({b.id})
                </option>
              ))}
              {stale && <option value={value.backendId}>{value.backendId} (not registered in storage)</option>}
            </select>
          ) : (
            <input {...p} className={inputClass} value={value.backendId} placeholder="e.g. lake" onChange={(e) => onChange({ ...value, backendId: e.target.value })} />
          )
        }
      </Field>
      {backends.status === "unavailable" && (
        <p className="-mt-2 text-xs text-slate-500 dark:text-slate-400">
          Storage backends couldn't be listed ({backends.reason}), so type the backend ID instead.
        </p>
      )}

      <Field
        id="loc-path"
        label="Path"
        error={errors.path}
        help="Relative to the backend root, e.g. warehouse/orders. A folder or a single object. Leave empty for the whole backend."
      >
        {(p) => <input {...p} className={inputClass} value={value.path} placeholder="warehouse/orders" onChange={(e) => onChange({ ...value, path: e.target.value })} />}
      </Field>

      <LocationCheck api={api} location={value} />
    </fieldset>
  );
}

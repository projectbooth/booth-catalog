import { useEffect, useRef, useState } from "react";
import { searchUsers, type ApiContext } from "../api/client";
import { useDebounced } from "../hooks";
import type { UserSummary } from "../types";
import { Field, inputClass } from "./ui";

/**
 * The `owner` field: a combobox that type-ahead searches booth-core's user directory
 * (`GET /api/users?q=`, ADR 0047/0052) as the caller's own token, scoped to the active workspace.
 * Picking a suggestion stores the resolved `sub` — otherwise `value` stays whatever was typed, so
 * free text keeps working exactly as it always did (docs/decisions/0003): a brand-new user who's
 * never signed in here has no directory entry, and booth-core may not even be reachable, so a
 * failed or empty search just means no suggestions, never a blocked field. This mirrors how
 * LocationPicker degrades when booth-storage is unavailable.
 */
export function OwnerPicker({
  api,
  id,
  value,
  onChange,
  label = "Owner",
  help,
  error,
}: {
  api: ApiContext;
  id: string;
  value: string;
  onChange: (v: string) => void;
  label?: string;
  help?: string;
  error?: string;
}) {
  const [open, setOpen] = useState(false);
  const [matches, setMatches] = useState<UserSummary[]>([]);
  const [highlight, setHighlight] = useState(0);
  const debouncedQuery = useDebounced(value.trim());
  // Ignores a response for a query that's no longer current, e.g. a fast edit after a slow reply.
  const requestId = useRef(0);

  useEffect(() => {
    // Not focused (e.g. an edit form just mounted with an existing owner already filled in), or
    // nothing typed yet: nothing to suggest, and no reason to call booth-core at all.
    if (!open || debouncedQuery === "") {
      setMatches([]);
      return;
    }
    const mine = ++requestId.current;
    searchUsers(api, debouncedQuery).then(
      (users) => {
        if (mine === requestId.current) setMatches(users);
      },
      () => {
        if (mine === requestId.current) setMatches([]);
      },
    );
    // api.workspace scopes the directory search (ADR 0052); the token accessor is read fresh per
    // call and deliberately not a dependency here, matching LocationPicker's backend fetch.
  }, [open, debouncedQuery, api.workspace]);

  useEffect(() => setHighlight(0), [matches]);

  function pick(u: UserSummary) {
    onChange(u.sub);
    setMatches([]);
    setOpen(false);
  }

  const listboxId = `${id}-listbox`;
  const showList = open && matches.length > 0;

  return (
    <Field id={id} label={label} help={help} error={error}>
      {(p) => (
        <div className="relative">
          <input
            {...p}
            className={inputClass}
            role="combobox"
            aria-expanded={showList}
            aria-controls={listboxId}
            aria-autocomplete="list"
            autoComplete="off"
            value={value}
            onChange={(e) => {
              onChange(e.target.value);
              setOpen(true);
            }}
            onFocus={() => setOpen(true)}
            onBlur={() => setOpen(false)}
            onKeyDown={(e) => {
              if (!showList) return;
              if (e.key === "ArrowDown") {
                e.preventDefault();
                setHighlight((h) => Math.min(h + 1, matches.length - 1));
              } else if (e.key === "ArrowUp") {
                e.preventDefault();
                setHighlight((h) => Math.max(h - 1, 0));
              } else if (e.key === "Enter" && matches[highlight]) {
                e.preventDefault();
                pick(matches[highlight]);
              } else if (e.key === "Escape") {
                setOpen(false);
              }
            }}
          />
          {showList && (
            <ul
              id={listboxId}
              role="listbox"
              aria-label={`${label} suggestions`}
              className="absolute z-10 mt-1 max-h-48 w-full overflow-auto rounded-md border border-slate-300 bg-white py-1 text-sm shadow-lg dark:border-slate-600 dark:bg-slate-800"
            >
              {matches.map((u, i) => (
                <li
                  key={u.sub}
                  role="option"
                  aria-selected={i === highlight}
                  // Fires before the input's blur would close the list, so the click still lands.
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => pick(u)}
                  className={`cursor-pointer px-2 py-1 text-slate-900 dark:text-slate-100 ${i === highlight ? "bg-indigo-50 dark:bg-indigo-950" : ""}`}
                >
                  {u.displayName}
                  {u.email && u.email !== u.displayName && <span className="text-slate-500 dark:text-slate-400"> · {u.email}</span>}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </Field>
  );
}

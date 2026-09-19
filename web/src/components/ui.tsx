import type { AnchorHTMLAttributes, ButtonHTMLAttributes, MouseEvent, ReactNode } from "react";
import type { LoadState } from "../hooks";

// Small presentational primitives, styled with the same Tailwind slate/indigo palette
// booth-module-store and booth-storage use, so the native modules sit together consistently in the
// shell. Every colour has a `dark:` counterpart — booth-design toggles dark mode via a data-theme
// attribute, which tailwind.config.js's darkMode selector follows.

type Variant = "primary" | "secondary" | "danger";

const VARIANTS: Record<Variant, string> = {
  primary: "bg-indigo-600 text-white hover:bg-indigo-500 disabled:opacity-50",
  secondary:
    "border border-slate-300 text-slate-700 hover:bg-slate-100 disabled:opacity-50 dark:border-slate-600 dark:text-slate-300 dark:hover:bg-slate-800",
  danger:
    "border border-red-300 text-red-700 hover:bg-red-50 disabled:opacity-50 dark:border-red-800 dark:text-red-400 dark:hover:bg-red-950",
};

export function Button({
  variant = "secondary",
  className = "",
  type = "button",
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & { variant?: Variant }) {
  return (
    <button
      type={type}
      className={`rounded-md px-3 py-1.5 text-sm font-medium focus:outline-none focus:ring-2 focus:ring-indigo-500 ${VARIANTS[variant]} ${className}`}
      {...rest}
    />
  );
}

export const inputClass =
  "w-full rounded-md border border-slate-300 bg-white px-2 py-1.5 text-sm text-slate-900 focus:border-indigo-500 focus:outline-none focus:ring-1 focus:ring-indigo-500 disabled:opacity-60 dark:border-slate-600 dark:bg-slate-900 dark:text-slate-100";

/** A labelled form control with optional help and error text, wired for accessibility: the label
 *  targets the control, and help/error are linked via aria-describedby. */
export function Field({
  id,
  label,
  help,
  error,
  required,
  children,
}: {
  id: string;
  label: string;
  help?: string;
  error?: string;
  required?: boolean;
  children: (props: { id: string; "aria-describedby"?: string; "aria-invalid"?: boolean }) => ReactNode;
}) {
  const describedBy = [help ? `${id}-help` : "", error ? `${id}-error` : ""].filter(Boolean).join(" ") || undefined;
  return (
    <div className="flex flex-col gap-1">
      <label htmlFor={id} className="text-xs font-medium text-slate-600 dark:text-slate-300">
        {label}
        {required && (
          <span className="ml-0.5 text-red-500" aria-hidden="true">
            *
          </span>
        )}
      </label>
      {children({ id, "aria-describedby": describedBy, "aria-invalid": error ? true : undefined })}
      {help && (
        <p id={`${id}-help`} className="text-xs text-slate-500 dark:text-slate-400">
          {help}
        </p>
      )}
      {error && (
        <p id={`${id}-error`} role="alert" className="text-xs text-red-600 dark:text-red-400">
          {error}
        </p>
      )}
    </div>
  );
}

export function Banner({ tone, children }: { tone: "error" | "info" | "success" | "warn"; children: ReactNode }) {
  const tones = {
    error: "border-red-200 bg-red-50 text-red-800 dark:border-red-900 dark:bg-red-950 dark:text-red-200",
    info: "border-slate-200 bg-slate-50 text-slate-700 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300",
    success: "border-emerald-200 bg-emerald-50 text-emerald-800 dark:border-emerald-900 dark:bg-emerald-950 dark:text-emerald-200",
    warn: "border-amber-200 bg-amber-50 text-amber-900 dark:border-amber-900 dark:bg-amber-950 dark:text-amber-200",
  };
  return (
    <div role={tone === "error" ? "alert" : "status"} className={`rounded-md border px-3 py-2 text-sm ${tones[tone]}`}>
      {children}
    </div>
  );
}

export function PageHeader({ title, subtitle, actions }: { title: string; subtitle?: string; actions?: ReactNode }) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-3">
      <div className="min-w-0">
        <h2 className="break-words text-lg font-semibold text-slate-900 dark:text-slate-100">{title}</h2>
        {subtitle && <p className="mt-0.5 text-sm text-slate-500 dark:text-slate-400">{subtitle}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  );
}

export function Chip({ children, tone = "slate" }: { children: ReactNode; tone?: "slate" | "indigo" | "amber" | "emerald" | "sky" }) {
  const tones = {
    slate: "bg-slate-200 text-slate-700 dark:bg-slate-800 dark:text-slate-300",
    indigo: "bg-indigo-100 text-indigo-800 dark:bg-indigo-950 dark:text-indigo-300",
    amber: "bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300",
    emerald: "bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300",
    sky: "bg-sky-100 text-sky-800 dark:bg-sky-950 dark:text-sky-300",
  };
  return <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium ${tones[tone]}`}>{children}</span>;
}

/** A link that navigates in-app without a page reload (see navigation.ts's defaultNavigate),
 *  while still being a real anchor: middle-click, ctrl/cmd-click and "open in new tab" work. */
export function Link({
  href,
  onNavigate,
  onClick,
  ...rest
}: AnchorHTMLAttributes<HTMLAnchorElement> & { href: string; onNavigate: (path: string) => void }) {
  return (
    <a
      href={href}
      onClick={(ev: MouseEvent<HTMLAnchorElement>) => {
        onClick?.(ev);
        if (ev.defaultPrevented || ev.button !== 0 || ev.metaKey || ev.ctrlKey || ev.shiftKey || ev.altKey) return;
        ev.preventDefault();
        onNavigate(href);
      }}
      {...rest}
    />
  );
}

export const linkClass = "text-indigo-600 hover:underline dark:text-indigo-400";

/** Renders the loading / error / ready states of a useLoad result. */
export function Async<T>({ state, children }: { state: LoadState<T>; children: (data: T) => ReactNode }) {
  if (state.status === "loading")
    return (
      <p role="status" className="py-6 text-sm text-slate-500 dark:text-slate-400">
        Loading…
      </p>
    );
  if (state.status === "error") return <Banner tone="error">{state.error}</Banner>;
  return <>{children(state.data)}</>;
}

export function EmptyState({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="rounded-lg border border-dashed border-slate-300 px-6 py-10 text-center dark:border-slate-700">
      <p className="text-sm font-medium text-slate-700 dark:text-slate-300">{title}</p>
      {children && <div className="mx-auto mt-1 max-w-md text-sm text-slate-500 dark:text-slate-400">{children}</div>}
    </div>
  );
}

export function Pager({ total, offset, limit, onChange }: { total: number; offset: number; limit: number; onChange: (offset: number) => void }) {
  if (total <= limit) return null;
  const from = offset + 1;
  const to = Math.min(offset + limit, total);
  return (
    <div className="flex items-center justify-between text-sm text-slate-600 dark:text-slate-400">
      <span>
        {from}–{to} of {total}
      </span>
      <div className="flex gap-2">
        <Button disabled={offset === 0} onClick={() => onChange(Math.max(0, offset - limit))}>
          Previous
        </Button>
        <Button disabled={offset + limit >= total} onClick={() => onChange(offset + limit)}>
          Next
        </Button>
      </div>
    </div>
  );
}

/** A definition-list row for a detail view. */
export function Meta({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div>
      <dt className="text-xs font-medium uppercase tracking-wide text-slate-500 dark:text-slate-400">{label}</dt>
      <dd className="mt-0.5 break-words text-sm text-slate-900 dark:text-slate-100">{children}</dd>
    </div>
  );
}

export function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-2">
      <h3 className="text-sm font-semibold text-slate-800 dark:text-slate-200">{title}</h3>
      {children}
    </section>
  );
}

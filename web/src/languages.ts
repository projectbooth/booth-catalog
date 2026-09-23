/**
 * The languages `booth-pipeline`'s base runner knows how to execute — its per-language dispatch
 * table (ADR 0064), `booth_pipeline/runners/languages.py`. Mirrored here as a small constant
 * rather than a runtime dependency on booth-pipeline (this repo doesn't depend on it, and isn't
 * meant to); update this list by hand if that registry grows. If a third module ever needs the
 * same list, that's a coordinator-level shared contract, not something resolved here unilaterally
 * (agent-briefs/catalog.md).
 *
 * This only feeds a dropdown convenience on the code catalog's `language` field — free text stays
 * valid input, since an entry can describe non-runnable code the way it always could.
 */
export const KNOWN_LANGUAGES = ["python", "sql"] as const;

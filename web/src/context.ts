import type { ApiContext } from "./api/client";
import type { Route } from "./navigation";

/** What every view needs from CatalogApp: how to call the API, whether this caller may write, and
 *  how to move around. Bundled so views take one prop and stay easy to render in tests. */
export interface ViewCtx {
  api: ApiContext;
  /** Whether write controls are offered. A UX nicety only — booth-catalog's backend enforces the
   *  same rule on every write route (ADR 0023, ADR 0041), so a hidden button is not the boundary. */
  canWrite: boolean;
  /** The href for a route, for real anchors. */
  href: (route: Route) => string;
  /** Navigate in-app to a route. */
  go: (route: Route) => void;
  /** Navigate in-app to a raw shell path (a dashboard's own page, under another module's navPath). */
  goPath: (path: string) => void;
}

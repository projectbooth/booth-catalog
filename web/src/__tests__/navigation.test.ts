import { describe, expect, it } from "vitest";
import { isShellPath, parseRoute, routePath, sectionOf, type Route } from "../navigation";

describe("routes", () => {
  const routes: Route[] = [
    { name: "data" },
    { name: "data-new" },
    { name: "data-detail", id: "ds-1" },
    { name: "data-edit", id: "ds-1" },
    { name: "code" },
    { name: "code-new" },
    { name: "code-detail", id: "c-1" },
    { name: "code-detail", id: "c-1", version: "1.0.0" },
    { name: "code-edit", id: "c-1" },
    { name: "code-publish", id: "c-1" },
    { name: "dashboards" },
    { name: "dashboard-detail", id: "b-1" },
    { name: "search", q: "order events" },
  ];

  it.each(routes)("round-trips %j", (route) => {
    const path = routePath(route);
    const [pathname, search = ""] = path.split("?");
    expect(parseRoute(pathname, search ? `?${search}` : "")).toEqual(route);
  });

  it("honours a different base path", () => {
    expect(routePath({ name: "data" }, "/x")).toBe("/x/data");
    expect(parseRoute("/x/dashboards/b1", "", "/x")).toEqual({ name: "dashboard-detail", id: "b1" });
  });

  it("round-trips IDs and versions that need URL encoding", () => {
    const route: Route = { name: "code-detail", id: "a/b c", version: "1.0.0+build.5" };
    expect(parseRoute(routePath(route), "")).toEqual(route);
  });

  it("lands on the dataset list for anything unrecognised, rather than an error", () => {
    for (const p of ["/catalog", "/catalog/", "/catalog/nonsense", "/catalog/data/a/b/c", "/catalog/code/a/nope", "/elsewhere/data", "/"]) {
      expect(parseRoute(p, "")).toEqual({ name: "data" });
    }
  });

  it("does not mistake 'new' or a reserved word for an ID", () => {
    expect(parseRoute("/catalog/data/new", "")).toEqual({ name: "data-new" });
    expect(parseRoute("/catalog/code/new", "")).toEqual({ name: "code-new" });
  });

  it("reads the search query, and an empty one", () => {
    expect(parseRoute("/catalog/search", "?q=hello+world")).toEqual({ name: "search", q: "hello world" });
    expect(parseRoute("/catalog/search", "")).toEqual({ name: "search", q: "" });
  });

  it("maps routes to sections", () => {
    expect(sectionOf({ name: "data-edit", id: "x" })).toBe("data");
    expect(sectionOf({ name: "code-publish", id: "x" })).toBe("code");
    expect(sectionOf({ name: "dashboard-detail", id: "x" })).toBe("dashboards");
    expect(sectionOf({ name: "search", q: "" })).toBeNull();
  });
});

describe("isShellPath", () => {
  it("accepts only paths inside the shell", () => {
    for (const ok of ["/superset/dashboard/1", "/a?b=1", "/"]) expect(isShellPath(ok)).toBe(true);
    for (const bad of ["https://evil.example", "//evil.example/x", "javascript:alert(1)", "superset/x", "", "/a\\b"]) expect(isShellPath(bad)).toBe(false);
  });
});

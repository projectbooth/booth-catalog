import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, checkLocation, createDataset, deleteDataset, listDatasets, searchUsers } from "../api/client";
import { CAT, CORE, STO, mockFetch } from "./testUtils";

afterEach(() => vi.unstubAllGlobals());

const ctx = (token: string | null = "tok") => ({ workspace: "acme", getAccessToken: () => token });

describe("request plumbing", () => {
  it("goes through the gateway path and sends the workspace and bearer token", async () => {
    const m = mockFetch({ [`GET ${CAT}/datasets`]: { json: { items: [], total: 0 } } });
    await listDatasets(ctx("abc"), {});
    expect(m.calls[0].path).toBe("/modules/catalog/api/datasets");
    expect(m.calls[0].headers.get("X-Workspace")).toBe("acme");
    expect(m.calls[0].headers.get("Authorization")).toBe("Bearer abc");
  });

  it("reads the token fresh on every request (ADR 0033), never caching it", async () => {
    const m = mockFetch({ [`GET ${CAT}/datasets`]: { json: { items: [], total: 0 } } });
    let token = "first";
    const c = { workspace: "acme", getAccessToken: () => token };
    await listDatasets(c);
    token = "renewed";
    await listDatasets(c);
    expect(m.calls.map((x) => x.headers.get("Authorization"))).toEqual(["Bearer first", "Bearer renewed"]);
  });

  it("omits Authorization when there is no token, rather than sending 'null'", async () => {
    const m = mockFetch({ [`GET ${CAT}/datasets`]: { json: { items: [], total: 0 } } });
    await listDatasets(ctx(null), {});
    expect(m.calls[0].headers.has("Authorization")).toBe(false);
  });

  it("encodes filters, repeating tag and dropping empty values", async () => {
    const m = mockFetch({ [`GET ${CAT}/datasets`]: { json: { items: [], total: 0 } } });
    await listDatasets(ctx(), { q: "a b", tags: ["pii", "finance"], owner: "", limit: 25, offset: 50 });
    const q = m.calls[0].query;
    expect(q.get("q")).toBe("a b");
    expect(q.getAll("tag")).toEqual(["pii", "finance"]);
    expect(q.has("owner")).toBe(false);
    expect(q.get("limit")).toBe("25");
    expect(q.get("offset")).toBe("50");
  });

  it("surfaces the server's error message and field", async () => {
    mockFetch({ [`POST ${CAT}/datasets`]: { status: 422, json: { error: "is required", field: "name" } } });
    const err = await createDataset(ctx(), { name: "", description: "", location: { backendId: "l", path: "" }, schema: [], tags: [], owner: "" }).catch((e) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 422, message: "is required", field: "name" });
  });

  it("falls back to the raw text for a non-JSON error (a gateway page)", async () => {
    mockFetch({ [`GET ${CAT}/datasets`]: { status: 502, text: "Bad Gateway" } });
    await expect(listDatasets(ctx())).rejects.toMatchObject({ status: 502, message: "Bad Gateway" });
  });

  it("treats 204 as success with no body", async () => {
    mockFetch({ [`DELETE ${CAT}/datasets/ds-1`]: { status: 204 } });
    await expect(deleteDataset(ctx(), "ds-1")).resolves.toBeUndefined();
  });

  it("encodes IDs into the path", async () => {
    const m = mockFetch({});
    await deleteDataset(ctx(), "a/b c").catch(() => undefined);
    expect(m.calls[0].path).toBe("/modules/catalog/api/datasets/a%2Fb%20c");
  });
});

describe("searchUsers (booth-core's own API, not a module — no /modules prefix)", () => {
  it("hits /api/users directly with q and a small limit", async () => {
    const m = mockFetch({ [`GET ${CORE}/users`]: { json: [{ sub: "s1", displayName: "Alice", lastSeenAt: "2026-09-01T00:00:00Z" }] } });
    const users = await searchUsers(ctx(), "ali");
    expect(m.calls[0].path).toBe("/api/users");
    expect(m.calls[0].query.get("q")).toBe("ali");
    expect(m.calls[0].query.get("limit")).toBe("8");
    expect(m.calls.some((c) => c.path.startsWith("/modules/"))).toBe(false);
    expect(users).toEqual([{ sub: "s1", displayName: "Alice", lastSeenAt: "2026-09-01T00:00:00Z" }]);
  });
});

describe("checkLocation (resolved through booth-storage, never the catalog backend)", () => {
  const loc = (path: string) => ({ backendId: "lake", path });
  const backend = { [`GET ${STO}/backends/lake`]: { json: { id: "lake" } } };

  it("finds a folder that has contents", async () => {
    const m = mockFetch({ ...backend, [`GET ${STO}/backends/lake/objects`]: { json: { entries: [{ path: "warehouse/orders/p.parquet", size: 1 }] } } });
    expect(await checkLocation(ctx(), loc("warehouse/orders"))).toEqual({ state: "found", kind: "folder" });
    expect(m.calls.every((c) => c.path.startsWith("/modules/storage/"))).toBe(true);
    expect(m.calls.some((c) => c.path.startsWith("/modules/catalog/"))).toBe(false);
  });

  it("finds a single object among its siblings", async () => {
    mockFetch({
      ...backend,
      [`GET ${STO}/backends/lake/objects`]: (c) =>
        c.query.get("prefix") === "warehouse/x.parquet" ? { json: { entries: [] } } : { json: { entries: [{ path: "warehouse/x.parquet", size: 9 }, { path: "warehouse/y.parquet", size: 1 }] } },
    });
    expect(await checkLocation(ctx(), loc("warehouse/x.parquet"))).toEqual({ state: "found", kind: "file" });
  });

  it("reports a path that isn't there, and pages through siblings before deciding", async () => {
    let listed = 0;
    mockFetch({
      ...backend,
      [`GET ${STO}/backends/lake/objects`]: (c) => {
        if (c.query.get("prefix") === "a/gone") return { json: { entries: [] } };
        listed++;
        return { json: { entries: [{ path: "a/other", size: 1 }], nextCursor: listed < 2 ? "more" : undefined } };
      },
    });
    expect(await checkLocation(ctx(), loc("a/gone"))).toEqual({ state: "missing-path" });
    expect(listed).toBe(2);
  });

  it("treats the backend root as found once the backend exists", async () => {
    mockFetch(backend);
    expect(await checkLocation(ctx(), loc(""))).toEqual({ state: "found", kind: "folder" });
  });

  it("distinguishes a missing backend from storage being unavailable", async () => {
    mockFetch({ [`GET ${STO}/backends/lake`]: { status: 404, json: { error: "backend not found" } } });
    expect(await checkLocation(ctx(), loc("x"))).toEqual({ state: "missing-backend" });

    // The gateway's own 404 (module not installed) is not "the backend doesn't exist".
    mockFetch({ [`GET ${STO}/backends/lake`]: { status: 404, text: 'module "storage" not found in registry' } });
    expect(await checkLocation(ctx(), loc("x"))).toMatchObject({ state: "unavailable" });

    mockFetch({ [`GET ${STO}/backends/lake`]: { status: 403, json: { error: "no role" } } });
    expect(await checkLocation(ctx(), loc("x"))).toEqual({ state: "unavailable", message: "no role" });
  });

  it("degrades to 'unavailable' if storage fails mid-check", async () => {
    mockFetch({ ...backend, [`GET ${STO}/backends/lake/objects`]: { status: 502, json: { error: "s3 timeout" } } });
    expect(await checkLocation(ctx(), loc("x"))).toEqual({ state: "unavailable", message: "s3 timeout" });
  });
});

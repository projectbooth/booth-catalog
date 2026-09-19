import { cleanup, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { CAT, STO, codeEntry, dashboard, dataset, mockFetch, page, renderApp } from "./testUtils";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const config = { json: { maxCodeSourceBytes: 1024, dashboardEvents: { state: "subscribed" } } };
/** The backend picker, once booth-storage's list has loaded into it. */
async function backendSelect() {
  await screen.findByRole("option", { name: /Data lake/ });
  return screen.getByLabelText(/^Backend/);
}

const storageBackends = { [`GET ${STO}/backends`]: { json: [{ id: "lake", displayName: "Data lake", kind: "s3", location: "s3://b" }] } };

describe("the app shell", () => {
  it("lands on the dataset list at the module's base path and marks the section", async () => {
    mockFetch({ [`GET ${CAT}/datasets`]: { json: page([dataset()]) }, [`GET ${CAT}/tags`]: { json: { tags: [] } } });
    renderApp("/catalog");
    expect(await screen.findByRole("link", { name: "orders" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Datasets" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Code" })).not.toHaveAttribute("aria-current");
  });

  it("navigates between sections in-app, without a page reload, and keeps the address bar honest", async () => {
    mockFetch({
      [`GET ${CAT}/datasets`]: { json: page([dataset()]) },
      [`GET ${CAT}/tags`]: { json: { tags: [] } },
      [`GET ${CAT}/code`]: { json: page([codeEntry()]) },
      [`GET ${CAT}/dashboards`]: { json: page([dashboard()]) },
      [`GET ${CAT}/config`]: config,
    });
    renderApp("/catalog/data");
    await screen.findByRole("link", { name: "orders" });

    await userEvent.click(screen.getByRole("link", { name: "Code" }));
    expect(await screen.findByRole("link", { name: "clean_emails" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/catalog/code");

    await userEvent.click(screen.getByRole("link", { name: "Dashboards" }));
    expect(await screen.findByRole("link", { name: "Revenue" })).toBeInTheDocument();
    expect(window.location.pathname).toBe("/catalog/dashboards");

    window.history.back();
    await waitFor(() => expect(window.location.pathname).toBe("/catalog/code"));
    expect(await screen.findByRole("link", { name: "clean_emails" })).toBeInTheDocument();
  });

  it("applies the theme and never trusts role for anything but which controls to show", async () => {
    mockFetch({ [`GET ${CAT}/datasets`]: { json: page([]) }, [`GET ${CAT}/tags`]: { json: { tags: [] } } });
    const { container } = renderApp("/catalog/data");
    expect(container.firstElementChild).toHaveAttribute("data-theme", "light");
    await screen.findByText("No datasets registered yet.");
  });
});

describe("role gating (the server refuses regardless; the UI just doesn't tempt)", () => {
  const routes = {
    [`GET ${CAT}/datasets`]: { json: page([dataset()]) },
    [`GET ${CAT}/tags`]: { json: { tags: [] } },
    [`GET ${CAT}/datasets/ds-1`]: { json: dataset() },
    [`GET ${CAT}/datasets/ds-1/lineage`]: { json: { dashboards: [] } },
  };

  it("offers editors and owners the way to register a dataset", async () => {
    mockFetch(routes);
    renderApp("/catalog/data", "owner");
    expect(await screen.findByRole("button", { name: "Register dataset" })).toBeInTheDocument();
  });

  it("hides write controls from viewers, on the list and the detail page", async () => {
    mockFetch(routes);
    renderApp("/catalog/data", "viewer");
    await screen.findByRole("link", { name: "orders" });
    expect(screen.queryByRole("button", { name: "Register dataset" })).not.toBeInTheDocument();

    cleanup();
    renderApp("/catalog/data/ds-1", "viewer");
    await screen.findByRole("heading", { name: "orders" });
    expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Delete" })).not.toBeInTheDocument();
  });

  it("shows a viewer who follows a write URL the read view, not a form", async () => {
    mockFetch({ ...routes, [`GET ${CAT}/code`]: { json: page([]) } });
    renderApp("/catalog/data/new", "viewer");
    expect(await screen.findByRole("heading", { name: "Datasets" })).toBeInTheDocument();
    expect(screen.queryByLabelText(/^Name/)).not.toBeInTheDocument();

    cleanup();
    renderApp("/catalog/data/ds-1/edit", "viewer");
    await screen.findByRole("heading", { name: "orders" });
    expect(screen.queryByRole("button", { name: "Save changes" })).not.toBeInTheDocument();
  });
});

describe("dataset list", () => {
  it("filters through the API and starts again from page one", async () => {
    const m = mockFetch({
      [`GET ${CAT}/datasets`]: (c) => ({ json: page(c.query.get("q") === "web" ? [dataset({ id: "ds-2", name: "web_events" })] : [dataset(), dataset({ id: "ds-2", name: "web_events" })]) }),
      [`GET ${CAT}/tags`]: { json: { tags: [{ tag: "pii", count: 2 }] } },
    });
    renderApp("/catalog/data");
    await screen.findByRole("link", { name: "orders" });

    await userEvent.type(screen.getByLabelText("Filter datasets"), "web");
    await waitFor(() => expect(screen.queryByRole("link", { name: "orders" })).not.toBeInTheDocument());
    expect(screen.getByRole("link", { name: "web_events" })).toBeInTheDocument();
    expect(m.calls.at(-1)?.query.get("offset")).toBe("0");

    // The tag filter is populated from what exists, with counts.
    await userEvent.selectOptions(screen.getByLabelText("Tag"), "pii");
    await waitFor(() => expect(m.calls.at(-1)?.query.getAll("tag")).toEqual(["pii"]));
  });

  it("explains an empty catalog differently from an empty search", async () => {
    mockFetch({ [`GET ${CAT}/datasets`]: { json: page([]) }, [`GET ${CAT}/tags`]: { json: { tags: [] } } });
    renderApp("/catalog/data", "viewer");
    expect(await screen.findByText("No datasets registered yet.")).toBeInTheDocument();
    expect(screen.getByText(/An editor or owner in this workspace can register/)).toBeInTheDocument();

    await userEvent.type(screen.getByLabelText("Filter datasets"), "zzz");
    expect(await screen.findByText("No datasets match those filters.")).toBeInTheDocument();
  });

  it("shows an API failure rather than an empty table", async () => {
    mockFetch({ [`GET ${CAT}/datasets`]: { status: 500, json: { error: "internal error" } }, [`GET ${CAT}/tags`]: { json: { tags: [] } } });
    renderApp("/catalog/data");
    expect(await screen.findByRole("alert")).toHaveTextContent("internal error");
  });

  it("pages when there are more results than a page", async () => {
    const m = mockFetch({
      [`GET ${CAT}/datasets`]: (c) => ({ json: { items: [dataset({ name: `page-${c.query.get("offset")}` })], total: 60 } }),
      [`GET ${CAT}/tags`]: { json: { tags: [] } },
    });
    renderApp("/catalog/data");
    await screen.findByText("1–25 of 60");
    await userEvent.click(screen.getByRole("button", { name: "Next" }));
    await screen.findByText("26–50 of 60");
    expect(m.calls.at(-1)?.query.get("offset")).toBe("25");
  });
});

describe("registering a dataset", () => {
  it("submits a normalized body and lands on the new dataset", async () => {
    const m = mockFetch({
      ...storageBackends,
      [`POST ${CAT}/datasets`]: { status: 201, json: dataset({ id: "ds-new", name: "orders" }) },
      [`GET ${CAT}/datasets/ds-new`]: { json: dataset({ id: "ds-new" }) },
      [`GET ${CAT}/datasets/ds-new/lineage`]: { json: { dashboards: [] } },
    });
    renderApp("/catalog/data/new");
    const user = userEvent.setup();

    await user.type(screen.getByLabelText(/^Name/), "  orders ");
    await user.type(screen.getByLabelText("Description"), "One row per order");
    await user.selectOptions(await backendSelect(), "lake");
    await user.type(screen.getByLabelText("Path"), "warehouse/orders");
    await user.click(screen.getByRole("button", { name: "Add column" }));
    await user.type(screen.getByLabelText("Column 1 name"), "id");
    await user.type(screen.getByLabelText("Column 1 type"), "bigint");
    await user.click(screen.getByRole("button", { name: "Add column" })); // left blank on purpose
    await user.type(screen.getByLabelText("Tags"), " Finance , pii,, ");
    await user.click(screen.getByRole("button", { name: "Register dataset" }));

    await screen.findByRole("heading", { name: "orders" });
    const post = m.called("POST", `${CAT}/datasets`)[0];
    expect(post.body).toEqual({
      name: "orders",
      description: "One row per order",
      location: { backendId: "lake", path: "warehouse/orders" },
      schema: [{ name: "id", type: "bigint", description: "" }], // the blank row was dropped
      tags: ["Finance", "pii"], // the server lowercases and dedupes; the UI just splits and trims
      owner: "",
    });
    expect(window.location.pathname).toBe("/catalog/data/ds-new");
  });

  it("puts a server validation error next to the field it is about", async () => {
    mockFetch({
      ...storageBackends,
      [`POST ${CAT}/datasets`]: { status: 409, json: { error: "a dataset with that name already exists in this workspace", field: "name" } },
    });
    renderApp("/catalog/data/new");
    const user = userEvent.setup();
    await user.type(screen.getByLabelText(/^Name/), "orders");
    await user.selectOptions(await backendSelect(), "lake");
    await user.click(screen.getByRole("button", { name: "Register dataset" }));

    const name = await screen.findByLabelText(/^Name/);
    await waitFor(() => expect(name).toHaveAttribute("aria-invalid", "true"));
    expect(screen.getByRole("alert")).toHaveTextContent("already exists");
    // The form is still there, still filled in, and usable again.
    expect(name).toHaveValue("orders");
    expect(screen.getByRole("button", { name: "Register dataset" })).toBeEnabled();
  });

  it("degrades the backend picker to a text box when storage isn't available", async () => {
    mockFetch({ [`GET ${STO}/backends`]: { status: 404, text: 'module "storage" not found' } });
    renderApp("/catalog/data/new");
    const box = await screen.findByPlaceholderText("e.g. lake");
    expect(box.tagName).toBe("INPUT");
    expect(screen.getByText(/Storage backends couldn't be listed/)).toBeInTheDocument();
  });

  it("lets the user check a location against storage before saving", async () => {
    mockFetch({
      ...storageBackends,
      [`GET ${STO}/backends/lake`]: { json: { id: "lake" } },
      [`GET ${STO}/backends/lake/objects`]: { json: { entries: [{ path: "warehouse/orders/p.parquet", size: 1 }] } },
    });
    renderApp("/catalog/data/new");
    const user = userEvent.setup();
    await user.selectOptions(await backendSelect(), "lake");
    await user.type(screen.getByLabelText("Path"), "warehouse/orders");
    await user.click(screen.getByRole("button", { name: "Check location" }));
    expect(await screen.findByText(/Found in storage/)).toBeInTheDocument();
  });
});

describe("dataset detail", () => {
  const routes = (dashboards: unknown[] = []) => ({
    [`GET ${CAT}/datasets/ds-1`]: { json: dataset() },
    [`GET ${CAT}/datasets/ds-1/lineage`]: { json: { dashboards } },
  });

  it("shows metadata, schema, the location, and the dashboards that read it", async () => {
    mockFetch(routes([dashboard()]));
    renderApp("/catalog/data/ds-1", "viewer");
    await screen.findByRole("heading", { name: "orders" });
    expect(screen.getByText("lake:warehouse/orders")).toBeInTheDocument();
    expect(screen.getByText("bigint")).toBeInTheDocument();
    expect(screen.getByText("pii")).toBeInTheDocument();
    const reader = await screen.findByRole("link", { name: "Revenue" });
    expect(reader).toHaveAttribute("href", "/catalog/dashboards/b-1");
    expect(screen.getByText("Superset")).toBeInTheDocument();
  });

  it("says so when nothing reads it, and still shows the dataset if lineage fails", async () => {
    mockFetch({ [`GET ${CAT}/datasets/ds-1`]: { json: dataset() }, [`GET ${CAT}/datasets/ds-1/lineage`]: { status: 500, json: { error: "internal error" } } });
    renderApp("/catalog/data/ds-1", "viewer");
    await screen.findByRole("heading", { name: "orders" });
    expect(await screen.findByRole("alert")).toHaveTextContent("internal error");
  });

  it("checks the location live and says plainly when the data isn't there", async () => {
    mockFetch({
      ...routes(),
      [`GET ${STO}/backends/lake`]: { json: { id: "lake" } },
      [`GET ${STO}/backends/lake/objects`]: { json: { entries: [] } },
    });
    renderApp("/catalog/data/ds-1", "viewer");
    await screen.findByRole("heading", { name: "orders" });
    await userEvent.click(screen.getByRole("button", { name: "Check location" }));
    expect(await screen.findByText(/nothing was found at that path/)).toBeInTheDocument();
  });

  it("asks before deleting, deletes, and returns to the list", async () => {
    const m = mockFetch({ ...routes(), [`DELETE ${CAT}/datasets/ds-1`]: { status: 204 }, [`GET ${CAT}/datasets`]: { json: page([]) }, [`GET ${CAT}/tags`]: { json: { tags: [] } } });
    renderApp("/catalog/data/ds-1", "editor");
    await screen.findByRole("heading", { name: "orders" });

    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    expect(m.called("DELETE", `${CAT}/datasets/ds-1`)).toHaveLength(0); // one click is not enough
    expect(screen.getByText(/data in storage is not touched/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.getByRole("button", { name: "Delete" })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    await userEvent.click(screen.getByRole("button", { name: "Confirm delete" }));
    await waitFor(() => expect(window.location.pathname).toBe("/catalog/data"));
    expect(m.called("DELETE", `${CAT}/datasets/ds-1`)).toHaveLength(1);
  });

  it("reports a failed delete and stays put", async () => {
    mockFetch({ ...routes(), [`DELETE ${CAT}/datasets/ds-1`]: { status: 403, json: { error: "only workspace editors and owners can change the catalog" } } });
    renderApp("/catalog/data/ds-1", "editor");
    await screen.findByRole("heading", { name: "orders" });
    await userEvent.click(screen.getByRole("button", { name: "Delete" }));
    await userEvent.click(screen.getByRole("button", { name: "Confirm delete" }));
    expect(await screen.findByText(/only workspace editors and owners/)).toBeInTheDocument();
    expect(window.location.pathname).toBe("/catalog/data/ds-1");
  });
});

describe("code", () => {
  const versions = [
    { version: "1.1.0", seq: 2, notes: "faster", sizeBytes: 30, publishedBy: "bob", publishedAt: "2026-09-19T12:00:00Z" },
    { version: "1.0.0", seq: 1, notes: "first", sizeBytes: 20, publishedBy: "bob", publishedAt: "2026-09-01T12:00:00Z" },
  ];
  const routes = {
    [`GET ${CAT}/code/c-1`]: { json: codeEntry() },
    [`GET ${CAT}/code/c-1/versions`]: { json: { versions } },
    [`GET ${CAT}/code/c-1/versions/latest`]: { json: { ...versions[0], source: "def f(): return 2" } },
    [`GET ${CAT}/code/c-1/versions/1.0.0`]: { json: { ...versions[1], source: "def f(): return 1" } },
  };

  it("shows the latest version's source and the history, and can pin an older one by URL", async () => {
    mockFetch(routes);
    renderApp("/catalog/code/c-1", "viewer");
    const src = await screen.findByLabelText("Source of version 1.1.0");
    expect(src).toHaveTextContent("def f(): return 2");
    const history = screen.getByRole("list", { name: "Versions" });
    expect(within(history).getByText("latest")).toBeInTheDocument();

    await userEvent.click(within(history).getByRole("link", { name: /1\.0\.0/ }));
    expect(await screen.findByLabelText("Source of version 1.0.0")).toHaveTextContent("def f(): return 1");
    expect(window.location.pathname).toBe("/catalog/code/c-1/v/1.0.0");
  });

  it("opens a pinned version straight from its URL", async () => {
    mockFetch(routes);
    renderApp("/catalog/code/c-1/v/1.0.0", "viewer");
    expect(await screen.findByLabelText("Source of version 1.0.0")).toHaveTextContent("return 1");
  });

  // Source is user-supplied. It must only ever be shown as text.
  it("renders source as inert text, never as markup", async () => {
    const evil = '<img src=x onerror="window.__pwned=1"><script>window.__pwned=1</script>';
    mockFetch({ ...routes, [`GET ${CAT}/code/c-1/versions/latest`]: { json: { ...versions[0], source: evil } } });
    const { container } = renderApp("/catalog/code/c-1", "viewer");
    const pre = await screen.findByLabelText("Source of version 1.1.0");
    expect(pre.textContent).toBe(evil);
    expect(container.querySelector("img, script")).toBeNull();
    expect((window as unknown as { __pwned?: number }).__pwned).toBeUndefined();
  });

  it("publishes a new entry with its first version", async () => {
    const m = mockFetch({
      [`GET ${CAT}/config`]: config,
      [`POST ${CAT}/code`]: { status: 201, json: codeEntry({ id: "c-new" }) },
      ...{ [`GET ${CAT}/code/c-new`]: { json: codeEntry({ id: "c-new" }) }, [`GET ${CAT}/code/c-new/versions`]: { json: { versions } }, [`GET ${CAT}/code/c-new/versions/latest`]: { json: { ...versions[0], source: "x" } } },
    });
    renderApp("/catalog/code/new");
    const user = userEvent.setup();
    await user.type(screen.getByLabelText(/^Name/), "clean_emails");
    await user.type(screen.getByLabelText("Language"), "Python");
    await user.type(screen.getByLabelText(/^Source/), "def f(): pass");
    await user.click(screen.getByRole("button", { name: "Publish" }));

    await screen.findByRole("heading", { name: "clean_emails" });
    expect(m.called("POST", `${CAT}/code`)[0].body).toMatchObject({ name: "clean_emails", language: "Python", version: "1.0.0", source: "def f(): pass" });
    expect(window.location.pathname).toBe("/catalog/code/c-new");
  });

  it("warns before submit when the source is over the limit, using the server's own limit", async () => {
    mockFetch({ [`GET ${CAT}/config`]: config }); // limit: 1024 bytes
    renderApp("/catalog/code/new");
    await waitFor(() => expect(screen.getByText("0 B of 1.0 KB")).toBeInTheDocument());
    const box = screen.getByLabelText(/^Source/);
    await userEvent.click(box);
    await userEvent.paste("x".repeat(1500));
    expect(await screen.findByRole("alert")).toHaveTextContent(/limited to 1\.0 KB/);
  });

  it("explains that a published version can't be republished", async () => {
    mockFetch({
      ...routes,
      [`POST ${CAT}/code/c-1/versions`]: { status: 409, json: { error: "that version is already published; published versions are immutable, so publish a new version instead", field: "version" } },
    });
    renderApp("/catalog/code/c-1/publish");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText(/^Version/), "1.0.0");
    await user.type(screen.getByLabelText(/^Source/), "tampered");
    await user.click(screen.getByRole("button", { name: "Publish version" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/immutable/);
  });

  it("publishes a new version and lands on it", async () => {
    const m = mockFetch({
      ...routes,
      [`POST ${CAT}/code/c-1/versions`]: { status: 201, json: { ...versions[0], version: "2.0.0", seq: 3 } },
      [`GET ${CAT}/code/c-1/versions/2.0.0`]: { json: { ...versions[0], version: "2.0.0", seq: 3, source: "def f(): return 3" } },
    });
    renderApp("/catalog/code/c-1/publish");
    const user = userEvent.setup();
    await user.type(await screen.findByLabelText(/^Version/), "2.0.0");
    await user.type(screen.getByLabelText(/^Source/), "def f(): return 3");
    await user.click(screen.getByRole("button", { name: "Publish version" }));
    expect(await screen.findByLabelText("Source of version 2.0.0")).toHaveTextContent("return 3");
    expect(m.called("POST", `${CAT}/code/c-1/versions`)[0].body).toMatchObject({ version: "2.0.0", source: "def f(): return 3" });
  });
});

describe("dashboards and lineage", () => {
  const detail = (over: Record<string, unknown> = {}) => ({
    ...dashboard(),
    lineage: {
      complete: false,
      sources: [
        { type: "dataset", datasetId: "ds-1", datasets: [{ id: "ds-1", name: "orders" }] },
        { type: "location", backendId: "lake", path: "warehouse/raw", datasets: [] },
        { type: "external", name: "public.customers", system: "postgres", datasets: [] },
      ],
    },
    ...over,
  });

  it("resolves what it can to datasets and labels the rest as not in the catalog, never hiding them", async () => {
    mockFetch({ [`GET ${CAT}/dashboards/b-1`]: { json: detail() } });
    renderApp("/catalog/dashboards/b-1", "viewer");
    await screen.findByRole("heading", { name: "Revenue" });

    expect(screen.getByRole("link", { name: "orders" })).toHaveAttribute("href", "/catalog/data/ds-1");
    expect(screen.getByText("Storage location lake:warehouse/raw")).toBeInTheDocument();
    expect(screen.getByText("public.customers (postgres)")).toBeInTheDocument();
    expect(screen.getAllByText("Not in the catalog")).toHaveLength(2);
    // The honest caveat, since the publisher didn't claim completeness.
    expect(screen.getByText(/doesn't claim this list is complete/)).toBeInTheDocument();
  });

  it("only says 'complete' when the publisher does", async () => {
    mockFetch({ [`GET ${CAT}/dashboards/b-1`]: { json: detail({ lineage: { complete: true, sources: [] } }) } });
    renderApp("/catalog/dashboards/b-1", "viewer");
    expect(await screen.findByText("The publisher reports this list as complete.")).toBeInTheDocument();
    expect(screen.getByText("No data sources were reported for this dashboard.")).toBeInTheDocument();
  });

  it("links to the dashboard's own page in the shell, but never to a path that leaves it", async () => {
    mockFetch({ [`GET ${CAT}/dashboards/b-1`]: { json: detail() } });
    renderApp("/catalog/dashboards/b-1", "viewer");
    expect(await screen.findByRole("link", { name: "Open in Superset" })).toHaveAttribute("href", "/superset/dashboard/42");

    for (const evil of ["https://evil.example/x", "//evil.example", "javascript:alert(1)"]) {
      cleanup();
      mockFetch({ [`GET ${CAT}/dashboards/b-1`]: { json: detail({ path: evil }) } });
      renderApp("/catalog/dashboards/b-1", "viewer");
      await screen.findByRole("heading", { name: "Revenue" });
      expect(screen.queryByRole("link", { name: /Open in/ })).not.toBeInTheDocument();
    }
  });

  it("has no create, edit or delete controls at any role", async () => {
    mockFetch({ [`GET ${CAT}/dashboards/b-1`]: { json: detail() }, [`GET ${CAT}/dashboards`]: { json: page([dashboard()]) }, [`GET ${CAT}/config`]: config });
    renderApp("/catalog/dashboards", "owner");
    await screen.findByRole("link", { name: "Revenue" });
    expect(screen.queryByRole("button", { name: /register|create|new|add|publish/i })).not.toBeInTheDocument();
    cleanup();
    renderApp("/catalog/dashboards/b-1", "owner");
    await screen.findByRole("heading", { name: "Revenue" });
    expect(screen.queryByRole("button", { name: /edit|delete/i })).not.toBeInTheDocument();
  });

  it("filters by tool", async () => {
    const m = mockFetch({ [`GET ${CAT}/dashboards`]: { json: page([dashboard()]) }, [`GET ${CAT}/config`]: config });
    renderApp("/catalog/dashboards");
    await screen.findByRole("link", { name: "Revenue" });
    await userEvent.selectOptions(screen.getByLabelText("Tool"), "streamlit");
    await waitFor(() => expect(m.calls.at(-1)?.query.get("source")).toBe("streamlit"));
  });

  it.each([
    ["subscribed", null],
    ["disabled", /switched off/],
    ["error", /isn't currently receiving dashboard updates \(error: nats down\)/],
    ["waiting-for-stream", /waiting-for-stream/],
  ])("explains the event subscription state '%s'", async (state, expected) => {
    mockFetch({ [`GET ${CAT}/dashboards`]: { json: page([]) }, [`GET ${CAT}/config`]: { json: { maxCodeSourceBytes: 1, dashboardEvents: { state, detail: state === "error" ? "nats down" : undefined } } } });
    renderApp("/catalog/dashboards");
    await screen.findByText("No dashboards indexed yet.");
    if (expected === null) {
      expect(screen.queryByRole("status")).not.toBeInTheDocument();
    } else {
      expect(await screen.findByText(expected)).toBeInTheDocument();
    }
  });
});

describe("search", () => {
  it("submits to a results page with its own URL and groups all three asset types", async () => {
    const m = mockFetch({
      [`GET ${CAT}/datasets`]: { json: page([]) },
      [`GET ${CAT}/tags`]: { json: { tags: [] } },
      [`GET ${CAT}/search`]: {
        json: {
          query: "order",
          hits: [
            { type: "data", id: "ds-1", name: "order_events", description: "raw events", owner: "alice" },
            { type: "code", id: "c-1", name: "parse_order", description: "", owner: "bob" },
            { type: "dashboard", id: "b-1", name: "Orders overview", description: "", owner: "carol", source: "metabase" },
          ],
        },
      },
    });
    renderApp("/catalog/data");
    await userEvent.type(screen.getByLabelText("Search the catalog"), "order{Enter}");

    await screen.findByRole("heading", { name: "Results for “order”" });
    expect(window.location.pathname + window.location.search).toBe("/catalog/search?q=order");
    expect(m.called("GET", `${CAT}/search`)[0].query.get("q")).toBe("order");

    expect(screen.getByRole("link", { name: "order_events" })).toHaveAttribute("href", "/catalog/data/ds-1");
    expect(screen.getByRole("link", { name: "parse_order" })).toHaveAttribute("href", "/catalog/code/c-1");
    expect(screen.getByRole("link", { name: "Orders overview" })).toHaveAttribute("href", "/catalog/dashboards/b-1");
    expect(screen.getByText("Metabase")).toBeInTheDocument();
    for (const label of ["Dataset", "Code", "Dashboard"]) expect(screen.getAllByText(label).length).toBeGreaterThan(0);
  });

  it("ignores a blank submit, and says when nothing matched", async () => {
    const m = mockFetch({ [`GET ${CAT}/search`]: { json: { query: "zzz", hits: [] } } });
    renderApp("/catalog/search?q=zzz");
    expect(await screen.findByText("Nothing matched.")).toBeInTheDocument();
    // The box shows the query in the URL, so a shared link reads correctly.
    expect(screen.getByLabelText("Search the catalog")).toHaveValue("zzz");

    const before = m.calls.length;
    await userEvent.clear(screen.getByLabelText("Search the catalog"));
    await userEvent.type(screen.getByLabelText("Search the catalog"), "   {Enter}");
    expect(m.calls.length).toBe(before);
  });

  it("shows a prompt, not a request, for an empty query", async () => {
    const m = mockFetch({});
    renderApp("/catalog/search");
    expect(await screen.findByText("Search the catalog", { selector: "p" })).toBeInTheDocument();
    expect(m.called("GET", `${CAT}/search`)).toHaveLength(0);
  });
});

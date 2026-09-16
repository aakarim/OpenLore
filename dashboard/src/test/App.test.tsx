import {
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App, loginURL } from "../App";
import { mockAPI } from "./fixtures";

test("desktop tree opens a production API document and switches views without losing its tab", async () => {
  mockAPI();
  render(<App />);
  const user = userEvent.setup();
  const tree = await screen.findByRole("complementary", {
    name: "Knowledge tree",
  });
  await user.click(await within(tree).findByRole("button", { name: /guide/ }));
  await user.click(
    await within(tree).findByRole("button", { name: /start.md/ }),
  );
  expect(await screen.findByRole("heading", { name: "Start" })).toBeVisible();
  const nativeScroller = document.querySelector(
    ".document-scroll",
  ) as HTMLElement;
  expect(nativeScroller).toBeInTheDocument();
  fireEvent.scroll(nativeScroller, { target: { scrollTop: 120 } });
  expect(screen.getByRole("tab", { name: "start.md" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await user.click(screen.getByRole("button", { name: "Analytics" }));
  expect(
    await screen.findByRole("img", {
      name: /\/guide\/start.md: 4 context tokens/,
    }),
  ).toBeVisible();
  expect(new URLSearchParams(location.search).get("path")).toBe(
    "/guide/start.md",
  );
  await user.click(screen.getByRole("button", { name: /Files 1/ }));
  expect(await screen.findByRole("heading", { name: "Start" })).toBeVisible();
});

test("overview renders full-depth interactive sunburst and attribution series", async () => {
  history.replaceState(
    null,
    "",
    "/dashboard/?view=analytics&path=/&tab=overview",
  );
  mockAPI();
  render(<App />);
  const chart = await screen.findByRole("group", {
    name: "Full-depth context token distribution",
  });
  expect(
    within(chart).getByRole("button", { name: /\/guide\/start.md: 4 tokens/ }),
  ).toBeVisible();
  expect(
    screen.getByRole("img", { name: "Daily activity stacked by attribution" }),
  ).toBeVisible();
  expect(screen.getAllByText("Unknown").length).toBeGreaterThan(0);
});

test("mobile uses category and details sheets instead of horizontal analytics tabs", async () => {
  history.replaceState(
    null,
    "",
    "/dashboard/?view=analytics&path=/&tab=overview",
  );
  mockAPI();
  render(<App />);
  const user = userEvent.setup();
  await screen.findByText("Context by folder");
  await user.click(screen.getByRole("button", { name: /Overview ⌃/ }));
  const dialog = screen.getByRole("dialog", { name: "Analytics sections" });
  expect(within(dialog).getByRole("button", { name: /Usage/ })).toBeVisible();
  expect(within(dialog).queryByRole("tab")).not.toBeInTheDocument();
  fireEvent.keyDown(dialog, { key: "Escape" });
  await waitFor(() => expect(dialog).not.toBeInTheDocument());
});

test("shows honest oversized-context error and hides Access without permission", async () => {
  history.replaceState(
    null,
    "",
    "/dashboard/?view=analytics&path=/&tab=overview",
  );
  mockAPI({ contextError: true });
  render(<App />);
  expect(await screen.findByRole("alert")).toHaveTextContent("too large");
  expect(screen.queryByRole("tab", { name: "Access" })).not.toBeInTheDocument();
});

test("preserves the complete direct route through passkey login", () => {
  history.replaceState(null, "", "/lore/guide/start.md?line=12");
  expect(new URL(loginURL()).searchParams.get("redirect")).toBe(
    "/lore/guide/start.md?line=12",
  );
});

test("file-scoped usage requests both current-hash line rankings", async () => {
  history.replaceState(
    null,
    "",
    "/dashboard/?view=analytics&path=%2Fguide%2Fstart.md&tab=usage",
  );
  const fetch = mockAPI();
  render(<App />);
  expect(
    await screen.findByRole("heading", { name: "Most-used lines" }),
  ).toBeVisible();
  expect(
    screen.getByRole("heading", { name: "Least-used lines" }),
  ).toBeVisible();
  const requests = fetch.mock.calls.map(([input]) => String(input));
  expect(requests.some((url) => url.includes("/most-used-lines?"))).toBe(true);
  expect(requests.some((url) => url.includes("/least-used-lines?"))).toBe(true);
});

test("direct file wins restoration, browser back resolves lore pathname, and file analytics targets exactly that file", async () => {
  history.replaceState(null, "", "/lore/guide/start.md");
  mockAPI();
  render(<App />);
  const user = userEvent.setup();
  expect(await screen.findByRole("heading", { name: "Start" })).toBeVisible();
  expect(screen.getByRole("tab", { name: "start.md" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  await user.click(screen.getByRole("button", { name: "Analytics ↗" }));
  expect(new URLSearchParams(location.search).get("path")).toBe(
    "/guide/start.md",
  );
  await screen.findByRole("heading", { name: "Most-used lines" });
  history.replaceState(null, "", "/lore/guide/start.md");
  fireEvent.popState(window);
  expect(await screen.findByRole("heading", { name: "Start" })).toBeVisible();
  expect(document.querySelector(".breadcrumbs")).not.toBeInTheDocument();
});

test("Access does not depend on usage or full context availability", async () => {
  history.replaceState(
    null,
    "",
    "/dashboard/?view=analytics&path=/&tab=access",
  );
  const fetch = mockAPI({ access: true, contextError: true });
  const original = fetch.getMockImplementation()!;
  fetch.mockImplementation((input, init) =>
    String(input).includes("/api/access?")
      ? Promise.resolve(
          new Response(
            JSON.stringify({
              path: "/",
              docsets: [],
              folder_rules: [],
              notes: ["No editable policy"],
            }),
          ),
        )
      : original(input, init),
  );
  render(<App />);
  expect(await screen.findByRole("heading", { name: "Access" })).toBeVisible();
  expect(screen.queryByRole("combobox")).not.toBeInTheDocument();
  expect(
    fetch.mock.calls.some(([url]) => String(url).includes("/api/usage?")),
  ).toBe(false);
});

test("expired session clears the previously rendered document", async () => {
  history.replaceState(null, "", "/lore/guide/start.md");
  const fetch = mockAPI();
  render(<App />);
  await screen.findByRole("heading", { name: "Start" });
  fetch.mockResolvedValue(
    new Response(JSON.stringify({ error: "Session expired" }), { status: 401 }),
  );
  fireEvent(window, new Event("dashboard-auth-expired"));
  expect(
    await screen.findByRole("heading", { name: "Sign in to OpenLore" }),
  ).toBeVisible();
  expect(
    screen.queryByRole("heading", { name: "Start" }),
  ).not.toBeInTheDocument();
});

test("an aggregation-only refresh recovers an expired session", async () => {
  history.replaceState(null, "", "/dashboard/?view=analytics&path=/&tab=gaps");
  const fetch = mockAPI();
  const original = fetch.getMockImplementation()!;
  render(<App />);
  const user = userEvent.setup();
  await screen.findByRole("heading", { name: "Top search queries" });
  await screen.findAllByRole("cell", { name: "/guide/start.md" });
  fetch.mockClear();
  fetch.mockImplementation((input, init) =>
    /\/analytics\/aggregations\/|\/dashboard\/api\/session$/.test(String(input))
      ? Promise.resolve(new Response("Session expired", { status: 401 }))
      : original(input, init),
  );
  await user.click(screen.getByRole("button", { name: "Refresh analytics" }));
  expect(
    await screen.findByRole("heading", { name: "Sign in to OpenLore" }),
  ).toBeVisible();
  expect(
    screen.queryByRole("complementary", { name: "Knowledge tree" }),
  ).not.toBeInTheDocument();
  expect(
    screen.queryByRole("heading", { name: "Top search queries" }),
  ).not.toBeInTheDocument();
  expect(
    fetch.mock.calls.some(([url]) =>
      String(url).endsWith("/dashboard/api/session"),
    ),
  ).toBe(true);
});

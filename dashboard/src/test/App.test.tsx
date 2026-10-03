import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import { App, loginURL } from "../App";
import { pollDelay } from "../Analytics";
import { mockAPI } from "./fixtures";

test("desktop tree opens a production API document and switches views without losing its tab", async () => {
  mockAPI();
  render(<App />);
  const user = userEvent.setup();
  const tree = await screen.findByRole("complementary", {
    name: "Knowledge tree",
  });
  const root = within(tree).getByRole("button", { name: /Workspace/ });
  expect(root).toHaveAttribute("aria-expanded", "true");
  await user.click(root);
  expect(root).toHaveAttribute("aria-expanded", "false");
  await user.click(root);
  const folder = await within(tree).findByRole("button", { name: /guide/ });
  expect(folder).toHaveAttribute("aria-expanded", "false");
  await user.click(folder);
  expect(folder).toHaveAttribute("aria-expanded", "true");
  expect(
    await within(tree).findByRole("button", { name: /start.md/ }),
  ).not.toHaveAttribute("aria-expanded");
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

test("ready but incomplete knowledge totals show their coverage", async () => {
  history.replaceState(
    null,
    "",
    "/dashboard/?view=analytics&path=/&tab=overview",
  );
  mockAPI({ partialContext: true });
  render(<App />);
  expect(await screen.findByText(/restricted docsets omitted/)).toBeVisible();
});

test.each(["cold", "failed", "disabled"])(
  "%s usage with null activity stays usable until a complete result arrives",
  async (state) => {
    history.replaceState(
      null,
      "",
      "/dashboard/?view=analytics&path=/&tab=overview",
    );
    const fetch = mockAPI();
    const original = fetch.getMockImplementation()!;
    let complete = false;
    fetch.mockImplementation((input, init) =>
      !complete && String(input).includes("/api/usage?")
        ? Promise.resolve(
            new Response(
              JSON.stringify({
                reads: 0,
                activity: null,
                analytics: {
                  state,
                  complete: false,
                  updating: state === "cold",
                },
              }),
            ),
          )
        : original(input, init),
    );
    render(<App />);
    expect(
      await screen.findByText(/Activity totals will appear/),
    ).toBeVisible();
    // Knowledge does not wait for activity analytics.
    expect(screen.getByText("Context by folder")).toBeVisible();
    expect(
      screen.queryByRole("img", {
        name: "Daily activity stacked by attribution",
      }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByText("No activity in this time range."),
    ).not.toBeInTheDocument();

    complete = true;
    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: "Refresh analytics" }));
    expect(
      await screen.findByRole("img", {
        name: "Daily activity stacked by attribution",
      }),
    ).toBeVisible();
  },
);

test("knowledge renders while activity is still loading", async () => {
  history.replaceState(
    null,
    "",
    "/dashboard/?view=analytics&path=/&tab=knowledge",
  );
  const fetch = mockAPI();
  const original = fetch.getMockImplementation()!;
  let finishUsage!: () => void;
  fetch.mockImplementation((input, init) =>
    String(input).includes("/api/usage?")
      ? new Promise<Response>((resolve) => {
          finishUsage = () => resolve(original(input, init) as never);
        })
      : original(input, init),
  );
  render(<App />);
  expect(
    await screen.findByRole("heading", { name: "Knowledge & context" }),
  ).toBeVisible();
  expect(screen.getByText("Computing activity…")).toBeVisible();
  await act(async () => finishUsage());
  expect(
    await screen.findByRole("heading", { name: "Knowledge contribution" }),
  ).toBeVisible();
});

test("overview keeps knowledge visible and reports activity failures", async () => {
  history.replaceState(
    null,
    "",
    "/dashboard/?view=analytics&path=/&tab=overview",
  );
  const fetch = mockAPI();
  const original = fetch.getMockImplementation()!;
  fetch.mockImplementation((input, init) =>
    String(input).includes("/api/usage?")
      ? Promise.resolve(
          new Response(JSON.stringify({ error: "activity offline" }), {
            status: 503,
          }),
        )
      : original(input, init),
  );
  render(<App />);
  expect(await screen.findByText("activity offline")).toBeVisible();
  expect(screen.getByText("Context by folder")).toBeVisible();
});

test("polling aggregations keep their result mounted instead of flashing", async () => {
  history.replaceState(
    null,
    "",
    "/dashboard/?view=analytics&path=/&tab=commands",
  );
  const fetch = mockAPI();
  const original = fetch.getMockImplementation()!;
  let calls = 0;
  fetch.mockImplementation((input, init) => {
    if (!String(input).includes("/analytics/aggregations/top-commands"))
      return original(input, init);
    calls++;
    const response = new Response(
      JSON.stringify({
        status: "planned",
        note: "Analytics build failed",
        table: { columns: [], rows: [], total: 0 },
        computed_at: "",
        window: {},
        analytics: {
          state: "failed",
          updating: true,
          complete: false,
          error: "build failed",
        },
      }),
    );
    // Hold later polls open so an unmounted result would be observable.
    return calls === 1
      ? Promise.resolve(response)
      : new Promise<Response>(() => {});
  });
  render(<App />);
  const card = (await screen.findByRole("heading", { name: "Top commands" }))
    .parentElement!;
  await within(card).findByText("Analytics build failed");
  await waitFor(() => expect(calls).toBeGreaterThan(1), { timeout: 2500 });
  expect(within(card).queryByText("Computing analytics…")).toBeNull();
  expect(within(card).getByText("Analytics build failed")).toBeVisible();
});

test("aggregations reset on new filters and report refresh errors", async () => {
  history.replaceState(
    null,
    "",
    "/dashboard/?view=analytics&path=/&tab=commands",
  );
  const fetch = mockAPI();
  const original = fetch.getMockImplementation()!;
  let calls = 0;
  fetch.mockImplementation((input, init) => {
    const url = String(input);
    if (!url.includes("/analytics/aggregations/top-commands"))
      return original(input, init);
    calls++;
    if (url.includes("since=7d"))
      return Promise.resolve(
        new Response(JSON.stringify({ error: "unavailable" }), { status: 503 }),
      );
    if (calls > 1)
      return Promise.resolve(new Response("offline", { status: 503 }));
    return Promise.resolve(
      new Response(
        JSON.stringify({
          status: "ok",
          table: {
            columns: ["command", "count"],
            rows: [["cat", 3]],
            total: 1,
          },
          computed_at: "2026-10-03T00:00:00Z",
          window: {},
          analytics: { state: "stale", updating: true, complete: true },
        }),
      ),
    );
  });
  render(<App />);
  const card = (await screen.findByRole("heading", { name: "Top commands" }))
    .parentElement!;
  expect(await within(card).findByText("cat")).toBeVisible();
  // A same-query refresh failure keeps the result and reports the failure.
  expect(
    await within(card).findByText(/Could not refresh/, {}, { timeout: 2500 }),
  ).toBeVisible();
  expect(within(card).getByText("cat")).toBeVisible();

  await userEvent.setup().selectOptions(screen.getByRole("combobox"), "7");
  const refreshed = (
    await screen.findByRole("heading", { name: "Top commands" })
  ).parentElement!;
  expect(
    await within(refreshed).findByText("Request failed (503)"),
  ).toBeVisible();
  expect(within(refreshed).queryByText("cat")).toBeNull();
});

test("failed aggregations poll again at their retry time", async () => {
  history.replaceState(
    null,
    "",
    "/dashboard/?view=analytics&path=/&tab=commands",
  );
  const fetch = mockAPI();
  const original = fetch.getMockImplementation()!;
  let calls = 0;
  fetch.mockImplementation((input, init) => {
    if (!String(input).includes("/analytics/aggregations/top-commands"))
      return original(input, init);
    calls++;
    return Promise.resolve(
      new Response(
        JSON.stringify({
          status: calls === 1 ? "planned" : "ok",
          note: calls === 1 ? "Analytics build failed" : undefined,
          table:
            calls === 1
              ? { columns: [], rows: [], total: 0 }
              : { columns: ["command", "count"], rows: [["ls", 2]], total: 1 },
          computed_at: "",
          window: {},
          analytics:
            calls === 1
              ? {
                  state: "failed",
                  updating: false,
                  complete: false,
                  error: "build failed",
                  retry_at: new Date(Date.now() + 1500).toISOString(),
                }
              : { state: "ready", updating: false, complete: true },
        }),
      ),
    );
  });
  render(<App />);
  const card = (await screen.findByRole("heading", { name: "Top commands" }))
    .parentElement!;
  await within(card).findByText("Analytics build failed");
  expect(
    await within(card).findByText("ls", {}, { timeout: 3000 }),
  ).toBeVisible();
});

test("analytics status shows how far activity history is processed", async () => {
  history.replaceState(
    null,
    "",
    "/dashboard/?view=analytics&path=/&tab=overview",
  );
  const fetch = mockAPI();
  const original = fetch.getMockImplementation()!;
  fetch.mockImplementation((input, init) =>
    String(input).includes("/api/analytics-status")
      ? Promise.resolve(
          new Response(
            JSON.stringify({
              activity: {
                state: "updating",
                complete: false,
                processed_since: "2026-09-25T00:00:00Z",
                latest_event: "2026-10-02T09:30:00Z",
                events_processed: 1200,
              },
            }),
          ),
        )
      : original(input, init),
  );
  render(<App />);
  await userEvent
    .setup()
    .click(await screen.findByRole("button", { name: "Analytics status" }));
  const dialog = screen.getByRole("dialog", { name: "Analytics status" });
  expect(await within(dialog).findByText("Processing history")).toBeVisible();
  expect(within(dialog).getByText("Back to 25 Sept 2026")).toBeVisible();
});

test("background progress and completed results stay mounted through slow polls and errors", async () => {
  history.replaceState(
    null,
    "",
    "/dashboard/?view=analytics&path=/&tab=overview",
  );
  const fetch = mockAPI();
  const original = fetch.getMockImplementation()!;
  let calls = 0;
  let finishPoll!: (response: Response) => void;
  fetch.mockImplementation(async (input, init) => {
    const response = await original(input, init);
    if (!String(input).includes("/api/usage?")) return response;
    calls++;
    if (calls === 2)
      return new Promise<Response>((resolve) => {
        finishPoll = resolve;
      });
    return new Response(
      JSON.stringify({
        ...(await response.json()),
        analytics:
          calls === 1
            ? {
                state: "stale",
                complete: true,
                updating: true,
                progress: { phase: "history", processed: 731, unit: "events" },
              }
            : { state: "ready", complete: true, updating: false },
      }),
    );
  });
  render(<App />);
  const progress = await screen.findByRole("progressbar", {
    name: "Activity analytics progress",
  });
  await screen.findByText(/731 events processed/);
  const chart = screen.getByRole("img", {
    name: "Daily activity stacked by attribution",
  });
  expect(progress).not.toHaveAttribute("aria-valuenow");
  await waitFor(() => expect(calls).toBe(2), { timeout: 2000 });

  vi.useFakeTimers();
  try {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    expect(calls).toBe(2); // Do not abort a slow poll with another poll.
    expect(
      screen.getByRole("progressbar", { name: "Activity analytics progress" }),
    ).toBe(progress);
    expect(
      screen.getByRole("img", {
        name: "Daily activity stacked by attribution",
      }),
    ).toBe(chart);
    expect(
      screen.queryByText(/Updating selected scope/),
    ).not.toBeInTheDocument();
    await act(async () => {
      finishPoll(new Response("Unavailable", { status: 503 }));
    });
    expect(screen.getByText(/Could not refresh analytics/)).toBeVisible();
    expect(chart).toBeVisible();
    expect(progress).not.toHaveAttribute("aria-valuenow");
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000);
    });
    expect(progress).toHaveAttribute("aria-valuenow", "100");
    expect(chart).toBeVisible();
    expect(
      screen.queryByText(/Could not refresh analytics/),
    ).not.toBeInTheDocument();
    const completedCalls = calls;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(3000);
    });
    expect(calls).toBe(completedCalls);
  } finally {
    vi.useRealTimers();
  }
});

test("completed scans retain oversized-file warnings", async () => {
  history.replaceState(
    null,
    "",
    "/dashboard/?view=analytics&path=/&tab=overview",
  );
  const fetch = mockAPI();
  const original = fetch.getMockImplementation()!;
  fetch.mockImplementation(async (input, init) => {
    const response = await original(input, init);
    if (!String(input).includes("/api/context?")) return response;
    return new Response(
      JSON.stringify({
        ...(await response.json()),
        analytics: {
          state: "ready",
          complete: true,
          updating: false,
          warning:
            "Files over 64 MiB omitted from workspace knowledge totals: 1.",
        },
      }),
    );
  });
  render(<App />);
  expect(await screen.findByText(/Files over 64 MiB omitted/)).toBeVisible();
  expect(
    screen.getByRole("progressbar", { name: "Knowledge analytics progress" }),
  ).toHaveAttribute("aria-valuenow", "100");
});

test("legacy completed usage with null activity renders without crashing", async () => {
  history.replaceState(
    null,
    "",
    "/dashboard/?view=analytics&path=/&tab=overview",
  );
  const fetch = mockAPI();
  const original = fetch.getMockImplementation()!;
  fetch.mockImplementation(async (input, init) => {
    const response = await original(input, init);
    if (!String(input).includes("/api/usage?")) return response;
    return new Response(
      JSON.stringify({ ...(await response.json()), activity: null }),
    );
  });
  render(<App />);
  expect(
    await screen.findByText("No activity in this time range."),
  ).toBeVisible();
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
  const navigation = screen.getByRole("navigation", { name: "Mobile workspace" });
  const overview = within(navigation).getByRole("button", { name: "Overview" });
  expect(overview).toHaveAttribute("aria-haspopup", "dialog");
  expect(overview.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
  await user.click(overview);
  const dialog = screen.getByRole("dialog", { name: "Analytics sections" });
  expect(within(dialog).getByRole("button", { name: /Usage/ })).toBeVisible();
  expect(within(dialog).queryByRole("tab")).not.toBeInTheDocument();
  for (const button of within(dialog).getAllByRole("button")) {
    expect(button.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
  }
  fireEvent.keyDown(dialog, { key: "Escape" });
  await waitFor(() => expect(dialog).not.toBeInTheDocument());
  await user.click(overview);
  await user.click(
    within(screen.getByRole("dialog", { name: "Analytics sections" })).getByRole(
      "button",
      { name: "Usage" },
    ),
  );
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(within(navigation).getByRole("button", { name: "Usage" })).toHaveAttribute(
    "aria-haspopup",
    "dialog",
  );
  expect(new URLSearchParams(location.search).get("tab")).toBe("usage");
});

test("mobile folder tree stays open while unfurling folders", async () => {
  mockAPI();
  render(<App />);
  const user = userEvent.setup();
  await screen.findByRole("complementary", { name: "Knowledge tree" });
  await user.click(screen.getByRole("button", { name: "Folders" }));

  const dialog = screen.getByRole("dialog", { name: "Folders" });
  const tree = within(dialog).getByRole("complementary", {
    name: "Knowledge tree",
  });
  const folder = await within(tree).findByRole("button", { name: /guide/ });
  await user.click(folder);

  expect(dialog).toBeInTheDocument();
  expect(folder).toHaveAttribute("aria-expanded", "true");
  expect(
    await within(tree).findByRole("button", { name: /start.md/ }),
  ).toBeVisible();
});

test.each(["/dashboard/?view=files&path=/", "/lore/guide"])(
  "uses only the tree for folder navigation at %s",
  async (url) => {
    history.replaceState(null, "", url);
    mockAPI();
    render(<App />);
    const user = userEvent.setup();
    const tree = await screen.findByRole("complementary", {
      name: "Knowledge tree",
    });
    expect(document.querySelector(".folder-browser")).not.toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Open a file" })).toBeVisible();
    await within(tree).findByRole("button", { name: /guide/ });

    await user.click(screen.getByRole("button", { name: "Browse files" }));
    const dialog = screen.getByRole("dialog", { name: "Folders" });
    fireEvent.keyDown(dialog, { key: "Escape" });
    await waitFor(() => expect(dialog).not.toBeInTheDocument());
    await user.click(screen.getByRole("button", { name: "Browse files" }));
    const mobileTree = within(
      screen.getByRole("dialog", { name: "Folders" }),
    ).getByRole("complementary", { name: "Knowledge tree" });
    await user.click(within(mobileTree).getByRole("button", { name: /guide/ }));
    await user.click(
      await within(mobileTree).findByRole("button", { name: /start.md/ }),
    );
    expect(await screen.findByRole("heading", { name: "Start" })).toBeVisible();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Close start.md" }));
    expect(screen.getByRole("heading", { name: "Open a file" })).toBeVisible();

    await user.click(screen.getByRole("button", { name: "Analytics" }));
    await screen.findByText("Context by folder");
    expect(document.querySelector(".scope-children")).not.toBeInTheDocument();
    expect(within(tree).getByRole("button", { name: /guide/ })).toBeVisible();
  },
);

test("shows honest oversized-context error and hides Access without permission", async () => {
  history.replaceState(
    null,
    "",
    "/dashboard/?view=analytics&path=/&tab=overview",
  );
  mockAPI({ contextError: true });
  render(<App />);
  expect(await screen.findByRole("alert")).toHaveTextContent("too large");
  expect(
    screen.getByRole("progressbar", { name: "Knowledge analytics progress" }),
  ).toHaveAttribute("aria-valuenow", "0");
  expect(screen.getByText("Processing stopped")).toBeVisible();
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
  await waitFor(() => {
    const requests = fetch.mock.calls.map(([input]) => String(input));
    expect(requests.some((url) => url.includes("/most-used-lines?"))).toBe(
      true,
    );
    expect(requests.some((url) => url.includes("/least-used-lines?"))).toBe(
      true,
    );
  });
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
  await user.click(screen.getByRole("button", { name: "View file analytics" }));
  expect(new URLSearchParams(location.search).get("path")).toBe(
    "/guide/start.md",
  );
  await screen.findByRole("heading", { name: "Most-used lines" });
  expect(screen.getByText("Single-file analytics")).toBeVisible();
  await user.click(screen.getByRole("button", { name: "View" }));
  expect(await screen.findByRole("heading", { name: "Start" })).toBeVisible();
  await user.click(screen.getByRole("button", { name: "View file analytics" }));
  await screen.findByRole("heading", { name: "Most-used lines" });
  history.replaceState(null, "", "/lore/guide/start.md");
  fireEvent.popState(window);
  expect(await screen.findByRole("heading", { name: "Start" })).toBeVisible();
  expect(
    screen.getByRole("button", { name: "Show path for start.md" }),
  ).toHaveTextContent("start.md");
});

test("filename path tooltip does not navigate away from the open file", async () => {
  history.replaceState(null, "", "/lore/guide/start.md");
  mockAPI();
  render(<App />);
  const user = userEvent.setup();
  await user.click(
    await screen.findByRole("button", { name: "Show path for start.md" }),
  );
  expect(screen.getByRole("tooltip")).toHaveTextContent("/guide/start.md");
  expect(location.pathname).toBe("/lore/guide/start.md");
  expect(screen.getByRole("heading", { name: "Start" })).toBeVisible();
  expect(screen.getByRole("tab", { name: "start.md" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
  expect(
    screen.queryByRole("navigation", { name: "File location" }),
  ).not.toBeInTheDocument();
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

test("returning to the browser tab preserves workspace state during session refresh", async () => {
  history.replaceState(null, "", "/lore/guide/start.md");
  const fetch = mockAPI();
  const original = fetch.getMockImplementation()!;
  render(<App />);
  const user = userEvent.setup();
  await screen.findByRole("heading", { name: "Start" });
  await user.click(screen.getByRole("button", { name: "Source" }));
  expect(screen.getByRole("button", { name: "Source" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  const fileRequests = () =>
    fetch.mock.calls.filter(([input]) =>
      String(input).includes("/dashboard/api/file?"),
    ).length;
  const initialFileRequests = fileRequests();

  let finishRefresh!: (response: Response) => void;
  fetch.mockImplementation((input, init) =>
    String(input).endsWith("/dashboard/api/session")
      ? new Promise<Response>((resolve) => {
          finishRefresh = resolve;
        })
      : original(input, init),
  );
  fireEvent.focus(window);

  expect(screen.getByRole("button", { name: "Source" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
  expect(screen.queryByText("Loading your workspace…")).not.toBeInTheDocument();

  await act(async () => {
    finishRefresh(
      new Response(
        JSON.stringify({
          identity: "private@example.test",
          lore_path: "/lore",
          access: false,
        }),
      ),
    );
  });
  await waitFor(() =>
    expect(fileRequests()).toBeGreaterThan(initialFileRequests),
  );
  expect(screen.getByRole("button", { name: "Source" })).toHaveAttribute(
    "aria-pressed",
    "true",
  );
});

test("session refresh removes a document whose access was revoked", async () => {
  history.replaceState(null, "", "/lore/guide/start.md");
  const fetch = mockAPI();
  const original = fetch.getMockImplementation()!;
  let fileAccess = true;
  fetch.mockImplementation((input, init) =>
    !fileAccess && String(input).includes("/dashboard/api/file?")
      ? Promise.resolve(
          new Response(JSON.stringify({ error: "not found" }), { status: 404 }),
        )
      : original(input, init),
  );
  render(<App />);
  await screen.findByRole("heading", { name: "Start" });

  fileAccess = false;
  fireEvent.focus(window);

  expect(await screen.findByRole("alert")).toHaveTextContent(
    "This path is unavailable or no longer readable.",
  );
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

test("returning to an analytics tab reuses its fetched results", async () => {
  history.replaceState(
    null,
    "",
    "/dashboard/?view=analytics&path=/&tab=overview",
  );
  const fetch = mockAPI();
  render(<App />);
  const user = userEvent.setup();
  await screen.findByText("Context by folder");

  await user.click(screen.getByRole("tab", { name: "Gaps" }));
  await screen.findByRole("heading", { name: "Top search queries" });
  const analyticsRequests = () =>
    fetch.mock.calls
      .map(([input]) => String(input))
      .filter((url) =>
        /\/api\/(context|usage)\?|\/analytics\/aggregations\//.test(url),
      );
  const afterFirstVisit = analyticsRequests();

  await user.click(screen.getByRole("tab", { name: "Overview" }));
  await screen.findByText("Context by folder");
  await user.click(screen.getByRole("tab", { name: "Gaps" }));
  await screen.findByRole("heading", { name: "Top search queries" });

  expect(analyticsRequests()).toEqual(afterFirstVisit);
});

test("published results poll every minute and building results every second", () => {
  expect(pollDelay({ state: "cold", updating: true, complete: false })).toBe(
    1000,
  );
  expect(pollDelay({ state: "ready", updating: false, complete: true })).toBe(
    60_000,
  );
  expect(
    pollDelay({ state: "disabled", updating: false, complete: false }),
  ).toBeUndefined();
});

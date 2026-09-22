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
    expect(
      screen.queryByRole("heading", { name: "Activity" }),
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
  await user.click(screen.getByRole("button", { name: /Overview ⌃/ }));
  const dialog = screen.getByRole("dialog", { name: "Analytics sections" });
  expect(within(dialog).getByRole("button", { name: /Usage/ })).toBeVisible();
  expect(within(dialog).queryByRole("tab")).not.toBeInTheDocument();
  fireEvent.keyDown(dialog, { key: "Escape" });
  await waitFor(() => expect(dialog).not.toBeInTheDocument());
});

test("mobile folder tree stays open while unfurling folders", async () => {
  mockAPI();
  render(<App />);
  const user = userEvent.setup();
  await screen.findByRole("complementary", { name: "Knowledge tree" });
  await user.click(screen.getByRole("button", { name: /▱ Folders/ }));

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

import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { App } from "./App";
import { mockAPI } from "./test/fixtures";

const shortcut = () =>
  fireEvent.keyDown(window, { key: "p", ctrlKey: true, cancelable: true });

async function openFinder() {
  shortcut();
  const dialog = await screen.findByRole("dialog", { name: "Find file" });
  const input = within(dialog).getByRole("combobox", { name: "Find file" });
  expect(input).toHaveFocus();
  await within(dialog).findByText(/3 files\. Type to search\./);
  return { dialog, input };
}

test("Ctrl+P opens the finder, ranks fuzzy matches, and Enter opens the file", async () => {
  mockAPI();
  render(<App />);
  await screen.findByRole("complementary", { name: "Knowledge tree" });
  const user = userEvent.setup();
  const { dialog, input } = await openFinder();
  // Browser print must not fire.
  expect(shortcut()).toBe(false);
  await user.type(input, "strt");
  const options = within(dialog).getAllByRole("option");
  // "strategy" and "start" both match; "start" is shorter and compact.
  expect(options.map((o) => o.textContent)).toEqual([
    "start.md/guide/",
    "strategy.md/notes/",
  ]);
  expect(options[0]).toHaveAttribute("aria-selected", "true");
  expect(
    within(options[0]).getAllByText((_, el) => el?.tagName === "MARK").map((m) => m.textContent),
  ).toEqual(["st", "rt"]);
  await user.keyboard("{ArrowDown}");
  expect(options[1]).toHaveAttribute("aria-selected", "true");
  expect(input).toHaveAttribute("aria-activedescendant", options[1].id);
  await user.keyboard("{ArrowUp}{Enter}");
  expect(screen.queryByRole("dialog", { name: "Find file" })).toBeNull();
  expect(await screen.findByRole("heading", { name: "Start" })).toBeVisible();
  expect(location.pathname).toBe("/lore/guide/start.md");
  expect(screen.getByRole("tab", { name: "start.md" })).toHaveAttribute(
    "aria-selected",
    "true",
  );
});

test("the toolbar button opens the finder with open files first, Escape restores focus, and no-match is reported", async () => {
  mockAPI();
  history.replaceState(null, "", "/lore/guide/start.md");
  render(<App />);
  await screen.findByRole("heading", { name: "Start" });
  const user = userEvent.setup();
  const button = screen.getByRole("button", { name: "Find file" });
  await user.click(button);
  const dialog = await screen.findByRole("dialog", { name: "Find file" });
  await within(dialog).findByText(/3 files/);
  // The already-open file is offered before typing.
  expect(
    within(dialog).getAllByRole("option").map((o) => o.textContent),
  ).toEqual(["start.md/guide/"]);
  await user.keyboard("qzx");
  expect(within(dialog).queryAllByRole("option")).toHaveLength(0);
  expect(within(dialog).getByText("No files match “qzx”.")).toBeVisible();
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog", { name: "Find file" })).toBeNull();
  expect(button).toHaveFocus();
});

test("choosing a file on the analytics view scopes analytics instead of opening a tab", async () => {
  mockAPI();
  history.replaceState(null, "", "/dashboard/?view=analytics&path=/&tab=overview");
  render(<App />);
  await screen.findByRole("group", {
    name: "Full-depth context token distribution",
  });
  const user = userEvent.setup();
  const { input } = await openFinder();
  await user.type(input, "start{Enter}");
  expect(new URLSearchParams(location.search).get("view")).toBe("analytics");
  expect(new URLSearchParams(location.search).get("path")).toBe(
    "/guide/start.md",
  );
  expect(screen.queryByRole("tab", { name: "start.md" })).toBeNull();
});

test("a truncated listing says so instead of claiming the whole workspace", async () => {
  mockAPI().mockImplementation(async (input) => {
    const url = new URL(String(input), location.origin);
    if (url.pathname.endsWith("/session"))
      return new Response(
        JSON.stringify({ identity: "cap@example.test", lore_path: "/lore", access: false }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
    if (url.pathname.endsWith("/files"))
      return new Response(
        JSON.stringify({
          path: "/",
          entries: Array.from({ length: 10000 }, (_, i) => ({
            path: `/bulk/${i}.md`,
            name: `${i}.md`,
          })),
          truncated: true,
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      );
    return new Response(JSON.stringify({ path: "/", entries: [] }), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    });
  });
  render(<App />);
  await screen.findByRole("complementary", { name: "Knowledge tree" });
  shortcut();
  const dialog = await screen.findByRole("dialog", { name: "Find file" });
  expect(
    await within(dialog).findByText(/Searching the first 10,000 files/),
  ).toBeVisible();
  expect(within(dialog).getAllByRole("option")).toHaveLength(100);
});

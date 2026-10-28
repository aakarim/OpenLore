import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import { SourcesView } from "./Sources";
import type { Source } from "./types";

test("reads a latest source copy and its deletion history without offering mutations", async () => {
  const latest = {
    snapshot: "second",
    time: "2026-10-22T10:00:00Z",
    deleted: 1,
    files: { "notes/api.md": { sha256: "hash", size: 11, mode: 420 } },
    meta: { name: "payments", origin: "~/notes" },
  };
  const source: Source = {
    store: "folder/alice/payments",
    owner: "alice",
    projections: ["payments-copy"],
    latest,
  };
  vi.spyOn(globalThis, "fetch").mockImplementation(async (input) => {
    const url = new URL(String(input), location.origin);
    if (url.searchParams.get("file") === "notes/api.md")
      return new Response("Payment API");
    return new Response(
      JSON.stringify([{ ...latest, snapshot: "first", deleted: 0 }, latest]),
    );
  });
  render(
    <SourcesView
      sources={{ data: [source], loading: false, refresh: () => {} }}
      store={source.store}
      onSelect={() => {}}
    />,
  );
  expect(screen.getByText("Source copy · read-only")).toBeVisible();
  expect(screen.getByText("~/notes")).toBeVisible();
  expect(
    screen.queryByRole("button", { name: /add|remove|save/i }),
  ).not.toBeInTheDocument();
  const user = userEvent.setup();
  const tabs = within(
    screen.getByRole("navigation", { name: "Source sections" }),
  );
  await user.click(tabs.getByRole("button", { name: "Files" }));
  await user.type(screen.getByRole("searchbox"), "api");
  await user.click(screen.getByRole("button", { name: /notes\/api.md/ }));
  expect(await screen.findByText("Payment API")).toBeVisible();
  await user.click(tabs.getByRole("button", { name: "Activity" }));
  const table = await screen.findByRole("table");
  expect(within(table).getByText("first")).toBeVisible();
  expect(within(table).getByText("second")).toBeVisible();
  expect(
    within(table).getByRole("columnheader", { name: "Deleted" }),
  ).toBeVisible();
});

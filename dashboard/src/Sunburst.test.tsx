import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { beforeEach, vi } from "vitest";
import { Sunburst } from "./Sunburst";
import type { ContextNode } from "./types";

const file = (path: string, characters: number, tokens = 999): ContextNode => ({
  path,
  name: path.split("/").at(-1) || path,
  directory: false,
  bytes: characters,
  lines: 1,
  characters,
  tokens,
});

const folder = (
  path: string,
  children: ContextNode[],
  characters = children.reduce((total, child) => total + child.characters, 0),
): ContextNode => ({
  path,
  name: path.split("/").at(-1) || "Workspace",
  directory: true,
  bytes: children.reduce((total, child) => total + child.bytes, 0),
  lines: children.reduce((total, child) => total + child.lines, 0),
  characters,
  tokens: 99999,
  children,
});

const props = { contextWindow: 200, onSelect: vi.fn() };

beforeEach(() => {
  vi.mocked(matchMedia).mockImplementation(
    (query) =>
      ({
        matches: false,
        media: query,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      }) as unknown as MediaQueryList,
  );
});

test("uses additive rounded leaf estimates for the center and asymmetric arcs", async () => {
  const deep = folder("/deep", [
    file("/deep/large.md", 9),
    file("/deep/zero.md", 0),
  ]);
  const root = folder("/", [file("/small.md", 5), deep], 14);
  const { rerender } = render(<Sunburst node={root} ratio={4} {...props} />);

  expect(screen.getByText("5")).toBeInTheDocument();
  const small = screen.getByRole("button", { name: "/small.md: 2 tokens" });
  const branch = screen.getByRole("button", { name: "/deep: 3 tokens" });
  const nested = screen.getByRole("button", {
    name: "/deep/large.md: 3 tokens",
  });
  expect(Number(small.dataset.end)).toBeCloseTo((Math.PI * 2 * 2) / 5);
  expect(Number(branch.dataset.start)).toBeCloseTo(Number(small.dataset.end));
  expect(Number(nested.dataset.inner)).toBeGreaterThan(
    Number(branch.dataset.inner),
  );
  expect(
    screen.queryByRole("button", { name: /zero\.md/ }),
  ).not.toBeInTheDocument();
  expect(small.getAttribute("d")).not.toContain("NaN");

  rerender(<Sunburst node={root} ratio={6} {...props} />);
  await waitFor(() => expect(screen.getByText("3")).toBeInTheDocument());
  expect(
    Number(
      screen.getByRole("button", { name: "/small.md: 1 tokens" }).dataset.end,
    ),
  ).toBeCloseTo((Math.PI * 2) / 3);
});

test("shows an accessible live path tooltip on hover, focus, and tap", () => {
  render(
    <Sunburst
      node={folder("/", [file("/guide.md", 20)])}
      ratio={4}
      {...props}
    />,
  );
  const segment = screen.getByRole("button", { name: "/guide.md: 5 tokens" });
  const tooltip = screen.getByLabelText("Context segment details");

  fireEvent.mouseEnter(segment);
  expect(tooltip).toHaveTextContent("/guide.md: 5 tokens (100.0%)");
  fireEvent.focus(segment);
  expect(tooltip).toHaveTextContent("/guide.md");
  fireEvent.pointerDown(segment);
  expect(tooltip).toHaveTextContent("/guide.md");
});

test("bounds mobile charts and aggregates excess top-level scopes", () => {
  vi.mocked(matchMedia).mockImplementation(
    (query) =>
      ({
        matches: query === "(max-width: 760px)",
        media: query,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      }) as unknown as MediaQueryList,
  );
  const branch = folder("/guide", [
    file("/guide/one.md", 20),
    file("/guide/two.md", 40),
  ]);
  const rootFiles = Array.from({ length: 30 }, (_, index) =>
    file(`/root-${index}.md`, 20),
  );
  render(
    <Sunburst
      node={folder("/", [branch, ...rootFiles])}
      ratio={4}
      {...props}
    />,
  );

  expect(
    screen.getByRole("group", {
      name: "Top-level context token distribution",
    }),
  ).toBeInTheDocument();
  expect(screen.getByText("165")).toBeInTheDocument();
  expect(screen.getAllByRole("button")).toHaveLength(23);
  expect(screen.getByRole("button", { name: "/guide: 15 tokens" })).toBeVisible();
  expect(screen.queryByRole("button", { name: /one\.md/ })).not.toBeInTheDocument();
  expect(
    screen.getByRole("img", { name: "Other (8 items): 40 tokens" }),
  ).toBeVisible();
});

test("renders file utilization and explicit empty-folder states safely", () => {
  const selected = file("/guide/readme.md", 401);
  const { rerender } = render(
    <Sunburst
      node={selected}
      ratio={4}
      contextWindow={200}
      onSelect={vi.fn()}
    />,
  );
  expect(
    screen.getByRole("img", {
      name: "/guide/readme.md: 101 context tokens, 50.50% of the selected context window",
    }),
  ).toBeInTheDocument();
  expect(screen.getByText("readme.md")).toHaveAccessibleName("Selected file");
  expect(screen.getByTestId("file-utilization-ring")).toHaveAttribute(
    "stroke-dasharray",
    expect.not.stringContaining("NaN"),
  );

  rerender(
    <Sunburst
      node={folder("/empty", [file("/empty/zero.md", 0)])}
      ratio={4}
      contextWindow={200}
      onSelect={vi.fn()}
    />,
  );
  expect(screen.getByRole("status")).toHaveTextContent(
    "Empty folder — no context to display.",
  );
  expect(screen.queryByRole("button")).not.toBeInTheDocument();
});

test("interpolates canonical-path geometry and cancels an unfinished transition", () => {
  const frames: FrameRequestCallback[] = [];
  const request = vi
    .spyOn(globalThis, "requestAnimationFrame")
    .mockImplementation((callback) => {
      frames.push(callback);
      return frames.length;
    });
  const cancel = vi
    .spyOn(globalThis, "cancelAnimationFrame")
    .mockImplementation(() => {});
  const child = file("/a/readme.md", 40);
  const branch = folder("/a", [child]);
  const { rerender, unmount } = render(
    <Sunburst
      node={folder("/", [branch, file("/other.md", 60)])}
      ratio={4}
      {...props}
    />,
  );
  const before = screen
    .getByRole("button", { name: "/a/readme.md: 10 tokens" })
    .getAttribute("d");

  rerender(<Sunburst node={branch} ratio={4} {...props} />);
  expect(request).toHaveBeenCalled();
  expect(
    screen
      .getByRole("button", { name: "/a/readme.md: 10 tokens" })
      .getAttribute("d"),
  ).toBe(before);
  act(() => frames.shift()?.(0));
  act(() => frames.shift()?.(175));
  const middle = screen
    .getByRole("button", { name: "/a/readme.md: 10 tokens" })
    .getAttribute("d");
  expect(middle).not.toBe(before);
  unmount();
  expect(cancel).toHaveBeenCalled();
});

test("honors reduced motion when changing folders", () => {
  vi.mocked(matchMedia).mockImplementation(
    (query) =>
      ({
        matches: query === "(prefers-reduced-motion: reduce)",
        media: query,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      }) as unknown as MediaQueryList,
  );
  const request = vi.spyOn(globalThis, "requestAnimationFrame");
  const child = file("/a/readme.md", 40);
  const branch = folder("/a", [child]);
  const { rerender } = render(
    <Sunburst node={folder("/", [branch])} ratio={4} {...props} />,
  );
  rerender(<Sunburst node={branch} ratio={4} {...props} />);
  expect(request).not.toHaveBeenCalled();
  expect(
    screen.getByRole("button", { name: "/a/readme.md: 10 tokens" }).dataset
      .outer,
  ).toBe("190");
});

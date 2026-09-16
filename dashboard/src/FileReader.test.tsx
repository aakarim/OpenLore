import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { vi } from "vitest";
import { api } from "./api";
import {
  Details,
  FileReader,
  MobileDetails,
  Sheet,
  resolveLorePath,
} from "./FileReader";
import type { FileResponse, Session } from "./types";

const session: Session = {
  identity: "reader@example.test",
  lore_path: "/lore",
  access: false,
};
const baseFile: FileResponse = {
  path: "/docs/readme.txt",
  name: "readme.txt",
  source: "Plain text knowledge",
  content_type: "text/plain",
  facts: { bytes: 20, lines: 1, characters: 13, tokens: 999 },
  binary: false,
};
const readerProps = {
  path: baseFile.path,
  session,
  mode: "preview" as const,
  onMode: vi.fn(),
  onFile: vi.fn(),
  onAnalytics: vi.fn(),
  ratio: 6,
  contextWindow: 12,
  scrollPosition: 0,
  onScroll: vi.fn(),
};

test("renders plaintext preview and estimates FileInfo from characters and ratio", async () => {
  vi.spyOn(api, "file").mockResolvedValue(baseFile);
  render(<FileReader {...readerProps} />);

  expect(await screen.findByText("Plain text knowledge")).toBeVisible();
  expect(screen.getByText("3")).toBeVisible();
  expect(screen.getByText("25.00%")).toBeVisible();
  expect(screen.queryByText("999")).not.toBeInTheDocument();
});

test("canonicalizes internal links once, preserves modified clicks, and protects images", async () => {
  const onFile = vi.fn();
  const file: FileResponse = {
    ...baseFile,
    path: "/docs/current.md",
    html: `<a id="relative" href="a%20b.md#part">Relative</a>
      <a id="absolute" href="${location.origin}/lore/shared/guide.md">Absolute</a>
      <a id="remote" href="https://outside.example/guide">Remote</a>
      <img alt="local" src="images/pic%20one.png">
      <img alt="remote image" src="https://outside.example/pixel.png">`,
  };
  vi.spyOn(api, "file").mockResolvedValue(file);
  render(<FileReader {...readerProps} path={file.path} onFile={onFile} />);

  const relative = (await screen.findByText("Relative")) as HTMLAnchorElement;
  expect(relative.href).toBe(`${location.origin}/lore/docs/a%20b.md#part`);
  expect(relative.href).not.toContain("%2520");
  fireEvent.click(relative);
  expect(onFile).toHaveBeenCalledWith("/docs/a b.md");

  const modified = new MouseEvent("click", {
    bubbles: true,
    cancelable: true,
    ctrlKey: true,
  });
  relative.dispatchEvent(modified);
  expect(modified.defaultPrevented).toBe(false);
  expect(onFile).toHaveBeenCalledTimes(1);

  fireEvent.click(screen.getByText("Absolute"));
  expect(onFile).toHaveBeenLastCalledWith("/shared/guide.md");
  expect(screen.getByText("Remote")).toHaveAttribute("target", "_blank");
  const localImage = screen.getByAltText("local") as HTMLImageElement;
  expect(new URL(localImage.src).pathname).toBe("/dashboard/api/raw");
  expect(new URL(localImage.src).searchParams.get("path")).toBe(
    "/docs/images/pic one.png",
  );
  expect(screen.getByAltText("remote image")).not.toHaveAttribute("src");
  expect(resolveLorePath("a%20b.md", "/docs/current.md")).toBe("/docs/a b.md");
});

test("restores and tracks window scroll on mobile while desktop uses document scroll", async () => {
  vi.spyOn(api, "file").mockResolvedValue(baseFile);
  let scrollY = 456;
  Object.defineProperty(window, "scrollY", {
    configurable: true,
    get: () => scrollY,
  });
  const scrollTo = vi.spyOn(window, "scrollTo").mockImplementation(() => {});
  vi.mocked(matchMedia).mockImplementation(
    (query) => ({ matches: query.includes("max-width") }) as MediaQueryList,
  );
  const onScroll = vi.fn();
  const { unmount } = render(
    <FileReader {...readerProps} scrollPosition={123} onScroll={onScroll} />,
  );
  await screen.findByText("Plain text knowledge");
  expect(scrollTo).toHaveBeenCalledWith(0, 123);
  fireEvent.scroll(window);
  expect(onScroll).toHaveBeenCalledWith(456);
  unmount();

  vi.mocked(matchMedia).mockReturnValue({ matches: false } as MediaQueryList);
  const desktopScroll = vi.fn();
  const view = render(
    <FileReader
      {...readerProps}
      scrollPosition={77}
      onScroll={desktopScroll}
    />,
  );
  await screen.findByText("Plain text knowledge");
  const container = view.container.querySelector(
    ".document-scroll",
  ) as HTMLElement;
  expect(container.scrollTop).toBe(77);
  container.scrollTop = 81;
  fireEvent.scroll(container);
  expect(desktopScroll).toHaveBeenCalledWith(81);
});

test("MobileDetails exposes preview/source mode and ratio-derived facts", async () => {
  vi.spyOn(api, "file").mockResolvedValue(baseFile);
  vi.spyOn(window, "scrollTo").mockImplementation(() => {});
  const onMode = vi.fn();
  render(
    <MobileDetails
      path={baseFile.path}
      session={session}
      mode="preview"
      onMode={onMode}
      ratio={6}
      contextWindow={12}
      onClose={vi.fn()}
      onAnalytics={vi.fn()}
      onSettings={vi.fn()}
    />,
  );
  await screen.findByRole("button", { name: "Preview" });
  fireEvent.click(screen.getByRole("button", { name: "Source" }));
  expect(onMode).toHaveBeenCalledWith("source");
  expect(screen.getByText("3")).toBeVisible();
});

test("Sheet locks and restores body scroll, focus, and both dialog-edge tabs", () => {
  let scrollY = 88;
  Object.defineProperty(window, "scrollY", {
    configurable: true,
    get: () => scrollY,
  });
  const scrollTo = vi.spyOn(window, "scrollTo").mockImplementation(() => {});
  const outside = document.createElement("button");
  document.body.append(outside);
  outside.focus();
  const { unmount } = render(
    <Sheet title="Safe sheet" onClose={vi.fn()}>
      <button>First action</button>
      <button>Last action</button>
    </Sheet>,
  );
  const dialog = screen.getByRole("dialog", { name: "Safe sheet" });
  expect(dialog).toHaveFocus();
  expect(document.body.style.position).toBe("fixed");
  fireEvent.keyDown(dialog, { key: "Tab" });
  expect(screen.getByRole("button", { name: "Close" })).toHaveFocus();
  dialog.focus();
  fireEvent.keyDown(dialog, { key: "Tab", shiftKey: true });
  expect(screen.getByRole("button", { name: "Last action" })).toHaveFocus();
  unmount();
  expect(document.body.style.position).toBe("");
  expect(scrollTo).toHaveBeenLastCalledWith(0, 88);
  expect(outside).toHaveFocus();
  outside.remove();
});

test("timeline paginates and appends entries using next_cursor", async () => {
  const history = vi.spyOn(api, "history");
  history
    .mockResolvedValueOnce({
      available: true,
      entries: [
        {
          time: "2026-09-01T10:00:00Z",
          attribution: "human",
          action: "write",
          hash: "firsthash",
        },
      ],
      next_cursor: "page-2",
    })
    .mockResolvedValueOnce({
      available: true,
      entries: [
        {
          time: "2026-08-31T10:00:00Z",
          attribution: "agent",
          action: "write",
          hash: "secondhash",
        },
      ],
    });
  render(
    <Details
      file={baseFile}
      contextWindow={200}
      onCopy={vi.fn()}
      onAnalytics={vi.fn()}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Timeline" }));
  await screen.findByText("firsthash");
  await userEvent.click(screen.getByRole("button", { name: "Load more" }));
  expect(await screen.findByText("secondhas")).toBeVisible();
  expect(history).toHaveBeenLastCalledWith(baseFile.path, "page-2");
  expect(
    screen.queryByRole("button", { name: "Load more" }),
  ).not.toBeInTheDocument();
});

test("clipboard actions announce success and failure", async () => {
  const copy = vi
    .fn()
    .mockResolvedValueOnce(undefined)
    .mockRejectedValueOnce(new Error("denied"));
  render(
    <Details
      file={baseFile}
      ratio={6}
      contextWindow={12}
      onCopy={copy}
      onAnalytics={vi.fn()}
    />,
  );
  fireEvent.click(screen.getByRole("button", { name: "Copy agent path" }));
  expect(await screen.findByText("Agent path copied.")).toBeVisible();
  fireEvent.click(screen.getByRole("button", { name: "Copy URL" }));
  expect(await screen.findByText("Couldn’t copy url.")).toBeVisible();
});

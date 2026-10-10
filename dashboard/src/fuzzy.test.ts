import { expect, test } from "vitest";
import { rankPaths, score } from "./fuzzy";

const order = (paths: string[], query: string) =>
  rankPaths(paths, query).map((m) => m.path);

test("matching is an ordered subsequence check", () => {
  expect(score("/guide/start.md", "gsm")?.positions).toEqual([1, 7, 13]);
  expect(score("/guide/start.md", "msg")).toBeNull();
});

test("non-matching paths are excluded and matching is case-insensitive", () => {
  expect(order(["/README.md", "/notes/zebra.md"], "readme")).toEqual([
    "/README.md",
  ]);
  expect(order(["/a.md"], "zzz")).toEqual([]);
});

test("filename matches outrank the same match in a directory", () => {
  expect(
    order(["/guide/start.md", "/docs/guide.md", "/guide/index.md"], "guide"),
  ).toEqual(["/docs/guide.md", "/guide/index.md", "/guide/start.md"]);
});

test("segment and word boundaries beat mid-word matches", () => {
  expect(
    order(["/learn.md", "/release-notes.md", "/corner.md"], "rn"),
  ).toEqual(["/release-notes.md", "/learn.md", "/corner.md"]);
  expect(order(["/guide/restart.md", "/guide/start.md"], "start")).toEqual([
    "/guide/start.md",
    "/guide/restart.md",
  ]);
});

test("shorter paths win exact ties and highlights stay contiguous", () => {
  expect(order(["/a/b/c/notes.md", "/a/notes.md"], "notes")).toEqual([
    "/a/notes.md",
    "/a/b/c/notes.md",
  ]);
  // "t" could jump to the segment boundary of "test"; staying contiguous with
  // "s" wins.
  expect(score("/stat/test.md", "st")?.positions).toEqual([1, 2]);
  expect(score("/guide/start.md", "start")?.positions).toEqual([
    7, 8, 9, 10, 11,
  ]);
});

test("boundary preference never strands the rest of the query", () => {
  // "a" at the segment boundary of "/alpha" would leave no "x" after it, so
  // the earlier mid-word "a" in "max" must be chosen.
  expect(score("/max/alpha", "ax")?.positions).toEqual([2, 3]);
});

test("whitespace separates terms instead of matching literally", () => {
  expect(order(["/decisions/search-ranking.md", "/a.md"], "search rank")).toEqual([
    "/decisions/search-ranking.md",
  ]);
});

test("empty query lists candidates in order and limit caps results", () => {
  const paths = Array.from({ length: 250 }, (_, i) => `/doc-${i}.md`);
  expect(order(paths, "")).toHaveLength(100);
  expect(order(paths, "").slice(0, 2)).toEqual(["/doc-0.md", "/doc-1.md"]);
  expect(rankPaths(paths, "doc", 5)).toHaveLength(5);
});

test("ranks ten thousand paths within an interactive budget", () => {
  const paths = Array.from(
    { length: 10_000 },
    (_, i) => `/area-${i % 37}/topic-${i % 211}/note-${i}.md`,
  );
  const started = performance.now();
  const first = rankPaths(paths, "n");
  const narrowed = rankPaths(paths, "note-99");
  const elapsed = performance.now() - started;
  expect(first).toHaveLength(100);
  expect(narrowed[0].path).toBe("/area-25/topic-99/note-99.md");
  // Generous so CI noise never fails it; a regression to quadratic work would.
  expect(elapsed).toBeLessThan(500);
});

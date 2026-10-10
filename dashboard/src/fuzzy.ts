// Fuzzy ranking for the file finder. Pure: it knows nothing about scopes,
// docsets, or the network. Callers decide which paths are candidates.

export type Match = { path: string; score: number; positions: number[] };

// Scoring weights, in the spirit of fzf: a match that starts a path segment or
// word is worth more than one buried mid-word, runs of adjacent matches are
// worth more than scattered ones, and anything in the filename outranks the
// same thing in a directory. Gaps cost a little so compact matches win, and
// long filenames cost a little so "note-99" beats "note-9917" for "note-99".
const MATCH = 16;
const BOUNDARY_BONUS = 24;
const SEGMENT_BONUS = 32;
const CONSECUTIVE_BONUS = 12;
const FILENAME_BONUS = 40;
const GAP_PENALTY = 1;
const LENGTH_PENALTY = 0.5;

const isBoundary = (previous: string) =>
  previous === "-" || previous === "_" || previous === "." || previous === " ";

/**
 * Scores one path against a lowercased query, or returns null when the query
 * is not an ordered subsequence of it. Greedy forward alignment that prefers
 * starting a match at a boundary when the same character also appears mid-word
 * within reach; a full dynamic program is not worth its cost for path-shaped
 * strings.
 */
export function score(path: string, query: string): Match | null {
  if (!query) return { path, score: 0, positions: [] };
  const lower = path.toLowerCase();
  // latest[i] is the rightmost index where query[i] can sit while the rest of
  // the query still fits after it. Computing it also proves containment, so
  // most of a large list is rejected here before any scoring work.
  const latest = new Array<number>(query.length);
  let end = lower.length;
  for (let i = query.length - 1; i >= 0; i--) {
    end = lower.lastIndexOf(query[i], end - 1);
    if (end === -1) return null;
    latest[i] = end;
  }
  const filenameStart = lower.lastIndexOf("/") + 1;
  const positions: number[] = [];
  let total = 0;
  let at = 0;
  for (let i = 0; i < query.length; i++) {
    const char = query[i];
    let index = lower.indexOf(char, at);
    const last = positions.length ? positions[positions.length - 1] : -2;
    // A character that continues the current run is kept as-is so highlights
    // stay contiguous instead of jumping to a later boundary.
    let probe = index === last + 1 ? -1 : index;
    while (probe !== -1 && probe <= latest[i]) {
      const previous = probe === 0 ? "/" : lower[probe - 1];
      if (previous === "/" || isBoundary(previous)) {
        index = probe;
        break;
      }
      probe = lower.indexOf(char, probe + 1);
    }
    const previous = index === 0 ? "/" : lower[index - 1];
    let gained = MATCH;
    if (previous === "/") gained += SEGMENT_BONUS;
    else if (isBoundary(previous)) gained += BOUNDARY_BONUS;
    if (index === last + 1) gained += CONSECUTIVE_BONUS;
    else if (positions.length) gained -= GAP_PENALTY * (index - last - 1);
    if (index >= filenameStart) gained += FILENAME_BONUS;
    total += gained;
    positions.push(index);
    at = index + 1;
  }
  const filenameLength = lower.length - filenameStart;
  return { path, score: total - filenameLength * LENGTH_PENALTY, positions };
}

/**
 * Ranks `paths` against `query` and returns the best `limit` matches, best
 * first. An empty query returns the first `limit` paths unscored so the palette
 * has something to show before the user types.
 */
export function rankPaths(paths: string[], query: string, limit = 100): Match[] {
  // Spaces separate terms rather than matching literally, so "search rank"
  // finds "search-ranking.md".
  const needle = query.replace(/\s+/g, "").toLowerCase();
  if (!needle)
    return paths
      .slice(0, limit)
      .map((path) => ({ path, score: 0, positions: [] }));
  const matches: Match[] = [];
  for (const path of paths) {
    const match = score(path, needle);
    if (match) matches.push(match);
  }
  matches.sort(
    (a, b) =>
      b.score - a.score ||
      a.path.length - b.path.length ||
      (a.path < b.path ? -1 : 1),
  );
  return matches.length > limit ? matches.slice(0, limit) : matches;
}

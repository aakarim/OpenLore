import {
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
} from "react";
import type { ContextNode } from "./types";

const colors = [
  "#ed986f",
  "#9e8cd0",
  "#71b6b0",
  "#dfbd73",
  "#d786aa",
  "#84a9db",
];
const duration = 350;

type Geometry = {
  start: number;
  end: number;
  inner: number;
  outer: number;
};
type Shape = Geometry & {
  node: ContextNode;
  tokens: number;
  color: string;
};

const point = (radius: number, angle: number) =>
  `${200 + radius * Math.sin(angle)},${200 - radius * Math.cos(angle)}`;

function arc({ start, end, inner, outer }: Geometry) {
  if (end <= start || outer <= inner) return "";
  const gap = Math.min(0.009, (end - start) / 7);
  const a = start + gap;
  const b = end - gap;
  if (b <= a) return "";
  const large = b - a > Math.PI ? 1 : 0;
  return `M${point(inner, a)}L${point(outer, a)}A${outer},${outer} 0 ${large} 1 ${point(outer, b)}L${point(inner, b)}A${inner},${inner} 0 ${large} 0 ${point(inner, a)}Z`;
}

export function estimatedTokens(node: ContextNode, ratio: 4 | 6): number {
  if (!node.directory) return Math.ceil(Math.max(0, node.characters) / ratio);
  return (node.children ?? []).reduce(
    (total, child) => total + estimatedTokens(child, ratio),
    0,
  );
}

function visibleDepth(node: ContextNode, ratio: 4 | 6): number {
  const children = (node.children ?? []).filter(
    (child) => estimatedTokens(child, ratio) > 0,
  );
  return children.length
    ? 1 + Math.max(...children.map((child) => visibleDepth(child, ratio)))
    : 1;
}

function layout(node: ContextNode, ratio: 4 | 6): Shape[] {
  const shapes: Shape[] = [];
  const rootTokens = estimatedTokens(node, ratio);
  if (!node.directory || rootTokens === 0) return shapes;
  const levels = Math.max(1, visibleDepth(node, ratio) - 1);
  const ringWidth = 122 / levels;

  const walk = (
    current: ContextNode,
    start: number,
    end: number,
    level: number,
    color: string,
  ) => {
    const tokens = estimatedTokens(current, ratio);
    if (tokens === 0 || end <= start) return;
    shapes.push({
      node: current,
      tokens,
      color,
      start,
      end,
      inner: 70 + (level - 1) * ringWidth,
      outer: 70 + level * ringWidth - 2,
    });
    let angle = start;
    for (const child of current.children ?? []) {
      const childTokens = estimatedTokens(child, ratio);
      if (childTokens === 0) continue;
      const next = angle + ((end - start) * childTokens) / tokens;
      walk(child, angle, next, level + 1, color);
      angle = next;
    }
  };

  let angle = 0;
  (node.children ?? []).forEach((child, index) => {
    const tokens = estimatedTokens(child, ratio);
    if (tokens === 0) return;
    const next = angle + (Math.PI * 2 * tokens) / rootTokens;
    walk(child, angle, next, 1, colors[index % colors.length]);
    angle = next;
  });
  return shapes;
}

function interpolate(from: Geometry, to: Geometry, progress: number): Geometry {
  const value = (key: keyof Geometry) =>
    from[key] + (to[key] - from[key]) * progress;
  return {
    start: value("start"),
    end: value("end"),
    inner: value("inner"),
    outer: value("outer"),
  };
}

function geometry(shape: Geometry): Geometry {
  return {
    start: shape.start,
    end: shape.end,
    inner: shape.inner,
    outer: shape.outer,
  };
}

function fallbackGeometry(
  shape: Shape,
  previous: Map<string, Shape>,
): Geometry {
  let parent = shape.node.path;
  while (parent.includes("/")) {
    parent = parent.slice(0, parent.lastIndexOf("/")) || "/";
    const match = previous.get(parent);
    if (match) return geometry(match);
    if (parent === "/") break;
  }
  const middle = (shape.start + shape.end) / 2;
  return {
    start: middle,
    end: middle,
    inner: shape.inner,
    outer: shape.inner,
  };
}

function FileContext({
  node,
  ratio,
  contextWindow,
}: {
  node: ContextNode;
  ratio: 4 | 6;
  contextWindow: number;
}) {
  const tokens = estimatedTokens(node, ratio);
  const utilization = contextWindow > 0 ? tokens / contextWindow : 0;
  const visible = Math.min(1, Math.max(0, utilization));
  const circumference = 2 * Math.PI * 122;
  const label = `${node.path}: ${tokens.toLocaleString()} context tokens, ${(utilization * 100).toFixed(2)}% of the selected context window`;
  return (
    <div className="sunburst-wrap">
      <svg
        className="sunburst"
        viewBox="0 0 400 400"
        role="img"
        aria-label={label}
      >
        <circle
          cx="200"
          cy="200"
          r="122"
          fill="none"
          stroke="var(--border)"
          strokeWidth="25"
        />
        <circle
          cx="200"
          cy="200"
          r="122"
          fill="none"
          stroke={colors[0]}
          strokeWidth="25"
          strokeLinecap="round"
          strokeDasharray={`${visible * circumference} ${circumference}`}
          transform="rotate(-90 200 200)"
          data-testid="file-utilization-ring"
        />
        <circle cx="200" cy="200" r="88" className="sunburst-center" />
        <text x="200" y="181" textAnchor="middle" className="sunburst-total">
          {tokens.toLocaleString()}
        </text>
        <text x="200" y="204" textAnchor="middle">
          context tokens
        </text>
        <text x="200" y="230" textAnchor="middle">
          {(utilization * 100).toFixed(2)}% of window
        </text>
      </svg>
      <p
        className="sunburst-file-label"
        aria-label="Selected file"
        style={{ color: "var(--muted)", fontSize: 11, textAlign: "center" }}
      >
        {node.name || node.path}
      </p>
    </div>
  );
}

export function Sunburst({
  node,
  ratio = 4,
  contextWindow = 200_000,
  onSelect,
}: {
  node: ContextNode;
  ratio?: 4 | 6;
  contextWindow?: number;
  onSelect: (node: ContextNode) => void;
}) {
  const title = useId();
  const target = useMemo(() => layout(node, ratio), [node, ratio]);
  const total = useMemo(() => estimatedTokens(node, ratio), [node, ratio]);
  const [shapes, setShapes] = useState(target);
  const [tooltip, setTooltip] = useState("");
  const previous = useRef(target);
  const previousPath = useRef(node.path);

  useEffect(() => {
    const old = new Map(
      previous.current.map((shape) => [shape.node.path, shape]),
    );
    const folderChanged = previousPath.current !== node.path;
    previousPath.current = node.path;
    previous.current = target;

    const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;
    if (!folderChanged || reduced || old.size === 0 || target.length === 0) {
      setShapes(target);
      return;
    }

    const starts = target.map((shape) => {
      const existing = old.get(shape.node.path);
      return {
        ...shape,
        ...(existing ? geometry(existing) : fallbackGeometry(shape, old)),
      };
    });
    setShapes(starts);
    let frame = 0;
    let started: number | undefined;
    const animate = (time: number) => {
      started ??= time;
      const progress = Math.min(1, (time - started) / duration);
      const eased = 1 - Math.pow(1 - progress, 3);
      setShapes(
        target.map((shape, index) => ({
          ...shape,
          ...interpolate(starts[index], shape, eased),
        })),
      );
      if (progress < 1) frame = requestAnimationFrame(animate);
    };
    frame = requestAnimationFrame(animate);
    return () => cancelAnimationFrame(frame);
  }, [node.path, target]);

  if (!node.directory)
    return <FileContext {...{ node, ratio, contextWindow }} />;
  if (total === 0)
    return (
      <div className="sunburst-wrap sunburst-empty" role="status">
        <strong>{node.name || node.path}</strong>
        <p>Empty folder — no context to display.</p>
      </div>
    );

  const inspect = (shape: Shape) =>
    setTooltip(
      `${shape.node.path}: ${shape.tokens.toLocaleString()} tokens (${((shape.tokens / total) * 100).toFixed(1)}%)`,
    );

  return (
    <div className="sunburst-wrap">
      <svg
        className="sunburst"
        viewBox="0 0 400 400"
        role="group"
        aria-labelledby={title}
      >
        <title id={title}>Full-depth context token distribution</title>
        {shapes.map((shape) => (
          <path
            key={shape.node.path}
            className="sunburst-segment"
            style={{ animation: "none" } as CSSProperties}
            d={arc(shape)}
            fill={shape.color}
            opacity={Math.max(0.52, 1 - ((shape.inner - 70) / 122 + 1) * 0.07)}
            role="button"
            tabIndex={0}
            data-path={shape.node.path}
            data-start={shape.start}
            data-end={shape.end}
            data-inner={shape.inner}
            data-outer={shape.outer}
            aria-label={`${shape.node.path}: ${shape.tokens.toLocaleString()} tokens`}
            onMouseEnter={() => inspect(shape)}
            onFocus={() => inspect(shape)}
            onPointerDown={() => inspect(shape)}
            onClick={() => onSelect(shape.node)}
            onKeyDown={(event) => {
              if (event.key === "Enter" || event.key === " ") {
                event.preventDefault();
                inspect(shape);
                onSelect(shape.node);
              }
            }}
          />
        ))}
        <circle cx="200" cy="200" r="66" className="sunburst-center" />
        <text x="200" y="196" textAnchor="middle" className="sunburst-total">
          {total.toLocaleString()}
        </text>
        <text x="200" y="218" textAnchor="middle">
          context tokens
        </text>
      </svg>
      <output
        className="sunburst-tooltip"
        aria-label="Context segment details"
        aria-live="polite"
        style={{
          color: "var(--muted)",
          display: "block",
          fontSize: 11,
          minHeight: 18,
          textAlign: "center",
        }}
      >
        {tooltip || "Hover, focus, or tap a segment to inspect it."}
      </output>
    </div>
  );
}

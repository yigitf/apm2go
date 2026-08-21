import { useMemo, useState } from "react";
import type { Span } from "../api/types";
import { formatDuration, seriesColor, truncate } from "../format";

/** A span placed in the tree, with the depth needed to indent it. */
interface Node {
  span: Span;
  depth: number;
  children: Node[];
}

/**
 * Builds the span tree.
 *
 * Traces arrive incomplete more often than one might hope — a parent can be
 * sampled away, arrive later, or be dropped by a rate limit — so any span whose
 * parent is missing is promoted to a root rather than discarded. Losing a span
 * from the view because its parent never arrived is the worst possible failure
 * for a debugging tool.
 */
function buildTree(spans: Span[]): Node[] {
  const byId = new Map<string, Node>();
  for (const span of spans) {
    byId.set(span.span_id, { span, depth: 0, children: [] });
  }

  const roots: Node[] = [];
  for (const node of byId.values()) {
    const parent = node.span.parent_span_id ? byId.get(node.span.parent_span_id) : undefined;
    if (parent && parent !== node) {
      parent.children.push(node);
    } else {
      roots.push(node);
    }
  }

  // Siblings run in the order they started, which is how the eye reads a
  // waterfall.
  const sortByStart = (nodes: Node[]) => {
    nodes.sort((a, b) => a.span.timestamp.localeCompare(b.span.timestamp));
    nodes.forEach((n) => sortByStart(n.children));
  };
  sortByStart(roots);

  const flat: Node[] = [];
  const walk = (nodes: Node[], depth: number) => {
    for (const node of nodes) {
      node.depth = depth;
      flat.push(node);
      walk(node.children, depth + 1);
    }
  };
  walk(roots, 0);
  return flat;
}

/** Names the span's kind for the row label. */
function spanKindLabel(kind: number): string {
  return ["", "internal", "server", "client", "producer", "consumer"][kind] ?? "";
}

/**
 * Labels a waterfall row with the most identifying thing about the span.
 *
 * The span name alone is not enough: OpenTelemetry names HTTP spans after the
 * method, so a trace crossing four services renders as a dozen rows all reading
 * "GET". The route or the SQL statement is what actually distinguishes them,
 * and is what an operator is scanning the waterfall for.
 */
function spanLabel(span: Span): string {
  if (span.http_route) {
    return span.http_method ? `${span.http_method} ${span.http_route}` : span.http_route;
  }
  if (span.db_statement) {
    // A statement can be long; the leading clause carries the identity.
    return truncate(span.db_statement.replace(/\s+/g, " ").trim(), 60);
  }
  return span.operation;
}

/**
 * The trace waterfall: one row per span, positioned and sized by time.
 *
 * The bar is the primary encoding — where work happened and how long it took.
 * Colour identifies the service, so a trace crossing three services is legible
 * at a glance, and an error is marked with both colour and an explicit label.
 */
export function Waterfall({
  spans,
  traceStart,
  traceDuration,
  onSelect,
  selectedId,
}: {
  spans: Span[];
  traceStart: string;
  traceDuration: number;
  onSelect: (span: Span) => void;
  selectedId?: string;
}) {
  const [hovered, setHovered] = useState<string | null>(null);
  const nodes = useMemo(() => buildTree(spans), [spans]);
  const startMs = new Date(traceStart).getTime();
  // A zero-length trace would divide by zero; one nanosecond is harmless.
  const total = Math.max(traceDuration, 1);

  return (
    <div className="divide-y" style={{ borderColor: "var(--border)" }}>
      {nodes.map((node) => {
        const span = node.span;
        const offsetNs = Math.max(0, (new Date(span.timestamp).getTime() - startMs) * 1e6);
        const left = (offsetNs / total) * 100;
        // Very short spans would render as invisible slivers; a floor keeps
        // them clickable without distorting the ones that matter.
        const width = Math.max((span.duration / total) * 100, 0.4);
        const isError = span.status === 2 || (span.http_status ?? 0) >= 500;
        const color = isError ? "var(--status-critical)" : seriesColor(span.service);
        const isSelected = selectedId === span.span_id;

        return (
          <button
            key={span.span_id}
            type="button"
            onClick={() => onSelect(span)}
            onMouseEnter={() => setHovered(span.span_id)}
            onMouseLeave={() => setHovered(null)}
            className="grid w-full grid-cols-[minmax(200px,1fr)_2.5fr] items-center gap-3 px-3 py-1.5 text-left"
            style={{
              background: isSelected
                ? "color-mix(in srgb, var(--series-1) 10%, transparent)"
                : hovered === span.span_id
                  ? "var(--hover-wash)"
                  : "transparent",
            }}
          >
            <div className="flex min-w-0 items-center gap-2" style={{ paddingLeft: node.depth * 12 }}>
              <span
                className="inline-block h-2.5 w-2.5 shrink-0 rounded-sm"
                style={{ background: color }}
                aria-hidden
              />
              <span className="truncate text-[12px]" title={spanLabel(span)}>
                {truncate(spanLabel(span), 60)}
              </span>
              {isError && (
                <span
                  className="shrink-0 rounded px-1 text-[10px] font-semibold"
                  style={{
                    color: "var(--status-critical)",
                    background: "color-mix(in srgb, var(--status-critical) 14%, transparent)",
                  }}
                >
                  error
                </span>
              )}
            </div>

            <div className="relative h-5">
              <div
                className="absolute top-1/2 h-2.5 -translate-y-1/2 rounded-sm"
                style={{
                  left: `${left}%`,
                  width: `${width}%`,
                  background: color,
                  // A surface ring keeps adjacent bars from merging visually.
                  boxShadow: "0 0 0 2px var(--surface-1)",
                }}
              />
              <span
                className="tabular absolute top-1/2 -translate-y-1/2 pl-1.5 text-[11px] whitespace-nowrap"
                style={{
                  left: `${Math.min(left + width, 88)}%`,
                  color: "var(--text-muted)",
                }}
              >
                {formatDuration(span.duration)}
              </span>
            </div>
          </button>
        );
      })}
    </div>
  );
}

export { spanKindLabel };

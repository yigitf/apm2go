import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router-dom";
import { api } from "../api/client";
import type { Dependency } from "../api/types";
import { useTimeRange } from "../components/TimeRange";
import { Card, EmptyState, ErrorState, Loading, Table, Td, Th } from "../components/primitives";
import { RuntimeBadge, runtimeBadge, useServiceRuntimes } from "../components/RuntimeBadge";
import { formatCount, formatDuration, formatPercent, seriesColor } from "../format";

interface Placed {
  name: string;
  x: number;
  y: number;
  /** Depth from an entry point, which decides the column a node sits in. */
  tier: number;
}

const NODE_RADIUS = 26;
const TIER_GAP = 210;
const ROW_GAP = 92;

/**
 * Lays the graph out in tiers rather than with a force simulation.
 *
 * A call graph is almost always shallow and directed — an entry point calling
 * a few services, which call a few more. Placing callers left of callees makes
 * the direction readable without arrows having to be traced, and unlike a force
 * layout the result is stable: the same data always draws the same picture.
 */
function layout(dependencies: Dependency[]): { nodes: Placed[]; width: number; height: number } {
  const names = new Set<string>();
  for (const dep of dependencies) {
    names.add(dep.caller);
    names.add(dep.callee);
  }

  const callees = new Set(dependencies.map((d) => d.callee));
  const tiers = new Map<string, number>();

  // Anything nobody calls is an entry point and starts at tier zero.
  const queue: string[] = [];
  for (const name of names) {
    if (!callees.has(name)) {
      tiers.set(name, 0);
      queue.push(name);
    }
  }
  // A graph that is entirely cyclic has no entry point; start somewhere.
  if (queue.length === 0 && names.size > 0) {
    const first = [...names][0];
    tiers.set(first, 0);
    queue.push(first);
  }

  while (queue.length > 0) {
    const current = queue.shift()!;
    const tier = tiers.get(current) ?? 0;
    for (const dep of dependencies) {
      if (dep.caller !== current) continue;
      const existing = tiers.get(dep.callee);
      if (existing === undefined || existing <= tier) {
        tiers.set(dep.callee, tier + 1);
        queue.push(dep.callee);
      }
    }
  }

  const byTier = new Map<number, string[]>();
  for (const name of names) {
    const tier = tiers.get(name) ?? 0;
    const list = byTier.get(tier) ?? [];
    list.push(name);
    byTier.set(tier, list);
  }

  const nodes: Placed[] = [];
  let maxRows = 1;
  for (const [tier, members] of byTier) {
    members.sort();
    maxRows = Math.max(maxRows, members.length);
    members.forEach((name, index) => {
      nodes.push({
        name,
        tier,
        x: 80 + tier * TIER_GAP,
        y: 60 + index * ROW_GAP,
      });
    });
  }

  return {
    nodes,
    width: 160 + Math.max(byTier.size, 1) * TIER_GAP,
    height: 60 + maxRows * ROW_GAP,
  };
}

/** How services call each other, drawn and listed. */
export function ServiceMap() {
  const { range, tick } = useTimeRange();

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ["dependencies", range, tick],
    queryFn: () => api.dependencies(range),
    refetchInterval: 15_000,
  });

  const runtimes = useServiceRuntimes();
  const dependencies = data?.dependencies ?? [];
  const placement = useMemo(() => layout(dependencies), [dependencies]);
  const positions = useMemo(
    () => new Map(placement.nodes.map((n) => [n.name, n])),
    [placement],
  );

  if (isError) return <ErrorState error={error} />;
  if (isLoading) return <Loading rows={6} />;

  if (dependencies.length === 0) {
    return (
      <Card>
        <EmptyState
          title="No service-to-service calls in this range"
          hint="An edge is drawn when one instrumented service calls another. A single service, or calls to uninstrumented systems, will not appear here."
        />
      </Card>
    );
  }

  const maxCalls = Math.max(...dependencies.map((d) => d.call_count));

  return (
    <div className="space-y-3">
      <Card title="Service map" subtitle="callers on the left, callees on the right">
        <div className="overflow-x-auto px-4 pb-4">
          <svg
            viewBox={`0 0 ${placement.width} ${placement.height}`}
            style={{ width: placement.width, maxWidth: "100%", height: placement.height }}
            role="img"
            aria-label="Service dependency graph"
          >
            <defs>
              <marker
                id="arrow"
                viewBox="0 0 10 10"
                refX="9"
                refY="5"
                markerWidth="6"
                markerHeight="6"
                orient="auto-start-reverse"
              >
                <path d="M0,0 L10,5 L0,10 z" fill="var(--baseline)" />
              </marker>
            </defs>

            {dependencies.map((dep) => {
              const from = positions.get(dep.caller);
              const to = positions.get(dep.callee);
              if (!from || !to) return null;

              // Edge weight encodes call volume; colour encodes health, and the
              // exact numbers are in the table below so nothing depends on
              // reading a stroke width precisely.
              const weight = 1 + (dep.call_count / maxCalls) * 3;
              const failing = dep.error_rate > 0.01;

              return (
                <g key={`${dep.caller}->${dep.callee}`}>
                  <line
                    x1={from.x + NODE_RADIUS}
                    y1={from.y}
                    x2={to.x - NODE_RADIUS - 4}
                    y2={to.y}
                    stroke={failing ? "var(--status-critical)" : "var(--baseline)"}
                    strokeWidth={weight}
                    markerEnd="url(#arrow)"
                    opacity={failing ? 0.9 : 0.6}
                  />
                </g>
              );
            })}

            {placement.nodes.map((node) => (
              <g key={node.name}>
                <circle
                  cx={node.x}
                  cy={node.y}
                  r={NODE_RADIUS}
                  fill={seriesColor(node.name)}
                  stroke="var(--surface-1)"
                  strokeWidth="2"
                />
                {/* The language badge rides on the node's shoulder rather than
                    replacing its fill: the fill is the service's categorical
                    colour, which is what ties this node to the same service
                    everywhere else. */}
                <image
                  href={runtimeBadge(runtimes.get(node.name)).src}
                  x={node.x + NODE_RADIUS - 8}
                  y={node.y - NODE_RADIUS - 8}
                  width="18"
                  height="18"
                >
                  <title>{runtimeBadge(runtimes.get(node.name)).label}</title>
                </image>
                <text
                  x={node.x}
                  y={node.y + NODE_RADIUS + 16}
                  textAnchor="middle"
                  fontSize="12"
                  fill="var(--text-primary)"
                >
                  {node.name.length > 22 ? `${node.name.slice(0, 21)}…` : node.name}
                </text>
              </g>
            ))}
          </svg>
        </div>
      </Card>

      <Card title="Calls" subtitle="every edge, with its numbers">
        <Table
          head={
            <>
              <Th>Caller</Th>
              <Th>Callee</Th>
              <Th align="right">Calls</Th>
              <Th align="right">Errors</Th>
              <Th align="right">Avg latency</Th>
            </>
          }
        >
          {dependencies.map((dep) => (
            <tr key={`${dep.caller}->${dep.callee}`} className="hover:bg-[var(--hover-wash)]">
              <Td>
                <Link
                  to={`/services/${encodeURIComponent(dep.caller)}`}
                  className="flex items-center gap-2 hover:underline"
                >
                  <RuntimeBadge runtime={runtimes.get(dep.caller)} />
                  {dep.caller}
                </Link>
              </Td>
              <Td>
                <Link
                  to={`/services/${encodeURIComponent(dep.callee)}`}
                  className="flex items-center gap-2 hover:underline"
                >
                  <RuntimeBadge runtime={runtimes.get(dep.callee)} />
                  {dep.callee}
                </Link>
              </Td>
              <Td align="right">{formatCount(dep.call_count)}</Td>
              <Td align="right">
                <span
                  style={{
                    color: dep.error_rate > 0.01 ? "var(--status-critical)" : "var(--text-primary)",
                  }}
                >
                  {formatPercent(dep.error_rate)}
                </span>
              </Td>
              <Td align="right">{formatDuration(dep.avg_latency_ns)}</Td>
            </tr>
          ))}
        </Table>
      </Card>
    </div>
  );
}

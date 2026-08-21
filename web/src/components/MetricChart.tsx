import { useMemo, useState } from "react";
import type { MetricSeries } from "../api/types";
import { formatTime } from "../format";
import {
  formatAxisTime,
  nearestIndex,
  nominalStep,
  resolveWindow,
  segments,
  timeTicks,
} from "./timeaxis";

const PADDING = { top: 12, right: 12, bottom: 26, left: 52 };

/**
 * Formats a value using the unit the instrument declared.
 *
 * Reading the unit rather than guessing from the name is what keeps bytes from
 * being rendered as a bare number with seven digits, and a ratio from being
 * shown as 0.734 where 73% is meant.
 */
function formatValue(value: number, unit?: string): string {
  switch (unit) {
    case "By": {
      if (value < 1024) return `${Math.round(value)} B`;
      const units = ["KB", "MB", "GB", "TB"];
      const exponent = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length);
      return `${(value / 1024 ** exponent).toFixed(1)} ${units[exponent - 1]}`;
    }
    case "1":
      // A dimensionless ratio is a fraction of one, which reads as a percentage.
      return `${(value * 100).toFixed(value < 0.1 ? 1 : 0)}%`;
    case "s":
      return value < 1 ? `${(value * 1000).toFixed(1)}ms` : `${value.toFixed(2)}s`;
    case "ms":
      return `${value.toFixed(value < 10 ? 1 : 0)}ms`;
    default:
      if (Math.abs(value) >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`;
      if (Math.abs(value) >= 1_000) return `${(value / 1_000).toFixed(1)}k`;
      return value < 10 ? value.toFixed(2) : Math.round(value).toLocaleString();
  }
}

/**
 * Names a series by its labels, since they are what distinguishes it.
 *
 * The keys are kept, not just the values: thread series are distinguished by
 * `jvm.thread.daemon` and `jvm.thread.state`, and rendering only the values
 * gives "false · runnable", which says nothing. The instrument's own prefix is
 * dropped because it is the same on every series and only costs width.
 */
function seriesLabel(series: MetricSeries, index: number): string {
  const labels = series.labels ?? {};
  const entries = Object.entries(labels);
  if (entries.length === 0) {
    return (series.points?.length ?? 0) > 0 ? series.name : `series ${index + 1}`;
  }

  return entries
    .map(([key, value]) => {
      const short = key.split(".").pop() ?? key;
      // A value that already names itself needs no key: "G1 Eden Space" reads
      // better than "name=G1 Eden Space".
      return /^[a-z_]+$/.test(String(value)) || value === "true" || value === "false"
        ? `${short}=${value}`
        : value;
    })
    .join(" · ");
}

/**
 * A multi-series metric chart with a crosshair.
 *
 * Several series share one plot only when they share a unit and a meaning —
 * memory pools, disk directions — which is exactly when comparing them is the
 * point. Series colours come from the fixed categorical order, so a given
 * series keeps its colour as the set changes.
 *
 * Every series is positioned by its own timestamps on one axis spanning the
 * window the server resolved. Two series of an instrument need not report the
 * same buckets — a memory pool that only exists after a GC promotes into it is
 * ordinary — and plotting each by array index silently slid the shorter one
 * left until it lined up with nothing.
 */
export function MetricChart({
  series,
  from,
  to,
  height = 150,
}: {
  series: MetricSeries[];
  from?: string;
  to?: string;
  height?: number;
}) {
  const [hover, setHover] = useState<number | null>(null);
  const width = 800;

  const geometry = useMemo(() => {
    // A series with zero points in range comes back with points: null, not
    // []: Go's encoding/json serialises a nil slice that way, and a series
    // that exists in the instrument list but has nothing yet in this narrow
    // a window — a service seconds after its first request — is exactly when
    // that happens. Normalising it away here, once, is what lets every line
    // below treat `points` as the plain array it almost always already is.
    const withPoints = series
      .map((s) => ({
        ...s,
        points: (s.points ?? []).map((p) => ({ ...p, at: Date.parse(p.timestamp) })),
      }))
      .filter((s) => s.points.length > 0);

    const allTimes = withPoints.flatMap((s) => s.points.map((p) => p.at));
    const window = resolveWindow(from, to, allTimes);
    if (!window) return null;

    // Every series shares an axis, so the scale spans all of them.
    const allValues = withPoints.flatMap((s) => s.points.map((p) => p.value));
    const max = Math.max(...allValues, Number.EPSILON) * 1.15;

    const plotWidth = width - PADDING.left - PADDING.right;
    const plotHeight = height - PADDING.top - PADDING.bottom;
    const span = window.end - window.start;

    const x = (at: number) => PADDING.left + ((at - window.start) / span) * plotWidth;
    const y = (v: number) => PADDING.top + plotHeight - (v / max) * plotHeight;

    const step = nominalStep(allTimes, window);

    // A run of consecutive buckets is one path; a run of one is a dot, since a
    // single M command draws nothing at all.
    const drawn = withPoints.map((s) => {
      const paths: string[] = [];
      const dots: { x: number; y: number }[] = [];
      for (const run of segments(s.points, (p) => p.at, step)) {
        if (run.length === 1) {
          dots.push({ x: x(run[0].at), y: y(run[0].value) });
          continue;
        }
        paths.push(run.map((p, i) => `${i === 0 ? "M" : "L"}${x(p.at)},${y(p.value)}`).join(" "));
      }
      return { paths, dots };
    });

    const ticks = [0, 0.25, 0.5, 0.75, 1].map((f) => ({ value: max * f, y: y(max * f) }));
    const xTicks = timeTicks(window).map((at) => ({ at, x: x(at), label: formatAxisTime(at, span) }));

    // The crosshair snaps to the union of every series' timestamps, so hovering
    // reads one instant across all of them rather than one series' own index.
    const timeline = [...new Set(allTimes)].sort((a, b) => a - b);

    return { withPoints, drawn, ticks, xTicks, x, y, plotWidth, plotHeight, timeline, step, window };
  }, [series, height, from, to]);

  if (!geometry || geometry.withPoints.length === 0) {
    return (
      <div
        className="flex items-center justify-center text-[12px]"
        style={{ height, color: "var(--text-muted)" }}
      >
        No data in this range
      </div>
    );
  }

  const unit = geometry.withPoints[0].unit;
  const hoveredAt = hover !== null ? geometry.timeline[hover] : null;

  return (
    <div>
      <div className="relative">
        <svg
          viewBox={`0 0 ${width} ${height}`}
          preserveAspectRatio="none"
          className="w-full"
          style={{ height }}
          role="img"
          aria-label={`${geometry.withPoints[0].name} over time`}
          onMouseLeave={() => setHover(null)}
          onMouseMove={(event) => {
            const rect = event.currentTarget.getBoundingClientRect();
            const ratio = (event.clientX - rect.left) / rect.width;
            const plotRatio = (ratio * width - PADDING.left) / geometry.plotWidth;
            const at =
              geometry.window.start + plotRatio * (geometry.window.end - geometry.window.start);
            setHover(nearestIndex(geometry.timeline, at, geometry.step * 0.75));
          }}
        >
          {geometry.ticks.map((tick, i) => (
            <g key={i}>
              <line
                x1={PADDING.left}
                x2={width - PADDING.right}
                y1={tick.y}
                y2={tick.y}
                stroke="var(--gridline)"
                strokeWidth="1"
              />
              <text
                x={PADDING.left - 6}
                y={tick.y + 3}
                textAnchor="end"
                fontSize="10"
                fill="var(--text-muted)"
                className="tabular"
              >
                {i === 0 ? "0" : formatValue(tick.value, unit)}
              </text>
            </g>
          ))}

          {geometry.drawn.map((s, i) => (
            <g key={i}>
              {s.paths.map((path, j) => (
                <path
                  key={j}
                  d={path}
                  fill="none"
                  stroke={`var(--series-${(i % 7) + 1})`}
                  strokeWidth="2"
                  strokeLinejoin="round"
                  strokeLinecap="round"
                  vectorEffect="non-scaling-stroke"
                />
              ))}
              {s.dots.map((dot, j) => (
                <circle key={j} cx={dot.x} cy={dot.y} r="2.5" fill={`var(--series-${(i % 7) + 1})`} />
              ))}
            </g>
          ))}

          {hoveredAt !== null && (
            <line
              x1={geometry.x(hoveredAt)}
              x2={geometry.x(hoveredAt)}
              y1={PADDING.top}
              y2={PADDING.top + geometry.plotHeight}
              stroke="var(--text-muted)"
              strokeWidth="1"
              strokeDasharray="3 3"
            />
          )}

          <line
            x1={PADDING.left}
            x2={width - PADDING.right}
            y1={PADDING.top + geometry.plotHeight}
            y2={PADDING.top + geometry.plotHeight}
            stroke="var(--baseline)"
            strokeWidth="1"
          />

          {/* Only the marks live in the viewBox; the labels are HTML below, so
              preserveAspectRatio="none" cannot stretch them. */}
          {geometry.xTicks.map((tick) => (
            <line
              key={tick.at}
              x1={tick.x}
              x2={tick.x}
              y1={PADDING.top + geometry.plotHeight}
              y2={PADDING.top + geometry.plotHeight + 3}
              stroke="var(--baseline)"
              strokeWidth="1"
            />
          ))}
        </svg>

        <div className="pointer-events-none absolute inset-x-0" style={{ bottom: 2, height: 12 }}>
          {geometry.xTicks.map((tick) => (
            <span
              key={tick.at}
              className="absolute text-[10px] whitespace-nowrap"
              style={{
                left: `${(tick.x / width) * 100}%`,
                transform: "translateX(-50%)",
                color: "var(--text-muted)",
              }}
            >
              {tick.label}
            </span>
          ))}
        </div>

        {hoveredAt !== null && (
          <div
            className="pointer-events-none absolute top-1 rounded-md px-2 py-1.5 text-[11px] shadow-sm"
            style={{
              background: "var(--surface-raised)",
              border: "1px solid var(--border-strong)",
              left: `${(geometry.x(hoveredAt) / width) * 100}%`,
              transform: "translateX(-50%)",
            }}
          >
            <div className="tabular" style={{ color: "var(--text-muted)" }}>
              {formatTime(new Date(hoveredAt).toISOString())}
            </div>
            {geometry.withPoints.map((s, i) => {
              // Each series is read at the hovered instant by its own
              // timestamps: one that skipped this bucket reports no value
              // rather than borrowing whichever of its points shares an index.
              const index = nearestIndex(
                s.points.map((p) => p.at),
                hoveredAt,
                geometry.step * 0.75,
              );
              return (
                <div key={i} className="mt-0.5 flex items-center gap-1.5 whitespace-nowrap">
                  <span
                    className="inline-block h-2 w-2 rounded-full"
                    style={{ background: `var(--series-${(i % 7) + 1})` }}
                  />
                  <span className="tabular font-medium">
                    {index !== null ? formatValue(s.points[index].value, s.unit) : "—"}
                  </span>
                </div>
              );
            })}
          </div>
        )}
      </div>

      {/* Identity is never carried by colour alone: more than one series always
          gets a legend naming what each line is. */}
      {geometry.withPoints.length > 1 && (
        <div className="flex flex-wrap gap-x-3 gap-y-1 px-3 pb-2">
          {geometry.withPoints.map((s, i) => (
            <span key={i} className="flex items-center gap-1.5 text-[11px]">
              <span
                className="inline-block h-2 w-2 rounded-sm"
                style={{ background: `var(--series-${(i % 7) + 1})` }}
                aria-hidden
              />
              <span style={{ color: "var(--text-secondary)" }}>{seriesLabel(s, i)}</span>
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

export { formatValue };

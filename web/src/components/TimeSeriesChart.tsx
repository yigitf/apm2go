import { useMemo, useState } from "react";
import type { TimeSeriesPoint } from "../api/types";
import { formatDuration, formatRate, formatTime } from "../format";
import {
  formatAxisTime,
  nearestIndex,
  nominalStep,
  resolveWindow,
  segments,
  timeTicks,
} from "./timeaxis";

/** One plotted measure. */
interface Series {
  key: "rate" | "p95" | "errors";
  label: string;
  color: string;
  value: (p: TimeSeriesPoint) => number;
  format: (v: number) => string;
}

const PADDING = { top: 12, right: 12, bottom: 26, left: 46 };

/**
 * A time series line chart with a crosshair and tooltip.
 *
 * Only one measure is plotted per chart. Throughput and latency have unrelated
 * scales, and putting them on two y-axes is the single most misleading thing a
 * monitoring chart can do — a crossing point that means nothing reads as an
 * event. Stacking two small charts costs a little vertical space and lies about
 * nothing.
 *
 * `from` and `to` are the window the server resolved, and the x axis spans
 * exactly that rather than the extent of the data. It is the difference between
 * a chart that answers "what happened in the last hour" and one that answers
 * "what happened between the first and last measurement, whenever those were" —
 * the second draws an identical picture at every range setting.
 */
export function TimeSeriesChart({
  points,
  measure,
  from,
  to,
  height = 140,
}: {
  points: TimeSeriesPoint[];
  measure: "rate" | "p95" | "errors";
  from?: string;
  to?: string;
  height?: number;
}) {
  const [hover, setHover] = useState<number | null>(null);
  const width = 800; // viewBox width; the SVG scales to its container.

  const series: Series = useMemo(() => {
    switch (measure) {
      case "p95":
        return {
          key: "p95",
          label: "p95 latency",
          color: "var(--series-2)",
          value: (p) => p.p95_latency_ns,
          format: formatDuration,
        };
      case "errors":
        return {
          key: "errors",
          label: "errors",
          color: "var(--status-critical)",
          value: (p) => p.error_count,
          format: (v) => String(Math.round(v)),
        };
      default:
        return {
          key: "rate",
          label: "throughput",
          color: "var(--series-1)",
          value: (p) => p.rate,
          format: formatRate,
        };
    }
  }, [measure]);

  const geometry = useMemo(() => {
    const times = points.map((p) => Date.parse(p.timestamp));
    const window = resolveWindow(from, to, times);
    if (!window) return null;

    const values = points.map(series.value);
    // The y-axis always starts at zero: a truncated axis exaggerates every
    // wobble, which on a latency chart reads as an incident that is not there.
    const max = Math.max(...values, Number.EPSILON) * 1.15;

    const plotWidth = width - PADDING.left - PADDING.right;
    const plotHeight = height - PADDING.top - PADDING.bottom;
    const span = window.end - window.start;

    const x = (at: number) => PADDING.left + ((at - window.start) / span) * plotWidth;
    const y = (v: number) => PADDING.top + plotHeight - (v / max) * plotHeight;

    const step = nominalStep(times, window);
    const indexed = points.map((point, index) => ({ point, index, at: times[index] }));
    const runs = segments(indexed, (p) => p.at, step);

    // Each run of consecutive buckets is its own path, so an interval where the
    // service reported nothing is a gap rather than a line drawn through it.
    // A run of one has no second coordinate to draw a line to and would render
    // as nothing at all, so it becomes a dot instead.
    const lines: string[] = [];
    const areas: string[] = [];
    const dots: { x: number; y: number }[] = [];
    const baseline = PADDING.top + plotHeight;

    for (const run of runs) {
      if (run.length === 1) {
        dots.push({ x: x(run[0].at), y: y(values[run[0].index]) });
        continue;
      }
      const line = run
        .map((p, i) => `${i === 0 ? "M" : "L"}${x(p.at)},${y(values[p.index])}`)
        .join(" ");
      lines.push(line);
      areas.push(`${line} L${x(run[run.length - 1].at)},${baseline} L${x(run[0].at)},${baseline} Z`);
    }

    // Four gridlines is enough to read a value off; more turns into texture.
    const ticks = [0, 0.25, 0.5, 0.75, 1].map((f) => ({ value: max * f, y: y(max * f) }));
    const xTicks = timeTicks(window).map((at) => ({ at, x: x(at), label: formatAxisTime(at, span) }));

    return { x, y, lines, areas, dots, ticks, xTicks, plotWidth, plotHeight, max, times, step, window };
  }, [points, series, height, from, to]);

  if (!geometry) {
    return (
      <div
        className="flex items-center justify-center text-[12px]"
        style={{ height, color: "var(--text-muted)" }}
      >
        No data in this range
      </div>
    );
  }

  const empty = points.length === 0;
  const hovered = hover !== null ? points[hover] : null;
  const gradientId = `grad-${series.key}`;

  return (
    <div className="relative">
      <svg
        viewBox={`0 0 ${width} ${height}`}
        preserveAspectRatio="none"
        className="w-full"
        style={{ height }}
        role="img"
        aria-label={`${series.label} over time`}
        onMouseLeave={() => setHover(null)}
        onMouseMove={(event) => {
          const rect = event.currentTarget.getBoundingClientRect();
          const ratio = (event.clientX - rect.left) / rect.width;
          const plotRatio = (ratio * width - PADDING.left) / geometry.plotWidth;
          const at =
            geometry.window.start + plotRatio * (geometry.window.end - geometry.window.start);
          // Half a bucket either side: past that, the pointer is over a gap and
          // labelling it with a distant measurement would be a fabrication.
          setHover(nearestIndex(geometry.times, at, geometry.step * 0.75));
        }}
      >
        <defs>
          <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor={series.color} stopOpacity="0.18" />
            <stop offset="100%" stopColor={series.color} stopOpacity="0.01" />
          </linearGradient>
        </defs>

        {/* Gridlines sit behind the data and stay recessive. */}
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
              {i === 0 ? "0" : series.format(tick.value)}
            </text>
          </g>
        ))}

        {geometry.areas.map((area, i) => (
          <path key={i} d={area} fill={`url(#${gradientId})`} />
        ))}
        {geometry.lines.map((line, i) => (
          <path
            key={i}
            d={line}
            fill="none"
            stroke={series.color}
            strokeWidth="2"
            strokeLinejoin="round"
            strokeLinecap="round"
            vectorEffect="non-scaling-stroke"
          />
        ))}
        {geometry.dots.map((dot, i) => (
          <circle key={i} cx={dot.x} cy={dot.y} r="2.5" fill={series.color} />
        ))}

        {hover !== null && (
          <g>
            <line
              x1={geometry.x(geometry.times[hover])}
              x2={geometry.x(geometry.times[hover])}
              y1={PADDING.top}
              y2={PADDING.top + geometry.plotHeight}
              stroke="var(--text-muted)"
              strokeWidth="1"
              strokeDasharray="3 3"
            />
            {/* A surface-coloured ring keeps the marker legible over the line. */}
            <circle
              cx={geometry.x(geometry.times[hover])}
              cy={geometry.y(series.value(points[hover]))}
              r="4"
              fill={series.color}
              stroke="var(--surface-1)"
              strokeWidth="2"
            />
          </g>
        )}

        <line
          x1={PADDING.left}
          x2={width - PADDING.right}
          y1={PADDING.top + geometry.plotHeight}
          y2={PADDING.top + geometry.plotHeight}
          stroke="var(--baseline)"
          strokeWidth="1"
        />

        {/* Ticks on round clock values, so the axis reads as the window the
            picker asked for rather than as the extent of whatever arrived.
            Only the marks are drawn here: preserveAspectRatio="none" stretches
            the viewBox horizontally, which distorts text, so the labels are
            HTML below rather than <text> inside. */}
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

        {empty && (
          <text
            x={PADDING.left + geometry.plotWidth / 2}
            y={PADDING.top + geometry.plotHeight / 2}
            textAnchor="middle"
            fontSize="12"
            fill="var(--text-muted)"
          >
            No data in this range
          </text>
        )}
      </svg>

      {/* Axis labels, positioned as a fraction of the width so they track the
          same scale the plot uses without inheriting its horizontal stretch. */}
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

      {hovered && (
        <div
          className="pointer-events-none absolute top-1 rounded-md px-2 py-1.5 text-[11px] shadow-sm"
          style={{
            background: "var(--surface-raised)",
            border: "1px solid var(--border-strong)",
            left: `${(geometry.x(geometry.times[hover!]) / width) * 100}%`,
            transform: "translateX(-50%)",
          }}
        >
          <div className="tabular" style={{ color: "var(--text-muted)" }}>
            {formatTime(hovered.timestamp)}
          </div>
          <div className="mt-0.5 flex items-center gap-1.5">
            <span
              className="inline-block h-2 w-2 rounded-full"
              style={{ background: series.color }}
            />
            <span className="tabular font-medium">{series.format(series.value(hovered))}</span>
          </div>
        </div>
      )}
    </div>
  );
}

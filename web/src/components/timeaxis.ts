/**
 * The shared time axis every chart draws on.
 *
 * Charts used to plot by array index: the first point at the left edge, the
 * last at the right, whatever times they carried. That draws the same picture
 * for every range — a service that reported for ten minutes fills a 24-hour
 * chart edge to edge — so the range picker above the charts changed the numbers
 * and never the shape. Positioning by timestamp inside the window the server
 * actually resolved is what makes the picker mean something, and it is also
 * what makes a gap in the data look like a gap rather than a straight line
 * drawn across the outage.
 */

/** The window a chart covers, as epoch milliseconds. */
export interface TimeWindow {
  start: number;
  end: number;
}

/**
 * Resolves the window to draw.
 *
 * The bounds come from the response envelope rather than from the browser's
 * clock: the server resolved "-1h" against its own clock, and re-resolving it
 * here would shift every point by whatever the two clocks disagree about. The
 * data's own extent is only a fallback, for a caller that has no envelope yet.
 */
export function resolveWindow(from?: string, to?: string, timestamps?: number[]): TimeWindow | null {
  const start = from ? Date.parse(from) : NaN;
  const end = to ? Date.parse(to) : NaN;
  if (Number.isFinite(start) && Number.isFinite(end) && end > start) {
    return { start, end };
  }

  const times = (timestamps ?? []).filter(Number.isFinite);
  if (times.length === 0) return null;
  const min = Math.min(...times);
  const max = Math.max(...times);
  // A single measurement has no extent of its own; give it a minute of width
  // so the scale below has something to divide by.
  return min === max ? { start: min - 30_000, end: max + 30_000 } : { start: min, end: max };
}

/**
 * Estimates the bucket width the server used, from the points themselves.
 *
 * The step is not sent with the response, and it is needed to tell "no data
 * here" from "the next point is simply the next bucket". The *smallest* gap is
 * the bucket width — every larger gap is missing buckets, which is exactly what
 * this is used to detect — and the window divided by a plausible bucket count
 * covers the case of too few points to measure anything from.
 */
export function nominalStep(times: number[], window: TimeWindow): number {
  const sorted = [...times].sort((a, b) => a - b);
  let smallest = Infinity;
  for (let i = 1; i < sorted.length; i++) {
    const delta = sorted[i] - sorted[i - 1];
    if (delta > 0 && delta < smallest) smallest = delta;
  }
  if (Number.isFinite(smallest)) return smallest;
  // defaultStep on the server targets about 120 buckets across the range.
  return Math.max((window.end - window.start) / 120, 1000);
}

/**
 * Splits points into runs of consecutive buckets.
 *
 * A run is broken wherever more than one bucket is missing, so an interval in
 * which a service reported nothing is drawn as a gap. Interpolating across it
 * instead would invent a smooth line through the outage that is the single
 * thing an operator most needs to see.
 */
export function segments<T>(points: T[], time: (point: T) => number, step: number): T[][] {
  const runs: T[][] = [];
  let run: T[] = [];
  let previous: number | null = null;

  for (const point of points) {
    const at = time(point);
    if (!Number.isFinite(at)) continue;
    if (previous !== null && at - previous > step * 1.5) {
      if (run.length > 0) runs.push(run);
      run = [];
    }
    run.push(point);
    previous = at;
  }
  if (run.length > 0) runs.push(run);
  return runs;
}

/** Tick intervals a clock reads naturally, shortest first. */
const TICK_INTERVALS = [
  1_000, 5_000, 10_000, 15_000, 30_000,
  60_000, 2 * 60_000, 5 * 60_000, 10 * 60_000, 15 * 60_000, 30 * 60_000,
  3_600_000, 2 * 3_600_000, 3 * 3_600_000, 6 * 3_600_000, 12 * 3_600_000,
  86_400_000, 2 * 86_400_000, 7 * 86_400_000,
];

/**
 * Picks the times to label along the axis.
 *
 * Ticks land on round clock values — :00, :15, :30 — rather than at even
 * fractions of the window, because a label reading 14:37 is a number to decode
 * and one reading 14:30 is a time you already know. Alignment is done in local
 * time, which is what the labels are rendered in.
 */
export function timeTicks(window: TimeWindow, target = 5): number[] {
  const span = window.end - window.start;
  const interval =
    TICK_INTERVALS.find((candidate) => span / candidate <= target) ??
    TICK_INTERVALS[TICK_INTERVALS.length - 1];

  // getTimezoneOffset is minutes *behind* UTC, so subtracting it shifts an
  // epoch value into a local-time frame where flooring aligns to local clock
  // boundaries rather than UTC ones.
  const offset = new Date(window.start).getTimezoneOffset() * 60_000;
  const first = Math.ceil((window.start - offset) / interval) * interval + offset;

  const ticks: number[] = [];
  for (let t = first; t <= window.end; t += interval) ticks.push(t);
  return ticks;
}

/**
 * Renders an axis label at a precision the window justifies.
 *
 * Seconds only appear on windows short enough for them to differ, and the date
 * only once the window crosses a day — otherwise every label repeats the same
 * prefix and the axis costs width to say nothing.
 */
export function formatAxisTime(ms: number, span: number): string {
  const date = new Date(ms);
  if (span <= 10 * 60_000) {
    return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  }
  if (span <= 36 * 3_600_000) {
    return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
  }
  return date.toLocaleString([], { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

/**
 * Finds the point nearest a time, or null when nothing is near enough.
 *
 * The tolerance is what keeps a crosshair from labelling empty space with the
 * value of a measurement an hour away: hovering a gap should report no value,
 * the same way the line shows none there.
 */
export function nearestIndex(times: number[], at: number, tolerance: number): number | null {
  let best: number | null = null;
  let bestDistance = Infinity;
  for (let i = 0; i < times.length; i++) {
    const distance = Math.abs(times[i] - at);
    if (distance < bestDistance) {
      best = i;
      bestDistance = distance;
    }
  }
  return best !== null && bestDistance <= tolerance ? best : null;
}

/** Formatting helpers shared by every view. */

/**
 * Renders a nanosecond duration at a readable precision.
 *
 * Latency spans six orders of magnitude in one table, so the unit changes with
 * the value and the precision drops as the number grows: "1.24ms" and "3.2s"
 * both carry three significant figures, which is all anyone reads.
 */
export function formatDuration(nanos: number): string {
  if (!Number.isFinite(nanos) || nanos <= 0) return "0";

  const micros = nanos / 1e3;
  if (micros < 1) return `${nanos.toFixed(0)}ns`;
  if (micros < 1000) return `${micros.toFixed(micros < 10 ? 1 : 0)}µs`;

  const millis = micros / 1000;
  if (millis < 1000) return `${millis.toFixed(millis < 10 ? 2 : millis < 100 ? 1 : 0)}ms`;

  const seconds = millis / 1000;
  if (seconds < 60) return `${seconds.toFixed(seconds < 10 ? 2 : 1)}s`;

  const minutes = Math.floor(seconds / 60);
  return `${minutes}m ${(seconds % 60).toFixed(0)}s`;
}

/** Renders a throughput figure, keeping sub-1/s rates meaningful. */
export function formatRate(perSecond: number): string {
  if (!Number.isFinite(perSecond) || perSecond <= 0) return "0/s";
  if (perSecond < 0.01) return `${(perSecond * 60).toFixed(1)}/min`;
  if (perSecond < 1) return `${perSecond.toFixed(2)}/s`;
  if (perSecond < 100) return `${perSecond.toFixed(1)}/s`;
  return `${Math.round(perSecond).toLocaleString()}/s`;
}

/** Renders a ratio as a percentage, keeping small non-zero rates visible. */
export function formatPercent(ratio: number): string {
  if (!Number.isFinite(ratio) || ratio <= 0) return "0%";
  const percent = ratio * 100;
  if (percent < 0.1) return "<0.1%";
  if (percent < 10) return `${percent.toFixed(1)}%`;
  return `${percent.toFixed(0)}%`;
}

export function formatCount(n: number): string {
  if (!Number.isFinite(n)) return "0";
  if (n < 1000) return String(n);
  if (n < 1_000_000) return `${(n / 1000).toFixed(n < 10_000 ? 1 : 0)}k`;
  return `${(n / 1_000_000).toFixed(1)}M`;
}

export function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const exponent = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  return `${(bytes / 1024 ** exponent).toFixed(exponent === 0 ? 0 : 1)} ${units[exponent]}`;
}

/**
 * Go's zero time, as encoding/json renders it.
 *
 * A timestamp that was never set does not arrive as null or as an absent field
 * — it arrives as year 1, which is a perfectly valid date and formats without
 * complaint. A JVM that never attached showed "attached 739847d ago" on the
 * inventory page for exactly this reason. Every formatter below treats it as
 * "not set", and callers deciding whether to render a field at all should ask
 * isUnset first rather than testing the string for truthiness.
 */
const goZeroTime = "0001-01-01T00:00:00Z";

/** Reports whether a timestamp is absent or is Go's zero time. */
export function isUnset(iso?: string): boolean {
  return !iso || iso === goZeroTime || iso.startsWith("0001-01-01T");
}

/** Renders a timestamp as a clock time, which is what a trace list needs. */
export function formatTime(iso: string): string {
  if (isUnset(iso)) return "—";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

export function formatDateTime(iso: string): string {
  if (isUnset(iso)) return "—";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "—";
  return date.toLocaleString([], {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

/** Renders how long ago something happened, for "last seen" columns. */
export function formatRelative(iso?: string): string {
  // `!iso` as well as isUnset, so the compiler can narrow away the undefined.
  if (!iso || isUnset(iso)) return "—";
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "—";

  const seconds = Math.floor((Date.now() - then) / 1000);
  if (seconds < 0) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86400)}d ago`;
}

/** Renders an uptime in the coarse units an operator reads it in. */
export function formatUptime(seconds: number): string {
  if (seconds < 60) return `${Math.floor(seconds)}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`;
  return `${Math.floor(seconds / 86400)}d ${Math.floor((seconds % 86400) / 3600)}h`;
}

/**
 * Number of categorical slots services may use.
 *
 * Slot 8 is red, which is close enough to the critical status colour to be
 * confusable — a healthy service painted red reads as an incident. Errors are
 * the thing this tool exists to surface, so red is reserved for them and
 * services draw from the first seven slots.
 */
const SERVICE_SLOTS = 7;

/**
 * Assigns a stable categorical colour to a name.
 *
 * The slot is derived from the name itself rather than from its position in a
 * list, so filtering a chart down never repaints the series that remain — a
 * colour always means the same service.
 */
export function seriesColor(name: string): string {
  let hash = 0;
  for (let i = 0; i < name.length; i++) {
    hash = (hash * 31 + name.charCodeAt(i)) >>> 0;
  }
  return `var(--series-${(hash % SERVICE_SLOTS) + 1})`;
}

/** Truncates a long value for a table cell, keeping the informative head. */
export function truncate(value: string, max: number): string {
  return value.length <= max ? value : `${value.slice(0, max - 1)}…`;
}

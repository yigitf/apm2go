import type { ReactNode } from "react";
import { BrandMark } from "./Brand";

/** A titled container. Every chart and table on a page sits in one of these. */
export function Card({
  title,
  subtitle,
  action,
  children,
  className = "",
}: {
  title?: string;
  subtitle?: string;
  action?: ReactNode;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section className={`card overflow-hidden ${className}`}>
      {(title || action) && (
        <header className="flex items-baseline justify-between gap-4 px-4 pt-3.5 pb-2.5">
          <div className="min-w-0">
            {title && (
              <h2 className="truncate text-[13px] font-semibold tracking-[-0.01em]">{title}</h2>
            )}
            {subtitle && (
              <p className="mt-0.5 text-[12px]" style={{ color: "var(--text-muted)" }}>
                {subtitle}
              </p>
            )}
          </div>
          {action}
        </header>
      )}
      {children}
    </section>
  );
}

/** The four tones a figure can carry, and how each is drawn. */
const TONES = {
  neutral: { text: "var(--text-primary)", rail: "transparent" },
  good: { text: "var(--status-good-text)", rail: "var(--status-good)" },
  warning: { text: "var(--status-warning-text)", rail: "var(--status-warning)" },
  critical: { text: "var(--status-critical)", rail: "var(--status-critical)" },
} as const;

/**
 * A single headline number.
 *
 * A metric that stands alone is not a chart: one large figure with its label
 * reads faster than any plot of a single value.
 *
 * A tone is drawn twice — as the colour of the figure and as a rail down the
 * left edge — so that a tile in trouble is distinguishable from a healthy one
 * by shape as well as by hue. The rail is present at every tone, transparent
 * when neutral, so the figures stay on a common left margin and a row of
 * tiles reads as a row rather than as a ragged edge.
 */
export function StatTile({
  label,
  value,
  detail,
  tone = "neutral",
}: {
  label: string;
  value: string;
  detail?: string;
  tone?: keyof typeof TONES;
}) {
  const { text, rail } = TONES[tone];

  return (
    <div className="card relative overflow-hidden px-4 py-3.5 pl-[17px]">
      <span
        aria-hidden="true"
        className="absolute top-0 bottom-0 left-0 w-[3px]"
        style={{ background: rail }}
      />
      <div className="eyebrow">{label}</div>
      <div
        className="tabular mt-1.5 text-[28px] leading-none font-semibold tracking-[-0.02em]"
        style={{ color: text }}
      >
        {value}
      </div>
      {detail && (
        <div className="mt-1.5 text-[12px]" style={{ color: "var(--text-muted)" }}>
          {detail}
        </div>
      )}
    </div>
  );
}

/**
 * A state pill. State is always carried by the label, never by colour alone —
 * the dot and the colour only speed up scanning for someone who can already
 * read it.
 */
export function StateBadge({ state, title }: { state: string; title?: string }) {
  const palette: Record<string, { fg: string; dot: string }> = {
    attached: { fg: "var(--status-good-text)", dot: "var(--status-good)" },
    attaching: { fg: "var(--series-1)", dot: "var(--series-1)" },
    discovered: { fg: "var(--text-secondary)", dot: "var(--text-muted)" },
    pending: { fg: "var(--text-secondary)", dot: "var(--text-muted)" },
    failed: { fg: "var(--status-critical)", dot: "var(--status-critical)" },
    skipped: { fg: "var(--text-muted)", dot: "var(--baseline)" },
    exited: { fg: "var(--text-muted)", dot: "var(--baseline)" },
  };
  const colors = palette[state] ?? palette.discovered;

  return (
    <span
      title={title}
      className="inline-flex items-center gap-1.5 rounded-full py-0.5 pr-2.5 pl-2 text-[11px] font-medium"
      style={{
        color: colors.fg,
        background: `color-mix(in srgb, ${colors.dot} 12%, transparent)`,
        border: `1px solid color-mix(in srgb, ${colors.dot} 28%, transparent)`,
      }}
    >
      <span
        aria-hidden="true"
        className="h-[5px] w-[5px] shrink-0 rounded-full"
        style={{ background: colors.dot }}
      />
      {state}
    </span>
  );
}

/**
 * Shown when a query succeeds but has nothing to show.
 *
 * The mark appears here, faintly, and nowhere else in the page body. An empty
 * panel is the one place with room for it, and it turns the blankest state in
 * the product into the one that looks most deliberate.
 */
export function EmptyState({ title, hint }: { title: string; hint?: string }) {
  return (
    <div className="flex flex-col items-center px-4 py-10 text-center">
      <div className="opacity-25 grayscale">
        <BrandMark size={34} />
      </div>
      <p className="mt-3 text-[13px] font-medium" style={{ color: "var(--text-secondary)" }}>
        {title}
      </p>
      {hint && (
        <p className="mx-auto mt-1.5 max-w-md text-[12px]" style={{ color: "var(--text-muted)" }}>
          {hint}
        </p>
      )}
    </div>
  );
}

/**
 * Shown when a query fails, carrying the API's own message.
 *
 * Deliberately borderless: this is rendered both as a whole page and inside a
 * card that failed to fill, and a bordered panel would nest inside the second
 * of those as a box within a box.
 */
export function ErrorState({ error }: { error: unknown }) {
  const message = error instanceof Error ? error.message : String(error);
  return (
    <div className="px-4 py-8 text-center">
      <p className="text-[13px] font-semibold" style={{ color: "var(--status-critical)" }}>
        Could not load this view
      </p>
      <p className="mx-auto mt-1.5 max-w-md text-[12px]" style={{ color: "var(--text-secondary)" }}>
        {message}
      </p>
    </div>
  );
}

/** A low-key loading placeholder that holds the layout steady. */
export function Loading({ rows = 3 }: { rows?: number }) {
  return (
    <div className="space-y-2 px-4 py-4">
      {Array.from({ length: rows }, (_, i) => (
        <div
          key={i}
          className="h-4 animate-pulse rounded"
          style={{ background: "var(--hover-wash)", width: `${100 - i * 12}%` }}
        />
      ))}
    </div>
  );
}

/**
 * A dense table, the default way to show a list of measured things.
 *
 * The header stays put while the body scrolls: these tables are read by
 * comparing one row against a column heading, and a heading that scrolls away
 * turns a wide table into a guessing game about which figure is which.
 */
export function Table({ head, children }: { head: ReactNode; children: ReactNode }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full border-collapse text-[13px]">
        <thead
          className="sticky top-0 z-[1]"
          style={{ background: "var(--surface-1)" }}
        >
          <tr className="eyebrow text-left">{head}</tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

export function Th({
  children,
  align = "left",
}: {
  children: ReactNode;
  align?: "left" | "right";
}) {
  return (
    <th
      className={`px-3 py-2 font-semibold whitespace-nowrap ${align === "right" ? "text-right" : "text-left"}`}
      style={{ borderBottom: "1px solid var(--border)" }}
    >
      {children}
    </th>
  );
}

export function Td({
  children,
  align = "left",
  className = "",
}: {
  children: ReactNode;
  align?: "left" | "right";
  className?: string;
}) {
  return (
    <td
      className={`px-3 py-2.5 ${align === "right" ? "text-right tabular" : ""} ${className}`}
      style={{ borderBottom: "1px solid var(--border)" }}
    >
      {children}
    </td>
  );
}

import { createContext, useContext, useMemo, useState, type ReactNode } from "react";
import type { Range } from "../api/client";

/** The presets an operator actually reaches for, shortest first. */
const PRESETS = [
  { label: "5m", value: "-5m" },
  { label: "15m", value: "-15m" },
  { label: "1h", value: "-1h" },
  { label: "6h", value: "-6h" },
  { label: "24h", value: "-24h" },
  { label: "7d", value: "-168h" },
] as const;

interface TimeRangeContext {
  range: Range;
  preset: string;
  setPreset: (value: string) => void;
  /** Refresh key, bumped so queries refetch on demand. */
  tick: number;
  refresh: () => void;
}

const Context = createContext<TimeRangeContext | null>(null);

/**
 * Holds the selected time range for the whole app.
 *
 * The range lives above the pages so that moving between the overview, a
 * service and a trace keeps the window an operator is investigating, rather
 * than resetting it at every navigation.
 */
export function TimeRangeProvider({ children }: { children: ReactNode }) {
  const [preset, setPreset] = useState<string>("-1h");
  const [tick, setTick] = useState(0);

  const value = useMemo<TimeRangeContext>(
    () => ({
      // Relative bounds are sent as-is and resolved by the server, so the
      // window stays anchored to the server's clock rather than the browser's.
      range: { from: preset, to: "now" },
      preset,
      setPreset,
      tick,
      refresh: () => setTick((t) => t + 1),
    }),
    [preset, tick],
  );

  return <Context.Provider value={value}>{children}</Context.Provider>;
}

export function useTimeRange(): TimeRangeContext {
  const context = useContext(Context);
  if (!context) throw new Error("useTimeRange must be used inside a TimeRangeProvider");
  return context;
}

/** The range picker, a single row of presets above the content. */
export function TimeRangePicker() {
  const { preset, setPreset, refresh } = useTimeRange();

  return (
    <div className="flex items-center gap-1">
      <div
        className="flex overflow-hidden rounded-[var(--radius-control)]"
        style={{ border: "1px solid var(--border-strong)" }}
        role="group"
        aria-label="Time range"
      >
        {PRESETS.map((option) => {
          const active = option.value === preset;
          return (
            <button
              key={option.value}
              type="button"
              onClick={() => setPreset(option.value)}
              aria-pressed={active}
              className="tabular px-2.5 py-1 text-[12px] font-medium transition-colors"
              style={{
                // Filled with the ink colour, not with a series hue: the series
                // slots stand for entities being charted, and a control that
                // borrowed slot 1 would read as if it belonged to that entity.
                background: active ? "var(--text-primary)" : "transparent",
                color: active ? "var(--surface-1)" : "var(--text-secondary)",
              }}
            >
              {option.label}
            </button>
          );
        })}
      </div>
      <button
        type="button"
        onClick={refresh}
        title="Refresh"
        aria-label="Refresh"
        className="rounded-[var(--radius-control)] px-2 py-1 text-[12px] transition-colors"
        style={{ border: "1px solid var(--border-strong)", color: "var(--text-secondary)" }}
      >
        ↻
      </button>
    </div>
  );
}

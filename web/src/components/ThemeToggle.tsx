import type { ReactNode } from "react";
import { useTheme, type ThemeChoice } from "./ThemeProvider";

const OPTIONS: { value: ThemeChoice; label: string; icon: ReactNode }[] = [
  {
    value: "light",
    label: "Light",
    icon: (
      <>
        <circle cx="8" cy="8" r="3.25" />
        <path d="M8 1v1.6M8 13.4V15M15 8h-1.6M2.6 8H1M12.95 3.05l-1.13 1.13M4.18 11.82l-1.13 1.13M12.95 12.95l-1.13-1.13M4.18 4.18L3.05 3.05" />
      </>
    ),
  },
  {
    value: "system",
    label: "System",
    icon: (
      <>
        <rect x="1.75" y="2.75" width="12.5" height="8.5" rx="1.25" />
        <path d="M5.5 14h5" />
      </>
    ),
  },
  {
    value: "dark",
    label: "Dark",
    icon: <path d="M13.4 9.6A5.8 5.8 0 0 1 6.4 2.6a5.8 5.8 0 1 0 7 7z" />,
  },
];

/** Where the control is drawn, which decides which token set it may use. */
const SKINS = {
  // The rail keeps its own surface in both palettes, so the page's ink tokens
  // would paint navy on navy here.
  rail: {
    border: "var(--rail-border)",
    activeBg: "var(--rail-active)",
    activeFg: "var(--rail-text-active)",
    idleFg: "var(--rail-muted)",
  },
  page: {
    border: "var(--border-strong)",
    activeBg: "var(--text-primary)",
    activeFg: "var(--surface-1)",
    idleFg: "var(--text-muted)",
  },
} as const;

/**
 * Chooses between the light palette, the dark one, and whatever the OS says.
 *
 * A three-way control rather than a two-state switch, because following the
 * system is the default and a switch has nowhere to show it: an operator who
 * has never touched this needs to be able to see that, and to get back to it
 * after trying the other two.
 */
export function ThemeToggle({
  skin = "rail",
  stack = false,
  showLabels = false,
}: {
  skin?: keyof typeof SKINS;
  /** Stack vertically, for the rail when it is collapsed to icons. */
  stack?: boolean;
  showLabels?: boolean;
}) {
  const { choice, setChoice } = useTheme();
  const colors = SKINS[skin];

  return (
    <div
      className={`flex overflow-hidden rounded-[var(--radius-control)] ${
        stack ? "flex-col" : "flex-row"
      }`}
      style={{ border: `1px solid ${colors.border}` }}
      role="group"
      aria-label="Colour theme"
    >
      {OPTIONS.map((option) => {
        const active = option.value === choice;
        return (
          <button
            key={option.value}
            type="button"
            onClick={() => setChoice(option.value)}
            aria-pressed={active}
            title={`${option.label} theme`}
            className={`flex items-center justify-center gap-1.5 py-1 transition-colors ${
              showLabels ? "px-3 text-[12px] font-medium" : "px-2"
            }`}
            style={{
              background: active ? colors.activeBg : "transparent",
              color: active ? colors.activeFg : colors.idleFg,
            }}
          >
            <svg
              width="14"
              height="14"
              viewBox="0 0 16 16"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.4"
              strokeLinecap="round"
              strokeLinejoin="round"
              aria-hidden="true"
            >
              {option.icon}
            </svg>
            {showLabels ? option.label : <span className="sr-only">{option.label}</span>}
          </button>
        );
      })}
    </div>
  );
}

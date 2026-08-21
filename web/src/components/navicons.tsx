import type { ReactNode } from "react";

/**
 * The navigation icons.
 *
 * Drawn here rather than pulled from an icon set, because half of them have no
 * entry in one: a trace is a waterfall, a service map is a graph, and the
 * thing apm2go attaches to is a JVM — which gets the cup, the same joke the
 * logo makes. A generic set would have offered a document, a share arrow and a
 * gear for those three, and the sidebar would name what it links to less
 * precisely than the product does.
 *
 * All are drawn on a 16-unit grid as strokes, so weight stays even across the
 * set and one size prop scales them together.
 */
export type NavIconName =
  | "overview"
  | "services"
  | "traces"
  | "map"
  | "host"
  | "jvms"
  | "settings";

const PATHS: Record<NavIconName, ReactNode> = {
  // A gauge, the instrument printed on the cup: the landing view is the
  // product's own dial.
  overview: (
    <>
      <path d="M2.5 12.5a6.5 6.5 0 1 1 11 0" />
      <path d="M8 12.5 11 7.6" />
    </>
  ),
  // Stacked slabs: many services, one on top of another.
  services: (
    <>
      <path d="M8 1.8 14.5 5 8 8.2 1.5 5z" />
      <path d="M1.5 8.4 8 11.6l6.5-3.2" />
      <path d="M1.5 11.6 8 14.8l6.5-3.2" />
    </>
  ),
  // A waterfall: spans of different length, each starting after the last.
  traces: (
    <>
      <path d="M2 3.5h9" />
      <path d="M4.5 8h8" />
      <path d="M7 12.5h5" />
    </>
  ),
  // A graph: nodes with edges between them.
  map: (
    <>
      <circle cx="3.5" cy="4" r="2" />
      <circle cx="12.5" cy="4" r="2" />
      <circle cx="8" cy="12.5" r="2" />
      <path d="M5.5 4h5" />
      <path d="M4.6 5.8 6.9 10.7" />
      <path d="M11.4 5.8 9.1 10.7" />
    </>
  ),
  // A machine: two stacked units with their status lights.
  host: (
    <>
      <rect x="1.8" y="2.5" width="12.4" height="4.8" rx="1.2" />
      <rect x="1.8" y="8.7" width="12.4" height="4.8" rx="1.2" />
      <path d="M4.4 4.9h.01M4.4 11.1h.01" />
    </>
  ),
  // The cup, reduced to a stroke: a tapered body, a lid, and steam.
  jvms: (
    <>
      <path d="M3.6 6.3h8.8l-.9 7a1.2 1.2 0 0 1-1.2 1.05H5.7A1.2 1.2 0 0 1 4.5 13.3z" />
      <path d="M2.9 4.1h10.2v2.2H2.9z" />
      <path d="M6.6 2.4c.7-.6.1-1.2.1-1.2M9.4 2.4c.7-.6.1-1.2.1-1.2" />
    </>
  ),
  // Sliders, not a gear: this page is a set of values, not a mechanism.
  settings: (
    <>
      <path d="M2 4.5h5M10 4.5h4" />
      <path d="M2 11.5h4M9 11.5h5" />
      <circle cx="8.6" cy="4.5" r="1.7" />
      <circle cx="7.4" cy="11.5" r="1.7" />
    </>
  ),
};

export function NavIcon({ name, size = 16 }: { name: NavIconName; size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.4"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      className="shrink-0"
    >
      {PATHS[name]}
    </svg>
  );
}

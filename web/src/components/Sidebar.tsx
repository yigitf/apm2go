import { NavLink } from "react-router-dom";
import { BrandLockup, BrandMark } from "./Brand";
import { NavIcon, type NavIconName } from "./navicons";
import { useSidebar } from "./SidebarProvider";
import { ThemeToggle } from "./ThemeToggle";

interface NavItem {
  to: string;
  label: string;
  icon: NavIconName;
  end?: boolean;
}

/**
 * The sections, grouped by what they are about.
 *
 * The split is not decoration: the first group is telemetry that arrived, the
 * second is the machinery it arrived from. They fail independently and are
 * read at different times — an operator looking at a latency spike stays in
 * the first, and one asking why a service reports nothing at all goes
 * straight to the second.
 */
const GROUPS: { label: string; items: NavItem[] }[] = [
  {
    label: "Telemetry",
    items: [
      { to: "/", label: "Overview", icon: "overview", end: true },
      { to: "/services", label: "Services", icon: "services" },
      { to: "/traces", label: "Traces", icon: "traces" },
      { to: "/map", label: "Service map", icon: "map" },
    ],
  },
  {
    label: "Instrumented",
    items: [
      { to: "/host", label: "Host", icon: "host" },
      { to: "/jvms", label: "JVMs", icon: "jvms" },
    ],
  },
];

/* Named for what it offers rather than for its route. "Settings" promises
   knobs that change how apm2go runs; what this page actually holds is the
   appearance choice and a read-only account of how the process is coping. */
const PREFERENCES: NavItem = { to: "/settings", label: "Preferences", icon: "settings" };

/** One row in the rail. Collapsed, it is the icon and its tooltip. */
function Item({ item, expanded }: { item: NavItem; expanded: boolean }) {
  return (
    <NavLink
      to={item.to}
      end={item.end}
      title={item.label}
      className="group relative flex items-center gap-2.5 rounded-[var(--radius-control)] px-2.5 py-[7px] text-[13px] transition-colors"
      style={({ isActive }) => ({
        background: isActive ? "var(--rail-active)" : "transparent",
        color: isActive ? "var(--rail-text-active)" : "var(--rail-text)",
        fontWeight: isActive ? 600 : 450,
      })}
    >
      {({ isActive }) => (
        <>
          {/* The one place the brand orange appears in the rail. It marks the
              open section and nothing else, so it never has to compete with
              itself. */}
          <span
            aria-hidden="true"
            className="absolute top-1/2 -left-2 h-4 w-[3px] -translate-y-1/2 rounded-r-full transition-opacity"
            style={{
              background: "var(--brand-orange)",
              opacity: isActive ? 1 : 0,
            }}
          />
          <NavIcon name={item.icon} />
          {expanded && <span className="truncate">{item.label}</span>}
        </>
      )}
    </NavLink>
  );
}

/**
 * The pin. A thumbtack, upright when the rail is held open and laid at an
 * angle — the way a real pin looks pulled halfway out — when it is not, so
 * the two states read apart from the icon alone, at the size the collapsed
 * rail gives it.
 */
function PinToggle({ expanded }: { expanded: boolean }) {
  const { pinned, setPinned } = useSidebar();
  return (
    <button
      type="button"
      onClick={() => setPinned(!pinned)}
      title={pinned ? "Unpin sidebar" : "Pin sidebar open"}
      aria-pressed={pinned}
      className="flex items-center gap-2.5 rounded-[var(--radius-control)] px-2.5 py-[7px] text-[13px] transition-colors"
      style={{ color: "var(--rail-muted)" }}
    >
      <svg
        width="16"
        height="16"
        viewBox="0 0 16 16"
        fill="none"
        stroke="currentColor"
        strokeWidth="1.4"
        strokeLinecap="round"
        strokeLinejoin="round"
        aria-hidden="true"
        className="shrink-0 transition-transform duration-150"
        style={{ transform: pinned ? "none" : "rotate(40deg)" }}
      >
        <circle cx="8" cy="5.3" r="2.8" />
        <path d="M8 8.1v6.4" />
      </svg>
      {expanded && <span>{pinned ? "Pinned" : "Unpinned"}</span>}
    </button>
  );
}

/**
 * The navigation rail.
 *
 * Vertical rather than across the top, which is what lets the sections carry
 * icons and group headings at all — and gives every chart on every page the
 * ~50px of height a second header row would have taken. It keeps its own dark
 * surface in both themes; see --rail-bg for why.
 *
 * Whether it is expanded or narrowed to icons comes from useSidebar, not from
 * a CSS breakpoint directly: an operator can pin it open or unpin it with the
 * button below the nav list, and that choice is what a narrow window falls
 * back from to the icon-only rail. See SidebarProvider for exactly how the
 * two interact.
 */
export function Sidebar({ version }: { version?: string }) {
  const { expanded } = useSidebar();

  return (
    /* Two elements: the outer column carries the surface and stretches to
       whatever the page's full height turns out to be, so the rail is never a
       dark band with page showing under it on a long page; the inner column is
       the viewport-tall thing that sticks. */
    <aside
      className={`shrink-0 overflow-hidden transition-[width] duration-150 ${
        expanded ? "w-[228px]" : "w-[60px]"
      }`}
      style={{
        background: "var(--rail-bg)",
        borderRight: "1px solid var(--rail-border)",
      }}
    >
      <div
        className={`sticky top-0 flex h-dvh w-full flex-col gap-1 py-3.5 ${
          expanded ? "px-3" : "px-2.5"
        }`}
      >
        {/* Collapsed: the mark alone. Expanded: the supplied lockup, which
            already carries the mark ahead of the wordmark — showing both here
            would draw the cup twice. */}
        <div className="flex items-center px-1 pt-0.5 pb-4">
          {expanded ? (
            <span title="APM in minutes, not days.">
              <BrandLockup height={140} />
            </span>
          ) : (
            <BrandMark size={34} />
          )}
        </div>

        <nav className="flex flex-1 flex-col gap-4 overflow-y-auto">
          {GROUPS.map((group) => (
            <div key={group.label} className="flex flex-col gap-0.5">
              {expanded && (
                <div className="eyebrow px-3 pb-1" style={{ color: "var(--rail-muted)" }}>
                  {group.label}
                </div>
              )}
              {group.items.map((item) => (
                <Item key={item.to} item={item} expanded={expanded} />
              ))}
            </div>
          ))}
        </nav>

        <div
          className="flex flex-col gap-1 pt-2"
          style={{ borderTop: "1px solid var(--rail-border)" }}
        >
          <PinToggle expanded={expanded} />
          <Item item={PREFERENCES} expanded={expanded} />
          <div
            className={`flex gap-2 px-1 pt-1.5 ${
              expanded ? "flex-row items-center justify-between" : "flex-col items-center"
            }`}
          >
            <ThemeToggle stack={!expanded} />
            {version && expanded && (
              <span
                className="ident truncate text-[11px]"
                style={{ color: "var(--rail-muted)" }}
                title={`Build ${version}`}
              >
                {version}
              </span>
            )}
          </div>
        </div>
      </div>
    </aside>
  );
}

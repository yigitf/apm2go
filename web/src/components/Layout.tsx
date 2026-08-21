import { Outlet, useLocation } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { api } from "../api/client";
import { Sidebar } from "./Sidebar";
import { TimeRangePicker } from "./TimeRange";

/** Section names, matched by path prefix so /services/:name resolves to Services. */
const SECTIONS: { prefix: string; label: string }[] = [
  { prefix: "/services", label: "Services" },
  { prefix: "/traces", label: "Traces" },
  { prefix: "/map", label: "Service map" },
  { prefix: "/host", label: "Host" },
  { prefix: "/jvms", label: "JVMs" },
  { prefix: "/settings", label: "Preferences" },
];

/**
 * Where the operator is, as a section and — on a detail page — the thing being
 * looked at.
 *
 * The rail already highlights the section, so this exists for the second half:
 * a trace id or a service name that is otherwise only visible in the address
 * bar. It is read out of the path rather than out of the loaded data, so it is
 * right before the fetch resolves and stays right if the fetch fails.
 */
function useCrumbs(): { section: string; detail?: string } {
  const { pathname } = useLocation();
  const match = SECTIONS.find(
    (s) => pathname === s.prefix || pathname.startsWith(`${s.prefix}/`),
  );
  if (!match) return { section: "Overview" };

  const rest = pathname.slice(match.prefix.length).replace(/^\//, "");
  return {
    section: match.label,
    detail: rest ? decodeURIComponent(rest) : undefined,
  };
}

/** The app shell: the rail, the page's own heading, and the routed page. */
export function Layout() {
  // Polled slowly: this only drives the rail's build string and the service
  // count, and a fast poll on every page would add load for no benefit.
  const { data: self } = useQuery({
    queryKey: ["self"],
    queryFn: api.self,
    refetchInterval: 15_000,
  });

  const { section, detail } = useCrumbs();
  const services = self?.storage?.services ?? 0;

  return (
    <div className="flex min-h-dvh">
      <Sidebar version={self?.version?.split(" ")[1]} />

      <div className="flex min-w-0 flex-1 flex-col">
        {/* Translucent and blurred rather than opaque, so a chart scrolling
            underneath stays faintly visible and the bar reads as a layer over
            the page rather than a lid on it. */}
        <header
          className="sticky top-0 z-20 flex flex-wrap items-center justify-between gap-x-5 gap-y-2 px-5 py-3 backdrop-blur-md"
          style={{
            background: "color-mix(in srgb, var(--surface-page) 82%, transparent)",
            borderBottom: "1px solid var(--border)",
          }}
        >
          <h1 className="flex min-w-0 items-baseline gap-2">
            <span className="text-[15px] font-semibold tracking-[-0.01em] whitespace-nowrap">
              {section}
            </span>
            {detail && (
              <>
                <span aria-hidden="true" style={{ color: "var(--text-muted)" }}>
                  /
                </span>
                <span
                  className="ident truncate text-[13px]"
                  style={{ color: "var(--text-secondary)" }}
                  title={detail}
                >
                  {detail}
                </span>
              </>
            )}
          </h1>

          <div className="flex shrink-0 items-center gap-3">
            {services > 0 && (
              <span
                className="hidden text-[12px] whitespace-nowrap sm:inline"
                style={{ color: "var(--text-muted)" }}
              >
                <span className="tabular" style={{ color: "var(--text-secondary)" }}>
                  {services}
                </span>{" "}
                service{services === 1 ? "" : "s"}
              </span>
            )}
            <TimeRangePicker />
          </div>
        </header>

        <main className="mx-auto w-full max-w-[1400px] flex-1 px-5 py-5">
          <Outlet />
        </main>
      </div>
    </div>
  );
}

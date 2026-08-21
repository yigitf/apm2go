import { createContext, useContext, useEffect, useState, type ReactNode } from "react";

/** Shared with nothing else; this is the only reader and writer of it. */
const STORAGE_KEY = "apm2go-sidebar-pinned";

/** The breakpoint the rail used to collapse at on its own, before the pin
 *  existed. It still applies as a floor: pinning does not force a full-width
 *  rail onto a window too narrow to show one. */
const NARROW_QUERY = "(min-width: 1024px)";

function readPinned(): boolean {
  try {
    const saved = localStorage.getItem(STORAGE_KEY);
    // Default pinned: that was the only behaviour before the button existed,
    // so a first-time operator sees exactly what they always saw.
    return saved === null ? true : saved === "1";
  } catch {
    return true;
  }
}

function useIsWide(): boolean {
  const [wide, setWide] = useState(
    () => typeof matchMedia !== "undefined" && matchMedia(NARROW_QUERY).matches,
  );
  useEffect(() => {
    const mql = matchMedia(NARROW_QUERY);
    const onChange = () => setWide(mql.matches);
    mql.addEventListener("change", onChange);
    return () => mql.removeEventListener("change", onChange);
  }, []);
  return wide;
}

const Context = createContext<{
  pinned: boolean;
  setPinned: (next: boolean) => void;
  /** What the rail should actually render as: pinned, and there is room. */
  expanded: boolean;
} | null>(null);

/**
 * Holds whether the rail is pinned open, for the rail itself and for the pin
 * button that sits inside it.
 *
 * "Pinned" is the operator's stated preference; "expanded" is what the rail
 * renders as, which also depends on window width. A pinned rail on a window
 * narrowed below 1024px still collapses to icons — the pin overrides nothing
 * about the space a window actually has, it only overrides the default that
 * would otherwise apply above that width.
 */
export function SidebarProvider({ children }: { children: ReactNode }) {
  const [pinned, setPinnedState] = useState(readPinned);
  const wide = useIsWide();

  const setPinned = (next: boolean) => {
    setPinnedState(next);
    try {
      localStorage.setItem(STORAGE_KEY, next ? "1" : "0");
    } catch {
      // The state above is what actually drives the layout; losing the
      // persisted choice only costs it on the next load.
    }
  };

  return (
    <Context.Provider value={{ pinned, setPinned, expanded: pinned && wide }}>
      {children}
    </Context.Provider>
  );
}

export function useSidebar() {
  const context = useContext(Context);
  if (!context) throw new Error("useSidebar must be used inside a SidebarProvider");
  return context;
}

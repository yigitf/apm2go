import { createContext, useContext, useEffect, useState, type ReactNode } from "react";

/**
 * What the operator chose, which is not the same as what is on screen:
 * "system" is a choice to follow the OS, and the OS can be either.
 */
export type ThemeChoice = "light" | "system" | "dark";

/** Shared with the inline script in index.html, which applies the saved
 *  choice before first paint. Changing it here means changing it there. */
const STORAGE_KEY = "apm2go-theme";

function readChoice(): ThemeChoice {
  try {
    const saved = localStorage.getItem(STORAGE_KEY);
    return saved === "light" || saved === "dark" ? saved : "system";
  } catch {
    // Storage disabled by policy, or private mode. Following the OS is a
    // perfectly good fallback.
    return "system";
  }
}

/**
 * Applies a choice to the document.
 *
 * "system" removes the attribute rather than resolving the OS preference and
 * stamping the answer: the stylesheet's own media query is what serves that
 * state, and it keeps tracking a preference that can change while the page is
 * open — which stamping a resolved value would freeze.
 */
function applyChoice(choice: ThemeChoice) {
  const root = document.documentElement;
  try {
    if (choice === "system") {
      root.removeAttribute("data-theme");
      localStorage.removeItem(STORAGE_KEY);
    } else {
      root.setAttribute("data-theme", choice);
      localStorage.setItem(STORAGE_KEY, choice);
    }
  } catch {
    // The attribute is what actually changes the palette; failing to persist
    // it costs the choice on the next load and nothing before then.
    if (choice === "system") root.removeAttribute("data-theme");
    else root.setAttribute("data-theme", choice);
  }
}

const Context = createContext<{
  choice: ThemeChoice;
  setChoice: (next: ThemeChoice) => void;
} | null>(null);

/**
 * Holds the theme choice for the whole app.
 *
 * It is a context rather than local state in the control because the choice is
 * offered in two places — the rail, for reach, and Preferences, because that
 * is where someone goes looking for it. Two independent copies would each show
 * their own idea of the current theme, and the one that had not been clicked
 * would be wrong.
 */
export function ThemeProvider({ children }: { children: ReactNode }) {
  const [choice, setChoice] = useState<ThemeChoice>(readChoice);

  useEffect(() => {
    applyChoice(choice);
  }, [choice]);

  return <Context.Provider value={{ choice, setChoice }}>{children}</Context.Provider>;
}

export function useTheme() {
  const context = useContext(Context);
  if (!context) throw new Error("useTheme must be used inside a ThemeProvider");
  return context;
}

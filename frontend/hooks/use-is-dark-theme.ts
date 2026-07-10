"use client";

import { useSyncExternalStore } from "react";

/**
 * Reads the resolved theme from the `dark` class that next-themes stamps onto
 * <html> before paint.
 *
 * useSyncExternalStore, not `useState` + `useEffect`. Three reasons:
 *
 *  1. The theme genuinely IS an external store — a class attribute owned by a
 *     pre-paint script, not by React. This is the hook designed for that.
 *  2. `useEffect(() => setMounted(true))` triggers a cascading render, which
 *     React's own `react-hooks/set-state-in-effect` lint rule now flags.
 *  3. It hydrates correctly for free: React uses `getServerSnapshot` for the
 *     server pass and the real snapshot after, so there is no mismatch warning
 *     and no flash of the wrong palette.
 *
 * `useTheme()` from next-themes returns undefined on the first client render,
 * which would paint the chart once in the wrong colors before correcting.
 */
export function useIsDarkTheme(): boolean {
  return useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}

function subscribe(onChange: () => void): () => void {
  const observer = new MutationObserver(onChange);
  observer.observe(document.documentElement, {
    attributes: true,
    attributeFilter: ["class"],
  });
  return () => observer.disconnect();
}

function getSnapshot(): boolean {
  return document.documentElement.classList.contains("dark");
}

// The server cannot know the user's theme. next-themes resolves it on the
// client before paint; until then, assume light.
function getServerSnapshot(): boolean {
  return false;
}

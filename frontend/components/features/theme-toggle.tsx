"use client";

import { Moon, Sun } from "lucide-react";
import { useTheme } from "next-themes";

import { Button } from "@/components/ui/button";
import { useIsDarkTheme } from "@/hooks/use-is-dark-theme";

/**
 * Client component: reads and writes the theme, which only exists in the browser.
 *
 * The current theme is read through useSyncExternalStore rather than a
 * `mounted` flag, so there is no hydration mismatch and no extra render pass.
 * next-themes is used only to *write* the theme.
 */
export function ThemeToggle() {
  const { setTheme } = useTheme();
  const isDark = useIsDarkTheme();

  return (
    <Button
      variant="ghost"
      size="icon"
      className="size-9"
      onClick={() => setTheme(isDark ? "light" : "dark")}
      aria-label={isDark ? "Switch to light theme" : "Switch to dark theme"}
    >
      {isDark ? <Sun className="size-4" aria-hidden /> : <Moon className="size-4" aria-hidden />}
    </Button>
  );
}

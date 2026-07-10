"use client";

import { ThemeProvider as NextThemesProvider } from "next-themes";
import type { ComponentProps } from "react";

/**
 * next-themes writes the theme class onto <html> from an inline script that runs
 * before paint, which is what prevents the white flash a naive
 * `useEffect(() => setTheme(...))` produces on every page load.
 */
export function ThemeProvider({
  children,
  ...props
}: ComponentProps<typeof NextThemesProvider>) {
  return <NextThemesProvider {...props}>{children}</NextThemesProvider>;
}

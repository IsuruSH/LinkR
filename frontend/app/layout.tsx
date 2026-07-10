import type { Metadata } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import { Toaster } from "sonner";

import { QueryProvider } from "@/providers/query-provider";
import { ThemeProvider } from "@/providers/theme-provider";

import "./globals.css";

// The CSS variable names must match what globals.css consumes (--font-sans and
// --font-geist-mono). The create-next-app default exports --font-geist-sans,
// which shadcn's stylesheet never reads, so the font loads and is then ignored.
const geistSans = Geist({ variable: "--font-sans", subsets: ["latin"] });
const geistMono = Geist_Mono({ variable: "--font-geist-mono", subsets: ["latin"] });

export const metadata: Metadata = {
  title: "Linkr",
  description: "Short links with click analytics.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    // suppressHydrationWarning is required by next-themes: its pre-paint script
    // stamps `class` onto <html> before React hydrates, so server and client
    // markup legitimately differ on exactly this element. Scoped to <html>, it
    // does not hide hydration bugs anywhere else in the tree.
    <html
      lang="en"
      suppressHydrationWarning
      className={`${geistSans.variable} ${geistMono.variable} h-full antialiased`}
    >
      <body className="bg-background text-foreground flex min-h-full flex-col">
        <ThemeProvider
          attribute="class"
          defaultTheme="system"
          enableSystem
          disableTransitionOnChange
        >
          <QueryProvider>
            {children}
            <Toaster richColors closeButton position="top-right" />
          </QueryProvider>
        </ThemeProvider>
      </body>
    </html>
  );
}

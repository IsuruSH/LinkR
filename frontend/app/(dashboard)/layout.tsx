import { Link2 } from "lucide-react";
import Link from "next/link";

import { SignOutButton } from "@/components/features/sign-out-button";
import { ThemeToggle } from "@/components/features/theme-toggle";

/**
 * Server component. The header is static chrome; only the two buttons inside it
 * are client components, so the shell itself ships no JavaScript.
 *
 * There is no auth check here. middleware.ts already bounced an unauthenticated
 * request before this rendered, and the Go API is the real boundary regardless.
 * A second check here would be theatre.
 */
export default function DashboardLayout({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex min-h-full flex-1 flex-col">
      <header className="bg-background/80 sticky top-0 z-10 border-b backdrop-blur">
        <div className="mx-auto flex h-14 w-full max-w-6xl items-center justify-between px-4">
          <Link
            href="/dashboard/links"
            className="flex items-center gap-2 font-semibold tracking-tight"
          >
            <span className="bg-primary text-primary-foreground flex size-7 items-center justify-center rounded-lg">
              <Link2 className="size-4" aria-hidden />
            </span>
            Linkr
          </Link>

          <div className="flex items-center gap-1">
            <ThemeToggle />
            <SignOutButton />
          </div>
        </div>
      </header>

      <main className="mx-auto w-full max-w-6xl flex-1 px-4 py-8">{children}</main>
    </div>
  );
}

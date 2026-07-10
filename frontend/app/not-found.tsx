import Link from "next/link";

import { Button } from "@/components/ui/button";

export default function NotFound() {
  return (
    <main className="flex flex-1 flex-col items-center justify-center gap-4 px-4 text-center">
      <p className="text-muted-foreground font-mono text-sm">404</p>
      <h1 className="text-2xl font-semibold tracking-tight">Page not found</h1>
      <p className="text-muted-foreground max-w-sm text-sm">
        Short links are served by the API, not by this app. If you followed a short link and
        landed here, it has expired or never existed.
      </p>
      <Button asChild className="mt-2">
        <Link href="/dashboard/links">Go to your links</Link>
      </Button>
    </main>
  );
}

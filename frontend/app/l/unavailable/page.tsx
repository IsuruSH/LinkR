import { Clock, LinkIcon, TriangleAlert } from "lucide-react";
import Link from "next/link";

import { Button } from "@/components/ui/button";

/**
 * Where the backend sends a browser when a short link is missing or expired,
 * instead of showing a raw JSON error. Server component: it only reads the
 * `reason` query param and renders static content, so it ships no JavaScript.
 *
 * `searchParams` is a Promise in the App Router.
 */
export const metadata = { title: "Link unavailable · Linkr" };

const COPY = {
  expired: {
    icon: Clock,
    title: "This link has expired",
    body: "The short link you followed was set to expire and no longer points anywhere. Ask whoever shared it for an updated link.",
  },
  "not-found": {
    icon: TriangleAlert,
    title: "This link doesn't exist",
    body: "We couldn't find a short link with that code. It may have been deleted, or the address was mistyped.",
  },
} as const;

type Reason = keyof typeof COPY;

function resolveReason(value: string | string[] | undefined): Reason {
  return value === "expired" ? "expired" : "not-found";
}

export default async function LinkUnavailablePage({
  searchParams,
}: {
  searchParams: Promise<{ reason?: string }>;
}) {
  const { reason } = await searchParams;
  const { icon: Icon, title, body } = COPY[resolveReason(reason)];

  return (
    <main className="flex flex-1 items-center justify-center px-4 py-12">
      <div className="w-full max-w-md text-center">
        <div className="bg-muted mx-auto mb-6 flex size-14 items-center justify-center rounded-2xl">
          <Icon className="text-muted-foreground size-6" aria-hidden />
        </div>

        <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
        <p className="text-muted-foreground mx-auto mt-3 max-w-sm text-sm leading-relaxed">
          {body}
        </p>

        <div className="mt-8 flex items-center justify-center gap-3">
          <Button asChild>
            <Link href="/dashboard/links">
              <LinkIcon className="size-4" aria-hidden />
              Go to Linkr
            </Link>
          </Button>
        </div>

        <p className="text-muted-foreground mt-10 text-xs">
          Powered by <span className="font-medium">Linkr</span>
        </p>
      </div>
    </main>
  );
}

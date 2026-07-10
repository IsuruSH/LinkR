"use client";

import { ArrowLeft, Check, Copy, ExternalLink, Loader2, TriangleAlert } from "lucide-react";
import Link from "next/link";
import { useState } from "react";

import { ClicksChart } from "@/components/features/clicks-chart";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useCopyToClipboard } from "@/hooks/use-copy-to-clipboard";
import { useLinkStats } from "@/hooks/use-links";
import { toApiError } from "@/lib/api-error";
import type { StatsRange } from "@/types/api";

const RANGES: { value: StatsRange; label: string }[] = [
  { value: "7d", label: "7 days" },
  { value: "30d", label: "30 days" },
  { value: "all", label: "All time" },
];

export function LinkStats({ code }: { code: string }) {
  const [range, setRange] = useState<StatsRange>("7d");
  const { data, isPending, isError, error, refetch, isRefetching, isPlaceholderData } =
    useLinkStats(code, range);
  const { copy, copiedValue } = useCopyToClipboard();

  if (isPending) return <StatsSkeleton />;

  if (isError) {
    const apiError = toApiError(error);
    // A 404 here means the code does not exist, or belongs to someone else. The
    // API refuses to distinguish the two, and so does this screen.
    const notFound = apiError.code === "LINK_NOT_FOUND";

    return (
      <div
        role="alert"
        className="flex flex-col items-center justify-center rounded-lg border border-dashed px-6 py-16 text-center"
      >
        <div className="bg-muted mb-4 flex size-12 items-center justify-center rounded-full">
          <TriangleAlert className="text-muted-foreground size-5" aria-hidden />
        </div>
        <h2 className="text-lg font-semibold">
          {notFound ? "Link not found" : "Could not load stats"}
        </h2>
        <p className="text-muted-foreground mt-1 mb-6 max-w-sm text-sm">
          {notFound
            ? `No link with the code "${code}" belongs to your account.`
            : apiError.message}
        </p>
        <div className="flex gap-2">
          <Button variant="outline" asChild>
            <Link href="/dashboard/links">Back to links</Link>
          </Button>
          {!notFound && (
            <Button onClick={() => void refetch()} disabled={isRefetching}>
              {isRefetching && <Loader2 className="size-4 animate-spin" aria-hidden />}
              {isRefetching ? "Retrying…" : "Try again"}
            </Button>
          )}
        </div>
      </div>
    );
  }

  const isCopied = copiedValue === data.short_url;

  return (
    <div className="space-y-6">
      <div>
        <Button variant="ghost" size="sm" asChild className="-ml-2 mb-3">
          <Link href="/dashboard/links">
            <ArrowLeft className="size-4" aria-hidden />
            All links
          </Link>
        </Button>

        <div className="flex flex-wrap items-center gap-2">
          <h1 className="font-mono text-2xl font-semibold tracking-tight">/{data.short_code}</h1>
          <Button
            variant="ghost"
            size="icon"
            className="size-8"
            onClick={() => void copy(data.short_url)}
            aria-label={isCopied ? "Copied to clipboard" : `Copy ${data.short_url}`}
          >
            {isCopied ? (
              <Check className="size-4 text-emerald-600 dark:text-emerald-500" aria-hidden />
            ) : (
              <Copy className="size-4" aria-hidden />
            )}
          </Button>
        </div>

        <a
          href={data.long_url}
          target="_blank"
          rel="noopener noreferrer"
          className="text-muted-foreground hover:text-foreground mt-1 inline-flex max-w-full items-center gap-1.5 text-sm underline-offset-4 hover:underline"
        >
          <span className="truncate">{data.long_url}</span>
          <ExternalLink className="size-3 shrink-0" aria-hidden />
        </a>
      </div>

      {/* The headline number. Lifetime, from the denormalized counter — NOT the
          sum of the visible series, which is windowed by the selected range. */}
      <Card>
        <CardHeader className="pb-2">
          <CardDescription>Total clicks (all time)</CardDescription>
          <CardTitle className="text-4xl tabular-nums">
            {data.total_clicks.toLocaleString()}
          </CardTitle>
        </CardHeader>
      </Card>

      <Card>
        <CardHeader className="flex flex-row items-center justify-between gap-4 space-y-0">
          <div>
            <CardTitle className="text-base">Clicks per day</CardTitle>
            <CardDescription>Bucketed by UTC day.</CardDescription>
          </div>

          <Tabs value={range} onValueChange={(v) => setRange(v as StatsRange)}>
            <TabsList>
              {RANGES.map((r) => (
                <TabsTrigger key={r.value} value={r.value}>
                  {r.label}
                </TabsTrigger>
              ))}
            </TabsList>
          </Tabs>
        </CardHeader>

        <CardContent>
          {/* placeholderData keeps the previous range on screen while the next
              one loads; dim it so the user knows it is stale, rather than
              blanking the card and jumping the page height. */}
          <div
            className={isPlaceholderData ? "opacity-60 transition-opacity" : "transition-opacity"}
            aria-busy={isPlaceholderData}
          >
            <ClicksChart data={data.series} />
          </div>
        </CardContent>
      </Card>
    </div>
  );
}

function StatsSkeleton() {
  return (
    <div className="space-y-6">
      <div className="space-y-2">
        <Skeleton className="h-8 w-20" />
        <Skeleton className="h-9 w-48" />
        <Skeleton className="h-4 w-72" />
      </div>
      <Skeleton className="h-28 w-full rounded-xl" />
      <Skeleton className="h-100 w-full rounded-xl" />
      <span className="sr-only" role="status">
        Loading link statistics
      </span>
    </div>
  );
}

"use client";

import {
  BarChart3,
  Check,
  Copy,
  ExternalLink,
  Link2,
  Loader2,
  MoreHorizontal,
  Trash2,
  TriangleAlert,
} from "lucide-react";
import NextLink from "next/link";
import { useState } from "react";

import { CreateLinkDialog } from "@/components/features/create-link-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useCopyToClipboard } from "@/hooks/use-copy-to-clipboard";
import { useDeleteLink, useLinks } from "@/hooks/use-links";
import { toApiError } from "@/lib/api-error";
import type { Link } from "@/types/api";

export function LinksTable() {
  const { data, isPending, isError, error, refetch, isRefetching, fetchNextPage, hasNextPage, isFetchingNextPage } =
    useLinks();

  if (isPending) return <TableSkeleton />;
  if (isError) return <ErrorState message={toApiError(error).message} onRetry={() => void refetch()} isRetrying={isRefetching} />;

  const links = data.links;
  if (links.length === 0) return <EmptyState />;

  return (
    <div className="space-y-4">
      <div className="rounded-lg border">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-[220px]">Short link</TableHead>
              <TableHead>Destination</TableHead>
              <TableHead className="w-[110px] text-right">Clicks</TableHead>
              <TableHead className="w-[130px]">Created</TableHead>
              <TableHead className="w-[60px]">
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {links.map((link) => (
              <LinkRow key={link.short_code} link={link} />
            ))}
          </TableBody>
        </Table>
      </div>

      {hasNextPage && (
        <div className="flex justify-center">
          <Button variant="outline" onClick={() => void fetchNextPage()} disabled={isFetchingNextPage}>
            {isFetchingNextPage && <Loader2 className="size-4 animate-spin" aria-hidden />}
            {isFetchingNextPage ? "Loading…" : "Load more"}
          </Button>
        </div>
      )}
    </div>
  );
}

function LinkRow({ link }: { link: Link }) {
  const { copy, copiedValue } = useCopyToClipboard();
  const deleteLink = useDeleteLink();
  const [menuOpen, setMenuOpen] = useState(false);

  const isCopied = copiedValue === link.short_url;

  return (
    <TableRow>
      <TableCell>
        <div className="flex items-center gap-1">
          <code className="font-mono text-sm">/{link.short_code}</code>
          <Button
            variant="ghost"
            size="icon"
            className="size-7"
            onClick={() => void copy(link.short_url)}
            // The visible affordance is an icon, so the accessible name has to
            // come from here. It also announces the state change on copy.
            aria-label={isCopied ? "Copied to clipboard" : `Copy ${link.short_url}`}
          >
            {isCopied ? (
              <Check className="size-3.5 text-emerald-600 dark:text-emerald-500" aria-hidden />
            ) : (
              <Copy className="size-3.5" aria-hidden />
            )}
          </Button>
          {/* Screen readers get the state change announced without a visual toast. */}
          <span aria-live="polite" className="sr-only">
            {isCopied ? "Copied to clipboard" : ""}
          </span>
        </div>
      </TableCell>

      <TableCell className="max-w-[1px]">
        <a
          href={link.long_url}
          target="_blank"
          // noreferrer implies noopener, but both are spelled out: without
          // noopener the opened page can navigate this one via window.opener.
          rel="noopener noreferrer"
          className="text-muted-foreground hover:text-foreground flex items-center gap-1.5 truncate text-sm underline-offset-4 hover:underline"
          title={link.long_url}
        >
          <span className="truncate">{link.long_url}</span>
          <ExternalLink className="size-3 shrink-0" aria-hidden />
        </a>
      </TableCell>

      <TableCell className="text-right">
        <Badge variant={link.click_count > 0 ? "secondary" : "outline"} className="tabular-nums">
          {link.click_count.toLocaleString()}
        </Badge>
      </TableCell>

      <TableCell className="text-muted-foreground text-sm">
        {formatDate(link.created_at)}
      </TableCell>

      <TableCell>
        <DropdownMenu open={menuOpen} onOpenChange={setMenuOpen}>
          <DropdownMenuTrigger asChild>
            <Button variant="ghost" size="icon" className="size-8" aria-label={`Actions for ${link.short_code}`}>
              <MoreHorizontal className="size-4" aria-hidden />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem asChild>
              <NextLink href={`/dashboard/links/${link.short_code}/stats`}>
                <BarChart3 className="size-4" aria-hidden />
                View stats
              </NextLink>
            </DropdownMenuItem>
            <DropdownMenuItem
              variant="destructive"
              disabled={deleteLink.isPending}
              onClick={() => deleteLink.mutate(link.short_code)}
            >
              <Trash2 className="size-4" aria-hidden />
              Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </TableCell>
    </TableRow>
  );
}

/**
 * A skeleton shaped like the table it replaces. A generic spinner would collapse
 * the layout and then shift it back once data arrives.
 */
function TableSkeleton() {
  return (
    <div className="rounded-lg border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead className="w-[220px]">Short link</TableHead>
            <TableHead>Destination</TableHead>
            <TableHead className="w-[110px] text-right">Clicks</TableHead>
            <TableHead className="w-[130px]">Created</TableHead>
            <TableHead className="w-[60px]" />
          </TableRow>
        </TableHeader>
        <TableBody>
          {Array.from({ length: 4 }).map((_, i) => (
            <TableRow key={i}>
              <TableCell><Skeleton className="h-5 w-28" /></TableCell>
              <TableCell><Skeleton className="h-5 w-full max-w-sm" /></TableCell>
              <TableCell className="flex justify-end"><Skeleton className="h-5 w-10" /></TableCell>
              <TableCell><Skeleton className="h-5 w-20" /></TableCell>
              <TableCell><Skeleton className="size-8 rounded-md" /></TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
      <span className="sr-only" role="status">Loading links</span>
    </div>
  );
}

function EmptyState() {
  return (
    <div className="flex flex-col items-center justify-center rounded-lg border border-dashed px-6 py-16 text-center">
      <div className="bg-muted mb-4 flex size-12 items-center justify-center rounded-full">
        <Link2 className="text-muted-foreground size-5" aria-hidden />
      </div>
      <h3 className="text-lg font-semibold">No links yet</h3>
      <p className="text-muted-foreground mt-1 mb-6 max-w-sm text-sm">
        Create your first short link and every click on it will show up here.
      </p>
      <CreateLinkDialog />
    </div>
  );
}

/** A real error state with a working retry, not a spinner that never resolves. */
function ErrorState({
  message,
  onRetry,
  isRetrying,
}: {
  message: string;
  onRetry: () => void;
  isRetrying: boolean;
}) {
  return (
    <div
      role="alert"
      className="border-destructive/40 bg-destructive/5 flex flex-col items-center justify-center rounded-lg border px-6 py-16 text-center"
    >
      <div className="bg-destructive/10 mb-4 flex size-12 items-center justify-center rounded-full">
        <TriangleAlert className="text-destructive size-5" aria-hidden />
      </div>
      <h3 className="text-lg font-semibold">Could not load your links</h3>
      <p className="text-muted-foreground mt-1 mb-6 max-w-sm text-sm">{message}</p>
      <Button variant="outline" onClick={onRetry} disabled={isRetrying}>
        {isRetrying && <Loader2 className="size-4 animate-spin" aria-hidden />}
        {isRetrying ? "Retrying…" : "Try again"}
      </Button>
    </div>
  );
}

/**
 * Fixed locale and UTC. `toLocaleDateString()` with the browser's locale renders
 * different text on the server and the client, which React reports as a
 * hydration mismatch.
 */
function formatDate(iso: string): string {
  return new Date(iso).toLocaleDateString("en-US", {
    year: "numeric",
    month: "short",
    day: "numeric",
    timeZone: "UTC",
  });
}

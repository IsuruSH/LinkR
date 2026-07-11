"use client";

import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { toast } from "sonner";

import { api, queryKeys, type CreateLinkInput } from "@/lib/api-client";
import { toApiError } from "@/lib/api-error";
import type { StatsRange } from "@/types/api";

const PAGE_SIZE = 20;

/**
 * Page-at-a-time links with Previous / Next.
 *
 * The API is keyset: each page returns an opaque cursor for the *next* page and
 * knows nothing about page numbers, so there is no random page access — which is
 * exactly what keeps a row from skipping or duplicating when a link is created
 * mid-paging (see DECISIONS.md). To make "Previous" work over a forward-only
 * cursor, we keep a stack of the cursors we have visited: the current page is
 * the top, Back pops it, Next pushes the page's next_cursor.
 *
 * `[null]` is page 1 (no cursor). The page number is the stack depth.
 */
export function usePagedLinks() {
  const [cursorStack, setCursorStack] = useState<(string | null)[]>([null]);
  const currentCursor = cursorStack[cursorStack.length - 1];

  const query = useQuery({
    queryKey: queryKeys.list(PAGE_SIZE, currentCursor),
    queryFn: () => api.listLinks(currentCursor, PAGE_SIZE),
    // Keep the current page visible while the next one loads, so the table does
    // not blank and jump between clicks.
    placeholderData: keepPreviousData,
  });

  const hasNext = Boolean(query.data?.next_cursor);
  const hasPrev = cursorStack.length > 1;

  function goNext() {
    const next = query.data?.next_cursor;
    if (next) setCursorStack((s) => [...s, next]);
  }

  function goPrev() {
    setCursorStack((s) => (s.length > 1 ? s.slice(0, -1) : s));
  }

  return {
    query,
    page: cursorStack.length,
    hasNext,
    hasPrev,
    goNext,
    goPrev,
    // Reset to page 1 after a mutation, so a newly created link (which lands at
    // the top) is visible and the cursor stack cannot point past the new data.
    resetToFirstPage: () => setCursorStack([null]),
  };
}

export function useCreateLink() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (input: CreateLinkInput) => api.createLink(input),
    onSuccess: (link) => {
      toast.success("Link created", { description: link.short_url });
      // Invalidate the whole `links` prefix rather than trying to splice the new
      // row into the right page: with a cursor, the new link belongs at the top
      // of page one, and hand-patching the cache would desync the cursors.
      void queryClient.invalidateQueries({ queryKey: queryKeys.links });
    },
    // Field-level errors (ALIAS_TAKEN, INVALID_URL) are surfaced on the form by
    // the caller. Only unattributable failures become toasts, so the user never
    // sees the same message twice.
    onError: (error) => {
      const apiError = toApiError(error);

      // Rate limiting is not a field error and reads better with the wait time.
      if (apiError.code === "RATE_LIMITED") {
        const retry = apiError.details?.retry_after_seconds;
        toast.error("Slow down", {
          description: retry
            ? `You're creating links too quickly. Try again in ${retry}s.`
            : "You're creating links too quickly. Try again shortly.",
        });
        return;
      }

      const isFieldError =
        apiError.code === "ALIAS_TAKEN" ||
        apiError.code === "RESERVED_ALIAS" ||
        apiError.code === "INVALID_ALIAS" ||
        apiError.code === "INVALID_URL";
      if (!isFieldError) {
        toast.error("Could not create link", { description: apiError.message });
      }
    },
  });
}

export function useDeleteLink() {
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (code: string) => api.deleteLink(code),
    onSuccess: (_data, code) => {
      toast.success("Link deleted", { description: `/${code} no longer resolves.` });
      void queryClient.invalidateQueries({ queryKey: queryKeys.links });
    },
    onError: (error) => {
      toast.error("Could not delete link", { description: toApiError(error).message });
    },
  });
}

export function useLinkStats(code: string, range: StatsRange) {
  return useQuery({
    queryKey: queryKeys.stats(code, range),
    queryFn: () => api.getStats(code, range),
    // Keep the previous range's chart on screen while the new one loads, so
    // switching tabs does not blank the graph and jump the layout.
    placeholderData: (previous) => previous,
  });
}

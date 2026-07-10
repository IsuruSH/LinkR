"use client";

import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
} from "@tanstack/react-query";
import { toast } from "sonner";

import { api, queryKeys, type CreateLinkInput } from "@/lib/api-client";
import { toApiError } from "@/lib/api-error";
import type { LinkPage, StatsRange } from "@/types/api";

const PAGE_SIZE = 20;

/**
 * Paginated links.
 *
 * useInfiniteQuery matches the API exactly: the server returns an opaque cursor
 * and knows nothing about page numbers, so there is no page index to compute.
 * `getNextPageParam` returns undefined on the last page, which is how TanStack
 * knows to disable `fetchNextPage`.
 */
export function useLinks() {
  return useInfiniteQuery({
    queryKey: queryKeys.list(PAGE_SIZE),
    queryFn: ({ pageParam }) => api.listLinks(pageParam, PAGE_SIZE),
    initialPageParam: null as string | null,
    getNextPageParam: (lastPage: LinkPage) => lastPage.next_cursor ?? undefined,
    // Flatten so the table renders one list rather than a list of pages.
    select: (data) => ({
      links: data.pages.flatMap((page) => page.items),
      pages: data.pages,
    }),
  });
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

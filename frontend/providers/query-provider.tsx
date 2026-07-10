"use client";

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { useState } from "react";

import { ApiError } from "@/lib/api-error";

/**
 * TanStack Query owns all server state. There is no `useEffect` fetching
 * anywhere in this app, which is what makes loading and error states uniform
 * instead of hand-rolled per component.
 */
export function QueryProvider({ children }: { children: React.ReactNode }) {
  const router = useRouter();

  // useState, not a module-level client. A module singleton is shared across
  // requests on the server, so one user's cached data could be rendered into
  // another user's HTML. Per-component state gives each request its own.
  const [queryClient] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            // Data is considered fresh for 30s. Long enough that navigating
            // between the dashboard and a stats page does not refetch, short
            // enough that a new click count shows up without a hard reload.
            staleTime: 30_000,
            refetchOnWindowFocus: false,

            retry: (failureCount, error) => {
              // Retrying a 401 or a 409 just burns time and rate limit: the
              // answer will not change. Only network faults and 5xx are worth
              // a second attempt.
              if (error instanceof ApiError && !error.isRetryable) return false;
              return failureCount < 2;
            },
            retryDelay: (attempt) => Math.min(1000 * 2 ** attempt, 8000),
          },
          mutations: {
            // A mutation that failed may have partially applied. Retrying a
            // POST /links would create a second link. Never retry by default.
            retry: false,
          },
        },
      }),
  );

  // A 401 anywhere means the cookie expired or was revoked. The BFF has already
  // cleared it; push the user to /login rather than letting every query on the
  // page fail one by one with its own error card.
  queryClient.getQueryCache().config.onError = (error) => {
    if (error instanceof ApiError && error.isAuthError) {
      router.push("/login");
    }
  };

  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

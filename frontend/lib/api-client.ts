import axios from "axios";

import { toApiError } from "@/lib/api-error";
import type { Link, LinkPage, LinkStats, StatsRange } from "@/types/api";

/**
 * The browser-side API client.
 *
 * Note the baseURL: `/api/bff`, a route on THIS Next server — not the Go API.
 *
 * The JWT lives in an httpOnly cookie, which JavaScript cannot read by design.
 * So the browser cannot attach an `Authorization` header itself. Instead it
 * calls the Next route handler at /api/bff/*, which reads the cookie
 * server-side and forwards the request to Go with the bearer token attached.
 *
 * The cost is one extra hop inside the cluster. What it buys: an XSS in any
 * dependency cannot exfiltrate the token, because the token is never in the
 * document. See DECISIONS.md.
 */
const client = axios.create({
  baseURL: "/api/bff",
  timeout: 10_000,
  headers: { "Content-Type": "application/json" },
});

client.interceptors.response.use(
  (response) => response,
  // Reject with ApiError so every caller — TanStack Query included — sees one
  // error shape. Without this, `error` in a component is `any`.
  (error) => Promise.reject(toApiError(error)),
);

export interface CreateLinkInput {
  url: string;
  alias?: string;
}

export const api = {
  async createLink(input: CreateLinkInput): Promise<Link> {
    // Send `alias` only when non-empty: the backend rejects unknown fields and
    // treats "" as "generate one for me", but omitting is the clearer contract.
    const body: CreateLinkInput = input.alias ? input : { url: input.url };
    const { data } = await client.post<Link>("/links", body);
    return data;
  },

  async listLinks(cursor?: string | null, limit = 20): Promise<LinkPage> {
    const { data } = await client.get<LinkPage>("/links", {
      params: { limit, ...(cursor ? { cursor } : {}) },
    });
    return data;
  },

  async getStats(code: string, range: StatsRange): Promise<LinkStats> {
    const { data } = await client.get<LinkStats>(
      `/links/${encodeURIComponent(code)}/stats`,
      { params: { range } },
    );
    return data;
  },

  async deleteLink(code: string): Promise<void> {
    await client.delete(`/links/${encodeURIComponent(code)}`);
  },
};

/**
 * Query keys, centralized so an invalidation cannot miss a cache entry through a
 * typo. `list()` is a prefix of every paginated list query, so invalidating it
 * refetches whichever page the user is on.
 */
export const queryKeys = {
  links: ["links"] as const,
  list: (limit: number) => ["links", "list", limit] as const,
  stats: (code: string, range: StatsRange) => ["links", "stats", code, range] as const,
};

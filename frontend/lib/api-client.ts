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
  /** RFC3339 instant, or omitted for a link that never expires. */
  expires_at?: string;
}

export const api = {
  async createLink(input: CreateLinkInput): Promise<Link> {
    // Send only the fields that are set. The backend rejects unknown fields, and
    // omitting is a clearer contract than sending empty strings.
    const body: CreateLinkInput = { url: input.url };
    if (input.alias) body.alias = input.alias;
    if (input.expires_at) body.expires_at = input.expires_at;
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
  // Each page is cached under its own cursor, so navigating Back is instant and
  // a create/delete invalidates the whole `links` prefix regardless of page.
  list: (limit: number, cursor: string | null) => ["links", "list", limit, cursor] as const,
  stats: (code: string, range: StatsRange) => ["links", "stats", code, range] as const,
};

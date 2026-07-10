import { NextRequest, NextResponse } from "next/server";

import { SESSION_COOKIE, backendUrl, getSessionToken } from "@/lib/session";

/**
 * The BFF proxy.
 *
 * Browser -> (same origin, httpOnly cookie) -> this route -> (Bearer) -> Go API
 *
 * This exists because the JWT is in an httpOnly cookie: client JavaScript cannot
 * read it, and therefore cannot set an Authorization header. Rather than weaken
 * the cookie so JS can read it (the whole point is that XSS cannot), the token
 * is attached here, server-side.
 *
 * It forwards only what it must, and never forwards the cookie itself onward.
 */

/** Methods the API actually exposes. Anything else is refused here. */
const ALLOWED_METHODS = new Set(["GET", "POST", "DELETE"]);

/**
 * Response headers worth passing back. Everything else — Set-Cookie above all —
 * is dropped. A blanket copy would let a compromised backend set cookies on the
 * frontend's origin.
 */
const FORWARDED_RESPONSE_HEADERS = ["content-type", "x-request-id"];

async function proxy(req: NextRequest, ctx: { params: Promise<{ path: string[] }> }) {
  if (!ALLOWED_METHODS.has(req.method)) {
    return NextResponse.json(
      { error: { code: "METHOD_NOT_ALLOWED", message: "Method not allowed" } },
      { status: 405 },
    );
  }

  const token = await getSessionToken();
  if (!token) {
    // Answer here rather than round-tripping to Go just to be told 401.
    return NextResponse.json(
      { error: { code: "UNAUTHORIZED", message: "Authentication required" } },
      { status: 401 },
    );
  }

  const { path } = await ctx.params;

  // Rebuild the path from the matched segments rather than slicing the incoming
  // URL. Each segment is re-encoded, so a crafted `%2e%2e%2f` cannot escape the
  // /api prefix and reach an arbitrary backend route.
  const target = new URL(`/api/${path.map(encodeURIComponent).join("/")}`, backendUrl());
  target.search = req.nextUrl.search;

  let upstream: Response;
  try {
    upstream = await fetch(target, {
      method: req.method,
      headers: {
        Authorization: `Bearer ${token}`,
        ...(req.headers.get("content-type")
          ? { "Content-Type": req.headers.get("content-type")! }
          : {}),
      },
      // GET and DELETE carry no body; passing one makes undici throw.
      body: req.method === "POST" ? await req.text() : undefined,
      // The browser already has its own timeout; this bounds a hung backend.
      signal: AbortSignal.timeout(10_000),
      cache: "no-store",
    });
  } catch {
    return NextResponse.json(
      { error: { code: "INTERNAL", message: "The API is unreachable." } },
      { status: 502 },
    );
  }

  // An expired or revoked token: clear the cookie so middleware.ts stops
  // treating the user as signed in and bounces them to /login on next nav.
  if (upstream.status === 401) {
    const res = NextResponse.json(
      { error: { code: "UNAUTHORIZED", message: "Your session has expired." } },
      { status: 401 },
    );
    res.cookies.delete(SESSION_COOKIE);
    return res;
  }

  // 204 has no body, and constructing a Response with one throws.
  if (upstream.status === 204) {
    return new NextResponse(null, { status: 204 });
  }

  const headers = new Headers();
  for (const name of FORWARDED_RESPONSE_HEADERS) {
    const value = upstream.headers.get(name);
    if (value) headers.set(name, value);
  }

  return new NextResponse(upstream.body, { status: upstream.status, headers });
}

export const GET = proxy;
export const POST = proxy;
export const DELETE = proxy;

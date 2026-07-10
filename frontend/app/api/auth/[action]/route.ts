import { NextRequest, NextResponse } from "next/server";

import { SESSION_COOKIE, backendUrl, sessionCookieOptions } from "@/lib/session";
import type { TokenResponse } from "@/types/api";

/**
 * Auth route handlers: POST /api/auth/login | register | logout.
 *
 * This is the only place a JWT is ever handled. It calls Go, takes the token out
 * of the JSON response, and puts it into an httpOnly cookie. The token is never
 * returned to the browser, so it never reaches `document`, `localStorage`, or a
 * third-party script.
 */

const ACTIONS = new Set(["login", "register", "logout"]);

export async function POST(
  req: NextRequest,
  ctx: { params: Promise<{ action: string }> },
) {
  const { action } = await ctx.params;

  if (!ACTIONS.has(action)) {
    return NextResponse.json(
      { error: { code: "NOT_FOUND", message: "Unknown auth action" } },
      { status: 404 },
    );
  }

  if (action === "logout") {
    const res = NextResponse.json({ ok: true });
    res.cookies.delete(SESSION_COOKIE);
    return res;
  }

  let upstream: Response;
  try {
    upstream = await fetch(new URL(`/api/auth/${action}`, backendUrl()), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: await req.text(),
      signal: AbortSignal.timeout(10_000),
      cache: "no-store",
    });
  } catch {
    return NextResponse.json(
      { error: { code: "INTERNAL", message: "The API is unreachable." } },
      { status: 502 },
    );
  }

  const body = await upstream.text();

  if (!upstream.ok) {
    // Pass the backend's error envelope straight through, so the form can show
    // "that alias is already taken" rather than a generic failure.
    return new NextResponse(body, {
      status: upstream.status,
      headers: { "Content-Type": "application/json" },
    });
  }

  let token: TokenResponse;
  try {
    token = JSON.parse(body) as TokenResponse;
  } catch {
    return NextResponse.json(
      { error: { code: "INTERNAL", message: "Malformed response from the API." } },
      { status: 502 },
    );
  }

  // The cookie expires exactly when the JWT does. Any other value produces one
  // of two bad states: a cookie that outlives its token (silent 401s), or one
  // that dies early (surprise logout with a perfectly valid token).
  const expiresAt = new Date(token.expires_at);

  const res = NextResponse.json({ user: token.user });
  res.cookies.set(SESSION_COOKIE, token.access_token, sessionCookieOptions(expiresAt));
  return res;
}

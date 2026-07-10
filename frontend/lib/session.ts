import "server-only";

import { cookies } from "next/headers";

/**
 * Server-side session helpers. `server-only` makes importing this from a client
 * component a build error rather than a runtime leak of the cookie name and,
 * worse, an accidental attempt to read the token in the browser.
 */

export const SESSION_COOKIE = "linkr_session";

/**
 * The Go API's address as seen from the Next server process.
 *
 * Deliberately NOT NEXT_PUBLIC_API_URL. That one is baked into the client bundle
 * and points at whatever the *browser* can reach (localhost:8080). Inside Docker
 * the Next server reaches the backend at http://backend:8080 on the compose
 * network, where "localhost" would be the Next container itself.
 */
export function backendUrl(): string {
  return (
    process.env.BACKEND_INTERNAL_URL ??
    process.env.NEXT_PUBLIC_API_URL ??
    "http://localhost:8080"
  );
}

export async function getSessionToken(): Promise<string | undefined> {
  const store = await cookies();
  return store.get(SESSION_COOKIE)?.value;
}

/**
 * Cookie attributes, in one place so login and logout cannot disagree — a
 * mismatched `path` or `sameSite` on clear leaves the cookie sitting there.
 *
 * httpOnly:  JS cannot read it. An XSS cannot steal the session.
 * sameSite:  "lax" blocks CSRF on the state-changing routes while still letting
 *            a normal top-level navigation into /dashboard carry the cookie.
 * secure:    on in production only; a Secure cookie is dropped over plain HTTP,
 *            which would silently break local development.
 */
export function sessionCookieOptions(expiresAt: Date) {
  return {
    httpOnly: true,
    sameSite: "lax" as const,
    secure: process.env.NODE_ENV === "production",
    path: "/",
    expires: expiresAt,
  };
}

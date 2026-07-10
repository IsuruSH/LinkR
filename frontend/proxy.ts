import { NextRequest, NextResponse } from "next/server";

/**
 * Route protection.
 *
 * This is the `proxy.ts` file convention. It replaces `middleware.ts`, which
 * Next 16 deprecates — same signature, same runtime, different filename and a
 * default export. Naming it `middleware.ts` still works today but warns at build
 * time, and building on a deprecated convention is how the next major release
 * breaks a deploy.
 *
 * It checks only for the *presence* of the session cookie. It does not verify
 * the JWT signature, and that is deliberate:
 *
 *   - Verifying an HS256 signature here means shipping the signing secret to the
 *     edge runtime. The secret belongs on the API.
 *   - Presence is enough for a redirect. This is a UX affordance, not a security
 *     boundary — authorization happens in Go on every request, and a forged
 *     cookie earns a 401 there.
 *
 * Treating this as the security boundary would be the mistake. Its only job is
 * to avoid rendering a dashboard that is about to 401.
 */

const SESSION_COOKIE = "linkr_session";

const PUBLIC_ROUTES = ["/login", "/register"];

export default function proxy(req: NextRequest) {
  const { pathname } = req.nextUrl;
  const hasSession = req.cookies.has(SESSION_COOKIE);
  const isPublicRoute = PUBLIC_ROUTES.includes(pathname);

  if (!hasSession && !isPublicRoute) {
    const url = req.nextUrl.clone();
    url.pathname = "/login";
    // Remember where they were headed, so login can send them back there
    // instead of dumping everyone on the dashboard root.
    if (pathname !== "/") {
      url.searchParams.set("next", pathname);
    }
    return NextResponse.redirect(url);
  }

  // An authenticated user has no business on the login page.
  if (hasSession && isPublicRoute) {
    const url = req.nextUrl.clone();
    url.pathname = "/dashboard/links";
    url.search = "";
    return NextResponse.redirect(url);
  }

  return NextResponse.next();
}

export const config = {
  /**
   * Everything except Next internals, static assets, and our own route handlers.
   *
   * /api/auth must be excluded or login itself would be redirected to /login.
   * /api/bff must be excluded because it answers 401 as JSON; redirecting an
   * XHR to an HTML login page produces the classic "unexpected token <" error.
   */
  matcher: [
    "/((?!api/|_next/static|_next/image|favicon.ico|.*\\.(?:svg|png|jpg|jpeg|gif|webp)$).*)",
  ],
};

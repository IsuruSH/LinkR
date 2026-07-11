// Package handler translates HTTP to service calls and back. It never touches
// SQL, never picks a status code for an error (httpx does), and never contains
// business rules.
package handler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/IsuruSh/linkr/internal/domain"
	"github.com/IsuruSh/linkr/internal/httpx"
	"github.com/IsuruSh/linkr/internal/middleware"
	"github.com/IsuruSh/linkr/internal/service"
)

type LinkHandler struct {
	svc     *service.LinkService
	baseURL string
	appURL  string
}

func NewLinkHandler(svc *service.LinkService, baseURL, appURL string) *LinkHandler {
	return &LinkHandler{svc: svc, baseURL: baseURL, appURL: appURL}
}

// Routes returns the /api/links group. Every route in it is protected, so
// requireAuth is applied once to the whole sub-router rather than repeated per
// route — a route added later cannot forget it.
//
// rateLimitCreate, when non-nil, guards POST only — creation is the write worth
// throttling; reads and deletes are not. It is passed here rather than stored on
// the handler so the mount site in main.go shows, in one place, exactly which
// route is limited. A nil value (rate limiting disabled) leaves POST unwrapped,
// so the disabled path has no per-request cost.
func (h *LinkHandler) Routes(
	requireAuth func(http.Handler) http.Handler,
	rateLimitCreate func(http.Handler) http.Handler,
) chi.Router {
	r := chi.NewRouter()
	r.Use(requireAuth)

	if rateLimitCreate != nil {
		r.With(rateLimitCreate).Post("/", h.Create)
	} else {
		r.Post("/", h.Create)
	}
	r.Get("/", h.List)
	r.Get("/{code}/stats", h.Stats)
	r.Delete("/{code}", h.Delete)

	return r
}

// Redirect is the hot path. GET /{code}, public, no auth.
//
// Order of operations is the entire point of this handler:
//
//  1. Resolve the code (Redis, then Postgres on miss, then cache-fill).
//  2. Write the 302. The client is now already on its way.
//  3. Hand the click to the worker, which never blocks.
//
// Recording before step 2 would put a database write on the critical path of
// every redirect. Recording after means a crash between 2 and 3 loses the click,
// which is the trade this design makes explicitly.
func (h *LinkHandler) Redirect(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")

	entry, err := h.svc.Resolve(r.Context(), code)
	if err != nil {
		h.redirectError(w, r, err)
		return
	}

	// Without no-store the browser caches the 302 and every subsequent visit
	// skips our server entirely — the click counter would simply stop moving.
	// This is why the redirect is 302 (temporary) and not 301 (permanent):
	// a 301 is cached indefinitely and is effectively irreversible.
	w.Header().Set("Cache-Control", "no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")

	http.Redirect(w, r, entry.LongURL, http.StatusFound)

	h.svc.RecordClick(entry.LinkID, r.Referer(), r.UserAgent())
}

// redirectError handles a failed redirect with content negotiation. A browser
// (Accept: text/html) is sent to a friendly page in the frontend rather than
// shown a raw JSON envelope; an API client or curl still gets the JSON, with the
// correct status (404 not-found, 410 gone). The status is preserved for browsers
// too — a 302 to the frontend would mask it — by writing it before the redirect
// via an intermediate meta page is overkill, so we 303-See-Other to the app and
// let that page carry the reason. Bots reading the status still see the 303.
func (h *LinkHandler) redirectError(w http.ResponseWriter, r *http.Request, err error) {
	reason := ""
	switch {
	case errors.Is(err, domain.ErrLinkExpired):
		reason = "expired"
	case errors.Is(err, domain.ErrLinkNotFound):
		reason = "not-found"
	}

	// Non-browser clients, or an error we do not have a page for, get JSON.
	if reason == "" || !acceptsHTML(r) {
		httpx.Error(w, r, err)
		return
	}

	target := h.appURL + "/l/unavailable?reason=" + reason
	// no-store so the browser does not cache this bounce and skip a link that
	// might be recreated under the same code later.
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// acceptsHTML reports whether the client is a browser navigating to the page,
// as opposed to an API client or a link-preview fetcher. Browsers send
// "text/html" in Accept on a top-level navigation; curl and most SDKs do not.
func acceptsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// Create handles POST /api/links. JWT-protected.
func (h *LinkHandler) Create(w http.ResponseWriter, r *http.Request) {
	userID, err := middleware.UserIDFrom(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	var req createLinkRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}

	link, err := h.svc.Create(r.Context(), userID, req.URL, req.Alias, req.ExpiresAt)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	w.Header().Set("Location", h.baseURL+"/"+link.ShortCode)
	httpx.JSON(w, http.StatusCreated, toLinkResponse(link, h.baseURL))
}

// List handles GET /api/links?cursor=&limit=. JWT-protected.
func (h *LinkHandler) List(w http.ResponseWriter, r *http.Request) {
	userID, err := middleware.UserIDFrom(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	limit := parseLimit(r.URL.Query().Get("limit"))
	page, err := h.svc.List(r.Context(), userID, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	// A non-nil empty slice, so an empty page marshals to [] and not null.
	items := make([]linkResponse, len(page.Items))
	for i, l := range page.Items {
		items[i] = toLinkResponse(l, h.baseURL)
	}

	resp := listLinksResponse{Items: items}
	if page.NextCursor != "" {
		resp.NextCursor = &page.NextCursor
	}
	httpx.JSON(w, http.StatusOK, resp)
}

// Stats handles GET /api/links/{code}/stats?range=7d|30d|all. JWT-protected.
func (h *LinkHandler) Stats(w http.ResponseWriter, r *http.Request) {
	userID, err := middleware.UserIDFrom(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	rng := service.ParseRange(r.URL.Query().Get("range"))
	stats, err := h.svc.Stats(r.Context(), userID, chi.URLParam(r, "code"), rng)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toStatsResponse(stats, string(rng), h.baseURL))
}

// Delete handles DELETE /api/links/{code}. JWT-protected.
//
// Not required by the spec, but the cache invalidation path has to be reachable
// to be real. An invalidation that nothing calls is scaffolding.
func (h *LinkHandler) Delete(w http.ResponseWriter, r *http.Request) {
	userID, err := middleware.UserIDFrom(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	if err := h.svc.Delete(r.Context(), userID, chi.URLParam(r, "code")); err != nil {
		httpx.Error(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// parseLimit tolerates junk: ClampPageSize turns anything out of range into a
// sane default, so `?limit=abc` is a 20-row page, not a 400.
func parseLimit(raw string) int {
	if raw == "" {
		return domain.DefaultPageSize
	}
	n := 0
	for _, r := range raw {
		if r < '0' || r > '9' {
			return domain.DefaultPageSize
		}
		n = n*10 + int(r-'0')
		if n > domain.MaxPageSize {
			return domain.MaxPageSize
		}
	}
	return n
}

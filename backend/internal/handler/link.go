// Package handler translates HTTP to service calls and back. It never touches
// SQL, never picks a status code for an error (httpx does), and never contains
// business rules.
package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/IsuruSh/linkr/internal/domain"
	"github.com/IsuruSh/linkr/internal/httpx"
	"github.com/IsuruSh/linkr/internal/middleware"
	"github.com/IsuruSh/linkr/internal/service"
)

type LinkHandler struct {
	svc     *service.LinkService
	baseURL string
}

func NewLinkHandler(svc *service.LinkService, baseURL string) *LinkHandler {
	return &LinkHandler{svc: svc, baseURL: baseURL}
}

// Routes returns the /api/links group. Every route in it is protected, so the
// middleware is applied once to the whole sub-router rather than repeated per
// route — a route added later cannot forget it.
//
// requireAuth is passed in rather than stored on the handler so the mount site
// in main.go shows, in one line, that this group is guarded.
func (h *LinkHandler) Routes(requireAuth func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()
	r.Use(requireAuth)

	r.Post("/", h.Create)
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
		httpx.Error(w, r, err)
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

	link, err := h.svc.Create(r.Context(), userID, req.URL, req.Alias)
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

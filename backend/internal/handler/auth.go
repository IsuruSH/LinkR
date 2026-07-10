package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/IsuruSh/linkr/internal/httpx"
	"github.com/IsuruSh/linkr/internal/service"
)

type AuthHandler struct {
	svc *service.AuthService
}

func NewAuthHandler(svc *service.AuthService) *AuthHandler {
	return &AuthHandler{svc: svc}
}

// Routes returns the /api/auth group. Both routes are public by definition:
// you cannot present a token before you have one.
func (h *AuthHandler) Routes() chi.Router {
	r := chi.NewRouter()

	r.Post("/register", h.Register)
	r.Post("/login", h.Login)

	return r
}

// Register handles POST /api/auth/register.
//
// It returns the token directly rather than setting a cookie. Cookie management
// is the Next.js BFF's job: it receives this token server-side and re-issues it
// as an httpOnly cookie the browser never reads. Setting the cookie here would
// tie the API to one frontend's session model and would not survive a native
// client. See DECISIONS.md.
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}

	token, err := h.svc.Register(r.Context(), req.Email, req.Password)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toTokenResponse(token))
}

// Login handles POST /api/auth/login.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Error(w, r, err)
		return
	}

	token, err := h.svc.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toTokenResponse(token))
}

func toTokenResponse(t service.Token) tokenResponse {
	return tokenResponse{
		AccessToken: t.AccessToken,
		TokenType:   "Bearer",
		ExpiresAt:   t.ExpiresAt,
		User: userDTO{
			ID:    t.UserID.String(),
			Email: t.Email,
		},
	}
}

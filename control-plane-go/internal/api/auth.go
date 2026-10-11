package api

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"sync"

	"infrapilot/internal/auth"
	"infrapilot/internal/store"
)

type authUser struct {
	ID    string  `json:"id"`
	Email string  `json:"email"`
	Name  *string `json:"name"`
}

type authResponse struct {
	Token        string   `json:"token"`
	RefreshToken string   `json:"refreshToken"`
	User         authUser `json:"user"`
}

const (
	devEmail     = "dev@local"
	minPasswordB = 8
	maxPasswordB = 72 // bcrypt reads only the first 72 bytes
)

func validEmail(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s
}

// dummyHash is a valid bcrypt hash (same cost as real ones) that no password
// matches. Login compares against it when the email is unknown, so unknown
// emails cost as much as wrong passwords (SEC-3).
var dummyHash = sync.OnceValue(func() string {
	h, err := auth.HashPassword("dummy-password-for-timing-only")
	if err != nil {
		panic(err) // only fails if the random source does
	}
	return h
})

// issueTokens returns a new access token and a new stored refresh token. The
// stored expiry comes from auth.RefreshTTL, the same value the UI is told.
func (s *Server) issueTokens(ctx context.Context, userID string) (string, string, error) {
	access, err := s.Signer.Sign(userID)
	if err != nil {
		return "", "", err
	}
	refresh, hash, err := auth.NewRefreshToken()
	if err != nil {
		return "", "", err
	}
	if err := s.Store.CreateRefreshToken(ctx, userID, hash, auth.RefreshTTL); err != nil {
		return "", "", err
	}
	return access, refresh, nil
}

func (s *Server) writeAuth(w http.ResponseWriter, r *http.Request, u store.User) {
	access, refresh, err := s.issueTokens(r.Context(), u.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, authResponse{Token: access, RefreshToken: refresh,
		User: authUser{ID: u.ID, Email: u.Email, Name: u.Name}})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string  `json:"email"`
		Password string  `json:"password"`
		Name     *string `json:"name"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeDecodeError(w, err, "invalid_request")
		return
	}
	if !validEmail(body.Email) || len(body.Password) < minPasswordB || len(body.Password) > maxPasswordB {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if body.Email == devEmail { // reserved for POST /dev/login
		writeError(w, http.StatusConflict, "email_already_exists")
		return
	}
	hash, err := auth.HashPassword(body.Password)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	u, err := s.Store.CreateUser(r.Context(), body.Email, hash, body.Name)
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "email_already_exists")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	s.writeAuth(w, r, u)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeDecodeError(w, err, "invalid_request")
		return
	}
	if !validEmail(body.Email) {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	u, err := s.Store.GetUserByEmail(r.Context(), body.Email)
	switch {
	case errors.Is(err, store.ErrNotFound):
		auth.CheckPassword(dummyHash(), body.Password) // equalise timing; result ignored
		writeError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	case !auth.CheckPassword(u.Password, body.Password):
		writeError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	s.writeAuth(w, r, u)
}

// refresh exchanges a refresh token for a new access + refresh token pair.
// Each refresh token works once. Unknown, expired, revoked and replayed tokens
// all get the same 401 body. Replaying a consumed token revokes all of that
// user's tokens (the store does this); the token itself is never logged.
func (s *Server) refresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeDecodeError(w, err, "invalid_request")
		return
	}
	if body.RefreshToken == "" {
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	userID, err := s.Store.ConsumeRefreshToken(r.Context(), auth.HashRefreshToken(body.RefreshToken))
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrTokenReused) {
		if errors.Is(err, store.ErrTokenReused) {
			s.Log.Warn("refresh token reused; revoked all sessions of the user", "requestId", requestID(r))
		}
		writeError(w, http.StatusUnauthorized, "invalid_refresh_token")
		return
	}
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	access, refresh, err := s.issueTokens(r.Context(), userID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": access, "refreshToken": refresh})
}

// logout revokes one refresh token. It is idempotent and answers {"ok":true}
// for unknown tokens too.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeDecodeError(w, err, "invalid_request")
		return
	}
	if body.RefreshToken != "" {
		if err := s.Store.RevokeRefreshToken(r.Context(), auth.HashRefreshToken(body.RefreshToken)); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// devLogin finds or creates dev@local. Registered only when Deps.DevLogin is
// set (APP_ENV=development).
func (s *Server) devLogin(w http.ResponseWriter, r *http.Request) {
	u, err := s.Store.UpsertDevUser(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	access, refresh, err := s.issueTokens(r.Context(), u.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": access, "refreshToken": refresh, "userId": u.ID})
}

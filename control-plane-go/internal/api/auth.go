package api

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"unicode/utf8"

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
	maxPasswordB = 72  // bcrypt reads only the first 72 bytes
	maxEmailB    = 254 // RFC 5321 path limit
	maxNameRunes = 100
)

func validEmail(s string) bool {
	if len(s) > maxEmailB { // cheap cap before parsing
		return false
	}
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
	switch {
	case !validEmail(body.Email):
		writeError(w, http.StatusBadRequest, "invalid_request")
		return
	case len(body.Password) < minPasswordB:
		writeError(w, http.StatusBadRequest, "password_too_short")
		return
	case len(body.Password) > maxPasswordB:
		writeError(w, http.StatusBadRequest, "password_too_long")
		return
	case body.Name != nil && utf8.RuneCountInString(*body.Name) > maxNameRunes:
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
// all get the same 401 body. The store rotates in one transaction. A replay
// inside auth.RefreshGrace (two tabs racing) revokes nothing; a replay after it
// revokes the token's family (the store does this). The token is never logged.
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
	refresh, newHash, err := auth.NewRefreshToken()
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	userID, err := s.Store.RotateRefreshToken(r.Context(), auth.HashRefreshToken(body.RefreshToken), newHash, auth.RefreshTTL)
	switch {
	case errors.Is(err, store.ErrReplayInGrace):
		s.Log.Info("refresh token replayed inside grace window", "requestId", requestID(r))
		writeError(w, http.StatusUnauthorized, "invalid_refresh_token")
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusUnauthorized, "invalid_refresh_token")
		return
	case errors.Is(err, store.ErrTokenReused):
		s.Log.Warn("refresh token reused; revoked its family", "requestId", requestID(r))
		writeError(w, http.StatusUnauthorized, "invalid_refresh_token")
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}
	access, err := s.Signer.Sign(userID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": access, "refreshToken": refresh})
}

// logout revokes the presenter's refresh-token family. It is idempotent and
// answers {"ok":true} for unknown tokens too.
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := decodeJSON(w, r, &body); err != nil {
		writeDecodeError(w, err, "invalid_request")
		return
	}
	if body.RefreshToken != "" {
		if err := s.Store.RevokeRefreshFamily(r.Context(), auth.HashRefreshToken(body.RefreshToken)); err != nil {
			s.internalError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// loopbackHost reports whether a Host header names this machine: localhost,
// 127.0.0.1 or [::1], with any port.
func loopbackHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil { // no port: a bare name, or a bracketed v6 literal
		host = strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]")
	}
	return strings.EqualFold(host, "localhost") || host == "127.0.0.1" || host == "::1"
}

// devHostOnly refuses a request whose Host is not loopback. /dev/login needs no
// credentials, so a hostile page that rebinds its DNS name to 127.0.0.1 must
// not be able to call it from the developer's browser.
func devHostOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			writeError(w, http.StatusForbidden, "forbidden_host")
			return
		}
		next(w, r)
	}
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

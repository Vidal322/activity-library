package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/Vidal322/activity-library/internal/auth"
	"github.com/Vidal322/activity-library/internal/store"
)

type ctxKey int

const (
	userIDKey ctxKey = iota
)

func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookieName)
		if err != nil {
			s.writeUnauthorized(w, msgNotAuthenticated)
			return
		}

		tokenHash := auth.HashToken(c.Value)

		session, err := store.GetSession(r.Context(), s.pool, tokenHash)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				s.clearSessionCookie(w)
				s.writeUnauthorized(w, msgNotAuthenticated)
				return
			}
			s.writeStoreError(w, err)
			return
		}

		ctx := context.WithValue(r.Context(), userIDKey, session.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func userIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(userIDKey).(string)
	return id, ok
}

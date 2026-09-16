package api

import "net/http"

const sessionCookieName = "activity_library_session"

func (s *Server) sessionCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		MaxAge:   maxAge,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.config.Session.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, s.sessionCookie(token, int(s.config.Session.TTL.Seconds())))
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, s.sessionCookie("", -1))
}

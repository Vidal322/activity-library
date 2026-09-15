package api

import "net/http"

type loginRequest struct {
	Email    string
	Password string
}

func (loginRequest) validate() string {
	return ""
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
}

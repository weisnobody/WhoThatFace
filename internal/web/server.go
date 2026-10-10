// Package web implements the authenticated local review HTTP service.
package web

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"

	"github.com/pjg/face_me_bro/internal/auth"
)

type Server struct {
	auth      auth.Store
	templates *template.Template
}

func NewServer(authStore auth.Store) (*Server, error) {
	page := template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>{{.Title}}</title>
<style>body{font-family:system-ui,sans-serif;max-width:70rem;margin:2rem auto;padding:0 1rem}nav{display:flex;justify-content:space-between}table{border-collapse:collapse}td,th{padding:.4rem;border:1px solid #ccc}input,button{padding:.4rem;margin:.2rem 0}.error{color:#a00}</style></head>
<body><nav><a href="/">Face-Me-Bro</a>{{if .Account}}<span>{{.Account.Username}} ({{.Account.Role}}) <form method="post" action="/logout" style="display:inline"><input type="hidden" name="csrf" value="{{.CSRF}}"><button>Log out</button></form></span>{{end}}</nav><main>{{.Body}}</main></body></html>`))
	return &Server{auth: authStore, templates: page}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /", s.dashboard)
	return securityHeaders(mux)
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "face-me-bro"})
}

func (s *Server) loginPage(w http.ResponseWriter, r *http.Request) {
	csrf, err := csrfToken()
	if err != nil {
		http.Error(w, "unable to create form", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "csrf", Value: csrf, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
	s.render(w, map[string]any{"Title": "Log in", "Body": template.HTML(fmt.Sprintf(`<h1>Log in</h1><form method="post" action="/login"><input type="hidden" name="csrf" value="%s"><label>Username<br><input name="username" autocomplete="username" required></label><br><label>Password<br><input type="password" name="password" autocomplete="current-password" required></label><br><button>Log in</button></form>`, template.HTMLEscapeString(csrf)))})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	account, err := s.auth.Authenticate(r.Context(), r.FormValue("username"), r.FormValue("password"))
	if err != nil {
		s.render(w, map[string]any{"Title": "Log in", "Body": template.HTML(`<p class="error">Login failed.</p><a href="/login">Try again</a>`)})
		return
	}
	token, _, err := s.auth.CreateSession(r.Context(), account.ID)
	if err != nil {
		http.Error(w, "unable to create session", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "session", Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 7 * 24 * 60 * 60})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if cookie, err := r.Cookie("session"); err == nil {
		_ = s.auth.Logout(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	csrf := ""
	if cookie, err := r.Cookie("csrf"); err == nil {
		csrf = cookie.Value
	}
	s.render(w, map[string]any{
		"Title": "Review dashboard", "Account": account, "CSRF": csrf,
		"Body": template.HTML(`<h1>Review dashboard</h1><p>Authenticated review service is ready.</p><ul><li>Photo and face review endpoints are available to application services.</li><li>Cluster review and reference selection are protected by account permissions.</li></ul>`),
	})
}

func (s *Server) requireAccount(w http.ResponseWriter, r *http.Request) (auth.Account, bool) {
	cookie, err := r.Cookie("session")
	if err != nil {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return auth.Account{}, false
	}
	account, err := s.auth.ValidateSession(r.Context(), cookie.Value)
	if err != nil {
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return auth.Account{}, false
	}
	return account, true
}

func (s *Server) render(w http.ResponseWriter, data map[string]any) {
	if err := s.templates.ExecuteTemplate(w, "page", data); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

func csrfToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func validCSRF(r *http.Request) bool {
	cookie, err := r.Cookie("csrf")
	return err == nil && cookie.Value != "" && cookie.Value == r.FormValue("csrf")
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; form-action 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

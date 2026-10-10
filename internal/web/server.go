// Package web implements the authenticated local review HTTP service.
package web

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/pjg/face_me_bro/internal/auth"
	"github.com/pjg/face_me_bro/internal/review"
	"github.com/pjg/face_me_bro/internal/search"
)

type Server struct {
	auth      auth.Store
	db        *sql.DB
	templates *template.Template
}

func NewServer(authStore auth.Store, database *sql.DB) (*Server, error) {
	page := template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>{{.Title}}</title>
<style>body{font-family:system-ui,sans-serif;max-width:70rem;margin:2rem auto;padding:0 1rem}nav{display:flex;justify-content:space-between}table{border-collapse:collapse}td,th{padding:.4rem;border:1px solid #ccc}input,button{padding:.4rem;margin:.2rem 0}.error{color:#a00}</style></head>
<body><nav><a href="/">Face-Me-Bro</a>{{if .Account}}<span>{{.Account.Username}} ({{.Account.Role}}) <form method="post" action="/logout" style="display:inline"><input type="hidden" name="csrf" value="{{.CSRF}}"><button>Log out</button></form></span>{{end}}</nav><main>{{.Body}}</main></body></html>`))
	return &Server{auth: authStore, db: database, templates: page}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /login", s.loginPage)
	mux.HandleFunc("POST /login", s.login)
	mux.HandleFunc("GET /change-password", s.changePasswordPage)
	mux.HandleFunc("POST /change-password", s.changePassword)
	mux.HandleFunc("POST /logout", s.logout)
	mux.HandleFunc("GET /api/photos", s.photos)
	mux.HandleFunc("GET /api/photos/{pathID}", s.photo)
	mux.HandleFunc("POST /api/faces/{faceID}/assignment", s.assignFace)
	mux.HandleFunc("POST /api/faces/{faceID}/reference", s.referenceFace)
	mux.HandleFunc("GET /", s.dashboard)
	mux.HandleFunc("GET /review/photos", s.reviewPhotos)
	mux.HandleFunc("GET /review/people", s.reviewPeople)
	mux.HandleFunc("GET /review/clusters", s.reviewClusters)
	mux.HandleFunc("GET /review/clusters/{clusterID}", s.reviewCluster)
	mux.HandleFunc("POST /api/clusters/{clusterID}/review", s.reviewClusterAPI)
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

func (s *Server) changePasswordPage(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	csrf := ""
	if cookie, err := r.Cookie("csrf"); err == nil {
		csrf = cookie.Value
	}
	body := template.HTML(fmt.Sprintf(`<h1>Change password</h1><p>Account %s must set a new password before continuing.</p><form method="post" action="/change-password"><input type="hidden" name="csrf" value="%s"><label>New password<br><input type="password" name="password" required></label><br><button>Change password</button></form>`, template.HTMLEscapeString(account.Username), template.HTMLEscapeString(csrf)))
	s.render(w, map[string]any{"Title": "Change password", "Account": account, "CSRF": csrf, "Body": body})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	if !validCSRF(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	if err := s.auth.ResetPassword(r.Context(), account.ID, r.FormValue("password"), false); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
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
		"Body": template.HTML(`<h1>Review dashboard</h1><p>Choose a review view:</p><ul><li><a href="/review/photos">Photos and faces</a></li><li><a href="/review/people">People</a></li><li><a href="/review/clusters">Clusters</a></li></ul>`),
	})
}

func (s *Server) reviewPhotos(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	result, err := search.NewStore(s.db).Photos(r.Context(), search.Options{CollectionID: query.Get("collection_id"), RelativePrefix: query.Get("directory"), IncludeProvisional: query.Get("include_provisional") == "true", IncludeDontTrack: query.Get("include_dont_track") == "true"})
	if err != nil {
		http.Error(w, "search failed", http.StatusInternalServerError)
		return
	}
	var body strings.Builder
	body.WriteString(`<h1>Photos</h1><p>Review faces and use the face API to assign people or select references.</p>`)
	for _, photo := range result {
		body.WriteString(`<figure><a href="/api/photos/` + template.HTMLEscapeString(photo.PathID) + `"><img loading="lazy" style="max-width:20rem;max-height:15rem" src="/api/photos/` + template.HTMLEscapeString(photo.PathID) + `"></a><figcaption>` + template.HTMLEscapeString(photo.RelativePath) + ` (` + fmt.Sprint(photo.FaceCount) + ` faces)</figcaption></figure>`)
	}
	s.render(w, map[string]any{"Title": "Photos", "Account": account, "Body": template.HTML(body.String())})
}

func (s *Server) reviewPeople(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT id, COALESCE(name, ''), is_provisional, dont_track FROM people ORDER BY name, id`)
	if err != nil {
		http.Error(w, "people lookup failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var body strings.Builder
	body.WriteString(`<h1>People</h1><table><tr><th>Name</th><th>Provisional</th><th>Don't track</th></tr>`)
	for rows.Next() {
		var id, name string
		var provisional, dontTrack int
		if err := rows.Scan(&id, &name, &provisional, &dontTrack); err != nil {
			http.Error(w, "people lookup failed", http.StatusInternalServerError)
			return
		}
		body.WriteString(`<tr><td>` + template.HTMLEscapeString(name) + `</td><td>` + fmt.Sprint(provisional == 1) + `</td><td>` + fmt.Sprint(dontTrack == 1) + `</td></tr>`)
	}
	body.WriteString(`</table>`)
	s.render(w, map[string]any{"Title": "People", "Account": account, "Body": template.HTML(body.String())})
}

func (s *Server) reviewClusters(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT c.id, c.cluster_key, c.purity, c.connectivity, c.is_mixed, c.review_status FROM clusters c JOIN clustering_runs cr ON cr.id = c.clustering_run_id WHERE cr.status = 'active' ORDER BY c.is_mixed DESC, c.cluster_key`)
	if err != nil {
		http.Error(w, "cluster lookup failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var body strings.Builder
	body.WriteString(`<h1>Clusters</h1><table><tr><th>Cluster</th><th>Purity</th><th>Connectivity</th><th>Mixed</th><th>Status</th></tr>`)
	for rows.Next() {
		var id, key, status string
		var purity, connectivity sql.NullFloat64
		var mixed int
		if err := rows.Scan(&id, &key, &purity, &connectivity, &mixed, &status); err != nil {
			http.Error(w, "cluster lookup failed", http.StatusInternalServerError)
			return
		}
		body.WriteString(`<tr><td><a href="/review/clusters/` + template.HTMLEscapeString(id) + `">` + template.HTMLEscapeString(key) + `</a></td><td>` + fmt.Sprint(purity.Float64) + `</td><td>` + fmt.Sprint(connectivity.Float64) + `</td><td>` + fmt.Sprint(mixed == 1) + `</td><td>` + template.HTMLEscapeString(status) + `</td></tr>`)
	}
	body.WriteString(`</table>`)
	s.render(w, map[string]any{"Title": "Clusters", "Account": account, "Body": template.HTML(body.String())})
}

func (s *Server) reviewCluster(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	clusterID := r.PathValue("clusterID")
	rows, err := s.db.QueryContext(r.Context(), `SELECT cm.face_id, COALESCE(ca.state, 'unassigned'), COALESCE(ca.person_id, '') FROM cluster_memberships cm LEFT JOIN current_face_assignments ca ON ca.face_id = cm.face_id WHERE cm.cluster_id = ? ORDER BY cm.face_id`, clusterID)
	if err != nil {
		http.Error(w, "cluster lookup failed", 500)
		return
	}
	defer rows.Close()
	var body strings.Builder
	body.WriteString(`<h1>Cluster ` + template.HTMLEscapeString(clusterID) + `</h1><table><tr><th>Face</th><th>State</th><th>Person</th></tr>`)
	for rows.Next() {
		var faceID, state, personID string
		if err := rows.Scan(&faceID, &state, &personID); err != nil {
			http.Error(w, "cluster lookup failed", 500)
			return
		}
		body.WriteString(`<tr><td>` + template.HTMLEscapeString(faceID) + `</td><td>` + template.HTMLEscapeString(state) + `</td><td>` + template.HTMLEscapeString(personID) + `</td></tr>`)
	}
	body.WriteString(`</table><p>Review decisions can be submitted through the authenticated cluster review API.</p>`)
	s.render(w, map[string]any{"Title": "Cluster review", "Account": account, "Body": template.HTML(body.String())})
}

func (s *Server) reviewClusterAPI(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	if !validCSRFApi(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	clusterID := r.PathValue("clusterID")
	var collectionID string
	if err := s.db.QueryRowContext(r.Context(), `SELECT pp.collection_id FROM cluster_memberships cm JOIN faces f ON f.id = cm.face_id JOIN analyses a ON a.id = f.analysis_id JOIN photo_assets pa ON pa.id = a.asset_id JOIN photo_paths pp ON pp.asset_id = pa.id WHERE cm.cluster_id = ? LIMIT 1`, clusterID).Scan(&collectionID); err != nil {
		http.Error(w, "cluster not found", http.StatusNotFound)
		return
	}
	allowed, err := s.auth.CanAccessCollection(r.Context(), account.ID, collectionID, true, false)
	if err != nil || !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var input struct {
		DefaultPersonID string                       `json:"default_person_id"`
		Status          string                       `json:"status"`
		Note            string                       `json:"note"`
		Decisions       []review.ClusterFaceDecision `json:"decisions"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid review", http.StatusBadRequest)
		return
	}
	if err := review.NewStore(s.db).ReviewCluster(r.Context(), clusterID, input.DefaultPersonID, input.Status, input.Decisions, review.Actor{Type: "user", AccountID: account.ID}, input.Note); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cluster_id": clusterID, "status": input.Status})
}

func (s *Server) photos(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireAccount(w, r); !ok {
		return
	}
	query := r.URL.Query()
	options := search.Options{
		CollectionID: query.Get("collection_id"), PersonID: query.Get("person_id"),
		RelativePrefix: query.Get("directory"), CaptureDateFrom: query.Get("capture_date_from"),
		CaptureDateUntil:   query.Get("capture_date_until"),
		IncludeProvisional: query.Get("include_provisional") == "true",
		IncludeUnknown:     query.Get("include_unknown") == "true",
		IncludeAmbiguous:   query.Get("include_ambiguous") == "true",
		IncludeDontTrack:   query.Get("include_dont_track") == "true",
		OnlyConfirmed:      query.Get("only_confirmed") == "true",
		OnlyUnconfirmed:    query.Get("only_unconfirmed") == "true",
	}
	result, err := search.NewStore(s.db).Photos(r.Context(), options)
	if err != nil {
		http.Error(w, "search failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) photo(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	pathID := r.PathValue("pathID")
	var collectionID, root, relative string
	err := s.db.QueryRowContext(r.Context(), `SELECT pp.collection_id, c.root_path, pp.relative_path FROM photo_paths pp JOIN collections c ON c.id = pp.collection_id WHERE pp.id = ? AND pp.missing_at IS NULL`, pathID).Scan(&collectionID, &root, &relative)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "photo lookup failed", http.StatusInternalServerError)
		return
	}
	allowed, err := s.auth.CanAccessCollection(r.Context(), account.ID, collectionID, false, false)
	if err != nil || !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	root, err = filepath.Abs(root)
	if err != nil {
		http.Error(w, "invalid collection root", http.StatusInternalServerError)
		return
	}
	target, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		http.Error(w, "invalid photo path", http.StatusInternalServerError)
		return
	}
	within, err := filepath.Rel(root, target)
	if err != nil || within == ".." || len(within) >= 3 && within[:3] == ".."+string(filepath.Separator) {
		http.Error(w, "invalid photo path", http.StatusForbidden)
		return
	}
	http.ServeFile(w, r, target)
}

func (s *Server) assignFace(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	if !validCSRFApi(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	faceID := r.PathValue("faceID")
	allowed, err := s.facePermission(r, account, faceID, true)
	if err != nil || !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var input struct {
		PersonID string                 `json:"person_id"`
		State    review.AssignmentState `json:"state"`
		Reject   bool                   `json:"reject"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid assignment", http.StatusBadRequest)
		return
	}
	store := review.NewStore(s.db)
	var assignment review.Assignment
	if input.Reject {
		assignment, err = store.Reject(r.Context(), faceID, review.Actor{Type: "user", AccountID: account.ID}, "web-review")
	} else {
		assignment, err = store.Assign(r.Context(), faceID, input.PersonID, input.State, review.Actor{Type: "user", AccountID: account.ID}, review.Evidence{}, "web-review")
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, assignment)
}

func (s *Server) referenceFace(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	if !validCSRFApi(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	faceID := r.PathValue("faceID")
	allowed, err := s.facePermission(r, account, faceID, true)
	if err != nil || !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var input struct {
		PersonID  string `json:"person_id"`
		Reference bool   `json:"reference"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid reference selection", http.StatusBadRequest)
		return
	}
	if input.PersonID == "" {
		http.Error(w, "person_id is required", http.StatusBadRequest)
		return
	}
	if err := review.NewStore(s.db).SetReference(r.Context(), faceID, input.PersonID, input.Reference, review.Actor{Type: "user", AccountID: account.ID}, "web-review"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"face_id": faceID, "person_id": input.PersonID, "reference": input.Reference})
}

func (s *Server) facePermission(r *http.Request, account auth.Account, faceID string, modify bool) (bool, error) {
	rows, err := s.db.QueryContext(r.Context(), `SELECT DISTINCT pp.collection_id FROM faces f JOIN analyses a ON a.id = f.analysis_id JOIN photo_assets pa ON pa.id = a.asset_id JOIN photo_paths pp ON pp.asset_id = pa.id WHERE f.id = ?`, faceID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var collectionID string
		if err := rows.Scan(&collectionID); err != nil {
			return false, err
		}
		found = true
		allowed, err := s.auth.CanAccessCollection(r.Context(), account.ID, collectionID, modify, false)
		if err != nil {
			return false, err
		}
		if allowed {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	return false, fmt.Errorf("face %q is not in an accessible collection (found=%t)", faceID, found)
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
	if account.MustChangePassword && r.URL.Path != "/change-password" {
		http.Redirect(w, r, "/change-password", http.StatusSeeOther)
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

func validCSRFApi(r *http.Request) bool {
	if token := r.Header.Get("X-CSRF-Token"); token != "" {
		cookie, err := r.Cookie("csrf")
		return err == nil && cookie.Value == token
	}
	return validCSRF(r)
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

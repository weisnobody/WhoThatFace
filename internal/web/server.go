// Package web implements the authenticated local review HTTP service.
package web

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pjg/face_me_bro/internal/audit"
	"github.com/pjg/face_me_bro/internal/auth"
	"github.com/pjg/face_me_bro/internal/id"
	"github.com/pjg/face_me_bro/internal/jobs"
	"github.com/pjg/face_me_bro/internal/representative"
	"github.com/pjg/face_me_bro/internal/review"
	"github.com/pjg/face_me_bro/internal/search"
)

type Server struct {
	auth       auth.Store
	db         *sql.DB
	pythonPath string
	templates  *template.Template
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func splitCSV(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func NewServer(authStore auth.Store, database *sql.DB) (*Server, error) {
	return newServer(authStore, database, "")
}

func NewServerWithPython(authStore auth.Store, database *sql.DB, pythonPath string) (*Server, error) {
	return newServer(authStore, database, pythonPath)
}

func newServer(authStore auth.Store, database *sql.DB, pythonPath string) (*Server, error) {
	//  body{font-family:system-ui,sans-serif;max-width:70rem;margin:2rem auto;padding:0 1rem}

	page := template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>{{.Title}}</title>
<style>
 body { font: 14px system-ui; margin: 20px; color: #222 }
 .page-header { position: sticky; top: 0; z-index: 10; background: #fff; padding: 2px 0}
 nav{display:flex;justify-content:space-between}
 table{border-collapse:collapse}
 td,th{padding:.4rem;border:1px solid #ccc}
 input,button{padding:.4rem;margin:.2rem 0}
 .error{color:#a00}
</style>{{with .PageStyle}}<style>{{.}}</style>{{end}}</head>
<body><nav><a href="/">Face-Me-Bro</a>{{if .Account}}<span>{{.Account.Username}} <form method="post" action="/logout" style="display:inline"><input type="hidden" name="csrf" value="{{.CSRF}}"><button>Log out</button></form></span>{{end}}</nav><main>{{.Body}}</main></body></html>`))
	return &Server{auth: authStore, db: database, pythonPath: pythonPath, templates: page}, nil
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
	mux.HandleFunc("GET /static/person.js", s.personScript)
	mux.HandleFunc("GET /static/photo.js", s.photoScript)
	mux.HandleFunc("GET /static/cluster.js", s.clusterScript)
	mux.HandleFunc("POST /api/faces/{faceID}/assignment", s.assignFace)
	mux.HandleFunc("POST /api/faces/{faceID}/disposition", s.faceDisposition)
	mux.HandleFunc("POST /api/faces/{faceID}/reference", s.referenceFace)
	mux.HandleFunc("GET /", s.dashboard)
	mux.HandleFunc("GET /review/photos", s.reviewPhotos)
	mux.HandleFunc("GET /review/people", s.reviewPeople)
	mux.HandleFunc("GET /review/people/{personID}", s.reviewPerson)
	mux.HandleFunc("GET /review/people/{personID}/references", s.reviewPersonReferences)
	mux.HandleFunc("POST /api/people", s.createPerson)
	mux.HandleFunc("POST /api/people/{personID}", s.renamePerson)
	mux.HandleFunc("POST /api/people/{personID}/reference-reprocess", s.referenceReprocess)
	mux.HandleFunc("GET /api/people/{personID}/references", s.personReferences)
	mux.HandleFunc("GET /api/people/{personID}/representative", s.personRepresentative)
	mux.HandleFunc("POST /api/people/{personID}/representative", s.setPersonRepresentative)
	mux.HandleFunc("GET /review/clusters", s.reviewClusters)
	mux.HandleFunc("GET /review/clusters/{clusterID}", s.reviewCluster)
	mux.HandleFunc("POST /api/clusters/{clusterID}/review", s.reviewClusterAPI)
	mux.HandleFunc("POST /api/audit/{eventID}/undo", s.undoAudit)
	mux.HandleFunc("GET /admin/accounts", s.adminAccounts)
	mux.HandleFunc("GET /admin/collections", s.adminCollections)
	mux.HandleFunc("POST /api/admin/accounts", s.adminCreateAccount)
	mux.HandleFunc("POST /api/admin/accounts/{accountID}/password", s.adminResetPassword)
	mux.HandleFunc("POST /api/admin/accounts/{accountID}/disabled", s.adminDisableAccount)
	mux.HandleFunc("POST /api/admin/permissions", s.adminPermission)
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
	if pathID := query.Get("path_id"); pathID != "" {
		s.reviewPhotoDetail(w, r, pathID)
		return
	}
	allowed, err := s.auth.AccessibleCollections(r.Context(), account.ID)
	if err != nil {
		http.Error(w, "permission lookup failed", http.StatusInternalServerError)
		return
	}
	result, err := search.NewStore(s.db).Photos(r.Context(), search.Options{AllowedCollectionIDs: allowed, CollectionID: query.Get("collection_id"), RelativePrefix: query.Get("directory"), ReviewMode: true, IncludeAssignedSystem: query.Get("include_assigned_system") == "true", IncludeAssignedUser: query.Get("include_assigned_user") == "true", IncludeDontTrack: query.Get("include_dont_track") == "true"})
	if err != nil {
		http.Error(w, "search failed", http.StatusInternalServerError)
		return
	}
	var body strings.Builder
	body.WriteString(`<h1>Photos</h1><p>Review faces and use the face API to assign people or select references.</p>`)
	for _, photo := range result {
		body.WriteString(`<figure><a href="/review/photos?path_id=` + template.HTMLEscapeString(photo.PathID) + `"><img loading="lazy" style="max-width:20rem;max-height:15rem" src="/api/photos/` + template.HTMLEscapeString(photo.PathID) + `"></a><figcaption><a href="/review/photos?path_id=` + template.HTMLEscapeString(photo.PathID) + `">` + template.HTMLEscapeString(photo.RelativePath) + `</a> (` + fmt.Sprint(photo.FaceCount) + ` faces)</figcaption></figure>`)
	}
	s.render(w, map[string]any{"Title": "Photos", "Account": account, "Body": template.HTML(body.String())})
}

func (s *Server) reviewPhotoDetail(w http.ResponseWriter, r *http.Request, pathID string) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	var collectionID, relative string
	var width, height int
	if err := s.db.QueryRowContext(r.Context(), `SELECT pp.collection_id, pp.relative_path, COALESCE(pa.width, 0), COALESCE(pa.height, 0) FROM photo_paths pp JOIN photo_assets pa ON pa.id = pp.asset_id WHERE pp.id = ? AND pp.missing_at IS NULL`, pathID).Scan(&collectionID, &relative, &width, &height); err != nil {
		http.NotFound(w, r)
		return
	}
	allowed, err := s.auth.CanAccessCollection(r.Context(), account.ID, collectionID, false, false)
	if err != nil || !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT f.id, f.face_index, f.box_left, f.box_top, f.box_right, f.box_bottom, COALESCE(ca.state, 'unassigned'), COALESCE(ca.person_id, ''), COALESCE(NULLIF(p.name, ''), substr(ca.person_id, -8), ''), COALESCE(rc.similarity, 0), COALESCE(rc.margin, 0), COALESCE(cfr.reference, 0) FROM faces f JOIN analyses a ON a.id = f.analysis_id JOIN photo_assets pa ON pa.id = a.asset_id JOIN photo_paths pp ON pp.asset_id = pa.id LEFT JOIN current_face_assignments ca ON ca.face_id = f.id LEFT JOIN people p ON p.id = ca.person_id LEFT JOIN current_face_references cfr ON cfr.face_id = f.id AND cfr.person_id = ca.person_id LEFT JOIN recognition_candidates rc ON rc.id = (SELECT id FROM recognition_candidates WHERE face_id = f.id ORDER BY created_at DESC, rank LIMIT 1) WHERE pp.id = ? ORDER BY f.face_index`, pathID)
	if err != nil {
		http.Error(w, "face lookup failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	body := strings.Builder{}
	body.WriteString(`<style>.photo-detail{position:relative;display:inline-block}.photo-detail img{max-width:90vw;max-height:70vh}.photo-box{position:absolute;border:2px solid #f00;background:rgba(204,0,0,.15)}.photo-face{border:1px solid #ccc;padding:8px;margin:8px 0}</style><h1>Photo review</h1><p>` + template.HTMLEscapeString(relative) + `</p><div class="photo-detail"><img src="/api/photos/` + template.HTMLEscapeString(pathID) + `">`)
	type face struct {
		ID                       string
		Index                    int
		Left, Top, Right, Bottom float64
		State, PersonID, Person  string
		Similarity, Margin       float64
		Reference                bool
	}
	var faces []face
	for rows.Next() {
		var value face
		var reference int
		if err := rows.Scan(&value.ID, &value.Index, &value.Left, &value.Top, &value.Right, &value.Bottom, &value.State, &value.PersonID, &value.Person, &value.Similarity, &value.Margin, &reference); err != nil {
			return
		}
		value.Reference = reference == 1
		body.WriteString(fmt.Sprintf(`<span class="photo-box" style="left:%s%%;top:%s%%;width:%s%%;height:%s%%"></span>`, percent(value.Left, width), percent(value.Top, height), percent(value.Right-value.Left, width), percent(value.Bottom-value.Top, height)))
		faces = append(faces, value)
	}
	body.WriteString(`</div><div id="photo-page" data-csrf="` + template.HTMLEscapeString(csrfCookie(r)) + `" data-person-id="` + template.HTMLEscapeString(collectionID) + `">`)
	peopleQuery := `SELECT id, COALESCE(NULLIF(name, ''), substr(id, -8)) FROM people`
	peopleArgs := []any{collectionID}
	peopleQuery += ` WHERE EXISTS (SELECT 1 FROM current_face_assignments ca2 JOIN faces f2 ON f2.id = ca2.face_id JOIN analyses a2 ON a2.id = f2.analysis_id JOIN photo_assets pa2 ON pa2.id = a2.asset_id JOIN photo_paths pp2 ON pp2.asset_id = pa2.id WHERE ca2.person_id = people.id AND pp2.collection_id = ?)`
	peopleQuery += ` ORDER BY LOWER(COALESCE(NULLIF(name, ''), substr(id, -8))), id`
	peopleRows, peopleErr := s.db.QueryContext(r.Context(), peopleQuery, peopleArgs...)
	if peopleErr != nil {
		http.Error(w, "people lookup failed", http.StatusInternalServerError)
		return
	}
	type photoPerson struct{ ID, Name string }
	var people []photoPerson
	for peopleRows.Next() {
		var p photoPerson
		if err := peopleRows.Scan(&p.ID, &p.Name); err != nil {
			peopleRows.Close()
			http.Error(w, "people lookup failed", http.StatusInternalServerError)
			return
		}
		people = append(people, p)
	}
	peopleRows.Close()
	for _, value := range faces {
		boxClass := "photo-face"
		body.WriteString(`<article class="` + boxClass + `" data-face-id="` + template.HTMLEscapeString(value.ID) + `"><strong>Face ` + fmt.Sprint(value.Index+1) + `</strong>: ` + template.HTMLEscapeString(value.State) + ` · ` + template.HTMLEscapeString(value.Person) + ` · confidence ` + formatScore(value.Similarity) + `%, margin ` + formatScore(value.Margin) + `%`)
		if value.Reference {
			body.WriteString(` · reference`)
		}
		if account.Role != auth.Viewer {
			body.WriteString(`<div class="photo-actions"><button type="button" data-action="unknown" data-face-id="` + template.HTMLEscapeString(value.ID) + `">Unknown</button><button type="button" data-action="ambiguous" data-face-id="` + template.HTMLEscapeString(value.ID) + `">Ambiguous</button><button type="button" data-action="disposition" data-disposition="false-detection" data-face-id="` + template.HTMLEscapeString(value.ID) + `">False detection</button><button type="button" data-action="disposition" data-disposition="dont-track" data-face-id="` + template.HTMLEscapeString(value.ID) + `">Don't track</button><select data-assignment-person="` + template.HTMLEscapeString(value.ID) + `"><option value="">Unknown</option><option value="__ambiguous__">Ambiguous</option>`)
			for _, person := range people {
				body.WriteString(`<option value="` + template.HTMLEscapeString(person.ID) + `"` + selectedAttr(person.ID, value.PersonID) + `>` + template.HTMLEscapeString(person.Name) + `</option>`)
			}
			body.WriteString(`</select><button type="button" data-action="assign" data-face-id="` + template.HTMLEscapeString(value.ID) + `">Save</button>`)
			if value.PersonID != "" && (value.State == string(review.AssignedSystem) || value.State == string(review.AssignedUser)) {
				body.WriteString(`<button type="button" data-action="role" data-face-id="` + template.HTMLEscapeString(value.ID) + `" data-person-id="` + template.HTMLEscapeString(value.PersonID) + `" data-reference="` + fmt.Sprint(value.Reference) + `">` + map[bool]string{true: "Evaluation", false: "Reference"}[value.Reference] + `</button>`)
			}
			body.WriteString(`</div>`)
		}
		body.WriteString(`</article>`)
	}
	body.WriteString(`</div>`)
	if account.Role != auth.Viewer {
		body.WriteString(`<script src="/static/photo.js" defer></script>`)
	}
	s.render(w, map[string]any{"Title": "Photo review", "Account": account, "Body": template.HTML(body.String()), "PageStyle": template.CSS(`.photo-detail{position:relative;display:inline-block}.photo-detail img{max-width:90vw;max-height:70vh}.photo-box{position:absolute;border:2px solid #f00;background:rgba(204,0,0,.15)}.photo-box.reference{border-color:#0a2;background:rgba(0,170,34,.15)}.photo-face{border:1px solid #ccc;padding:8px;margin:8px 0}.photo-actions{display:flex;justify-content:space-between;gap:8px;align-items:center}`)})
}

func (s *Server) reviewPeople(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	allowed, err := s.auth.AccessibleCollections(r.Context(), account.ID)
	if err != nil {
		http.Error(w, "permission lookup failed", http.StatusInternalServerError)
		return
	}
	peopleQuery := `SELECT p.id, COALESCE(NULLIF(p.name, ''), substr(p.id, -8)), p.dont_track,
		COUNT(DISTINCT CASE WHEN pp.missing_at IS NULL THEN ca.face_id END),
		COUNT(DISTINCT CASE WHEN pp.missing_at IS NULL AND ca.state = 'assigned-user' THEN ca.face_id END),
		COUNT(DISTINCT CASE WHEN pp.missing_at IS NULL AND ca.state = 'assigned-system' THEN ca.face_id END),
		COUNT(DISTINCT CASE WHEN pp.missing_at IS NULL AND ca.state = 'provisional' THEN ca.face_id END)
		FROM people p
		LEFT JOIN current_face_assignments ca ON ca.person_id = p.id
		LEFT JOIN faces f ON f.id = ca.face_id
		LEFT JOIN analyses a ON a.id = f.analysis_id
		LEFT JOIN photo_assets pa ON pa.id = a.asset_id
		LEFT JOIN photo_paths pp ON pp.asset_id = pa.id
		WHERE `
	var args []any
	if allowed != nil {
		if len(allowed) == 0 {
			body := `<h1>People</h1><p>No collections are assigned to this account.</p>`
			s.render(w, map[string]any{"Title": "People", "Account": account, "Body": template.HTML(body)})
			return
		}
		placeholders := make([]string, len(allowed))
		for i, id := range allowed {
			placeholders[i] = "?"
			args = append(args, id)
		}
		peopleQuery += `EXISTS (SELECT 1 FROM faces f2 JOIN analyses a2 ON a2.id = f2.analysis_id JOIN photo_assets pa2 ON pa2.id = a2.asset_id JOIN photo_paths pp2 ON pp2.asset_id = pa2.id JOIN current_face_assignments ca2 ON ca2.face_id = f2.id WHERE ca2.person_id = p.id AND pp2.collection_id IN (` + strings.Join(placeholders, ",") + `))`
		for _, id := range allowed {
			args = append(args, id)
		}
	} else {
		peopleQuery += `1=1`
	}
	peopleQuery += ` GROUP BY p.id, p.name, p.dont_track`
	rows, err := s.db.QueryContext(r.Context(), peopleQuery, args...)
	if err != nil {
		http.Error(w, "people lookup failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type personRow struct {
		ID, Name                                    string
		DontTrack, Faces, User, System, Provisional int
	}
	var people []personRow
	for rows.Next() {
		var id, name string
		var row personRow
		if err := rows.Scan(&id, &name, &row.DontTrack, &row.Faces, &row.User, &row.System, &row.Provisional); err != nil {
			http.Error(w, "people lookup failed", http.StatusInternalServerError)
			return
		}
		row.ID, row.Name = id, name
		people = append(people, row)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "people lookup failed", http.StatusInternalServerError)
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	track := r.URL.Query().Get("track")
	filtered := people[:0]
	for _, person := range people {
		if q != "" && !strings.Contains(strings.ToLower(person.Name), q) {
			continue
		}
		if track == "tracked" && person.DontTrack != 0 {
			continue
		}
		if track == "dont-track" && person.DontTrack == 0 {
			continue
		}
		filtered = append(filtered, person)
	}
	people = filtered
	sortKey := r.URL.Query().Get("sort")
	if sortKey == "" {
		sortKey = "name"
	}
	desc := r.URL.Query().Get("direction") == "desc"
	sort.SliceStable(people, func(i, j int) bool {
		var left, right string
		switch sortKey {
		case "faces":
			left, right = fmt.Sprintf("%09d", people[i].Faces), fmt.Sprintf("%09d", people[j].Faces)
		case "user":
			left, right = fmt.Sprintf("%09d", people[i].User), fmt.Sprintf("%09d", people[j].User)
		case "system":
			left, right = fmt.Sprintf("%09d", people[i].System), fmt.Sprintf("%09d", people[j].System)
		case "provisional":
			left, right = fmt.Sprintf("%09d", people[i].Provisional), fmt.Sprintf("%09d", people[j].Provisional)
		default:
			left, right = strings.ToLower(people[i].Name), strings.ToLower(people[j].Name)
		}
		if left == right {
			left, right = people[i].ID, people[j].ID
		}
		if desc {
			return left > right
		}
		return left < right
	})
	total := len(people)
	perPage := 10
	if value, parseErr := strconv.Atoi(r.URL.Query().Get("per_page")); parseErr == nil && (value == 10 || value == 20 || value == 25 || value == 50 || value == 100) {
		perPage = value
	}
	page := 1
	if value, parseErr := strconv.Atoi(r.URL.Query().Get("page")); parseErr == nil && value > 0 {
		page = value
	}
	pageCount := (total + perPage - 1) / perPage
	if pageCount == 0 {
		pageCount = 1
	}
	if page > pageCount {
		page = pageCount
	}
	start := (page - 1) * perPage
	end := start + perPage
	if end > total {
		end = total
	}
	pagePeople := people
	if start < total {
		pagePeople = people[start:end]
	} else {
		pagePeople = nil
	}
	pageURL := func(target int) string {
		values := url.Values{}
		for key, list := range r.URL.Query() {
			for _, value := range list {
				values.Add(key, value)
			}
		}
		values.Set("page", strconv.Itoa(target))
		values.Set("per_page", strconv.Itoa(perPage))
		return "/review/people?" + values.Encode()
	}
	var body strings.Builder
	body.WriteString(`<h1>People (` + fmt.Sprint(len(pagePeople)) + `/` + fmt.Sprint(total) + `)</h1><form id="people-controls" method="get"><input name="q" value="` + template.HTMLEscapeString(r.URL.Query().Get("q")) + `" placeholder="Filter name"><select name="track"><option value="">All</option><option value="tracked"` + selectedAttr("tracked", track) + `>Tracked</option><option value="dont-track"` + selectedAttr("dont-track", track) + `>Don't track</option></select><label>Sort <select name="sort">`)
	for _, option := range []struct{ Value, Label string }{{"name", "Name"}, {"faces", "Faces"}, {"user", "User"}, {"system", "System"}, {"provisional", "Provisional"}} {
		body.WriteString(`<option value="` + option.Value + `"` + selectedAttr(option.Value, sortKey) + `>` + option.Label + `</option>`)
	}
	body.WriteString(`</select><select name="direction"><option value="asc"` + selectedAttr("asc", r.URL.Query().Get("direction")) + `>Ascending</option><option value="desc"` + selectedAttr("desc", r.URL.Query().Get("direction")) + `>Descending</option></select><select name="per_page" onchange="this.form.submit()">`)
	for _, size := range []int{10, 20, 25, 50, 100} {
		body.WriteString(`<option value="` + fmt.Sprint(size) + `"` + selectedAttr(fmt.Sprint(size), fmt.Sprint(perPage)) + `>` + fmt.Sprint(size) + `</option>`)
	}
	body.WriteString(`</select><button>Filter</button></form><form id="create-person"><input name="name" placeholder="New person name" required><button>Create person</button></form>`)
	renderPager := func() {
		body.WriteString(`<nav class="pager"><a href="` + template.HTMLEscapeString(pageURL(1)) + `">First</a> <a href="` + template.HTMLEscapeString(pageURL(maxInt(1, page-1))) + `">Previous</a> <span>Page ` + fmt.Sprint(page) + `/` + fmt.Sprint(pageCount) + `</span> <a href="` + template.HTMLEscapeString(pageURL(minInt(pageCount, page+1))) + `">Next</a> <a href="` + template.HTMLEscapeString(pageURL(pageCount)) + `">Last</a> <form class="page-jump" method="get"><input type="number" name="page" min="1" value="` + fmt.Sprint(page) + `" onkeydown="if(event.key==='Enter')this.form.submit()"><input type="hidden" name="per_page" value="` + fmt.Sprint(perPage) + `"><input type="hidden" name="q" value="` + template.HTMLEscapeString(r.URL.Query().Get("q")) + `"><input type="hidden" name="track" value="` + template.HTMLEscapeString(track) + `"><input type="hidden" name="sort" value="` + template.HTMLEscapeString(sortKey) + `"><input type="hidden" name="direction" value="` + template.HTMLEscapeString(r.URL.Query().Get("direction")) + `"></form></nav>`)
	}
	renderPager()
	body.WriteString(`<table><tr><th>Name</th><th>Representative</th><th>Faces</th><th>User</th><th>System</th><th>Provisional</th><th>Don't track</th></tr>`)
	for _, person := range pagePeople {
		dontTrack := ""
		if person.DontTrack != 0 {
			dontTrack = "X"
		}
		body.WriteString(`<tr><td><a href="/review/people/` + template.HTMLEscapeString(person.ID) + `">` + template.HTMLEscapeString(person.Name) + `</a></td><td><img loading="lazy" style="max-height:32px;width:auto" alt="" src="/api/people/` + template.HTMLEscapeString(person.ID) + `/representative"></td><td>` + fmt.Sprint(person.Faces) + `</td><td>` + fmt.Sprint(person.User) + `</td><td>` + fmt.Sprint(person.System) + `</td><td>` + fmt.Sprint(person.Provisional) + `</td><td>` + dontTrack + `</td></tr>`)
	}
	body.WriteString(`</table>`)
	renderPager()
	body.WriteString(`<script>(function(){const form=document.getElementById('create-person');form.onsubmit=async function(e){e.preventDefault();const response=await fetch('/api/people',{method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':'` + template.JSEscapeString(csrfCookie(r)) + `'},body:JSON.stringify({name:form.name.value})});if(!response.ok){alert(await response.text());return}location.reload()}})();</script>`)
	s.render(w, map[string]any{"Title": "People", "Account": account, "Body": template.HTML(body.String())})
}

func (s *Server) reviewPerson(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	personID := r.PathValue("personID")
	var personName string
	if err := s.db.QueryRowContext(r.Context(), `SELECT COALESCE(NULLIF(name, ''), substr(id, -8)) FROM people WHERE id = ?`, personID).Scan(&personName); err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, "person lookup failed", http.StatusInternalServerError)
		return
	}
	allowed, err := s.auth.AccessibleCollections(r.Context(), account.ID)
	if err != nil {
		http.Error(w, "permission lookup failed", http.StatusInternalServerError)
		return
	}
	if allowed != nil && len(allowed) == 0 {
		s.render(w, map[string]any{"Title": personName, "Account": account, "Body": template.HTML(`<h1>` + template.HTMLEscapeString(personName) + `</h1><p>No collections are assigned to this account.</p>`)})
		return
	}
	query := `SELECT f.id, pp.id, pp.relative_path, COALESCE(pa.width, a.source_width, 0), COALESCE(pa.height, a.source_height, 0),
		f.box_left, f.box_top, f.box_right, f.box_bottom, COALESCE(f.quality, 0), COALESCE(f.detection_confidence, 0),
		CASE WHEN ae.reason = 'cluster-provisional' AND COALESCE(ae.support_count, 0) = 0 AND COALESCE(ae.quality, 0) = 0 AND COALESCE(ae.detection_confidence, 0) = 0 AND ae.agreement IS NULL THEN NULL ELSE ae.similarity END, COALESCE(ca.state, 'unassigned'), COALESCE(ca.person_id, ''), COALESCE(cfr.reference, 0)
		FROM faces f JOIN analyses a ON a.id = f.analysis_id
		JOIN photo_assets pa ON pa.id = a.asset_id JOIN photo_paths pp ON pp.asset_id = pa.id
		JOIN current_face_assignments ca ON ca.face_id = f.id
		LEFT JOIN assignment_events ae ON ae.id = ca.event_id
		LEFT JOIN current_face_references cfr ON cfr.face_id = f.id AND cfr.person_id = ?
		WHERE ca.person_id = ? AND pp.missing_at IS NULL`
	args := []any{personID, personID}
	if allowed != nil {
		query += ` AND pp.collection_id IN (` + strings.TrimRight(strings.Repeat("?,", len(allowed)), ",") + `)`
		for _, collectionID := range allowed {
			args = append(args, collectionID)
		}
	}
	query += ` ORDER BY pp.relative_path, f.face_index, f.id`
	rows, err := s.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		http.Error(w, "person faces lookup failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type personCandidate struct {
		PersonID, Name, Evidence string
		Similarity, Margin       float64
		Support                  int
	}
	type personCluster struct {
		ID, PersonID, PersonName                                     string
		Purity, Consistency, Connectivity                            float64
		PurityAvailable, ConsistencyAvailable, ConnectivityAvailable bool
		Quality, Detection                                           float64
	}
	type personFace struct {
		FaceID, PathID, RelativePath string
		Width, Height                int
		Left, Top, Right, Bottom     float64
		Quality, Detection           float64
		Confidence                   float64
		ConfidenceAvailable          bool
		State, PersonID              string
		Reference                    bool
		Recommended                  bool
		Representative               bool
		Cluster                      *personCluster
		Candidates                   []personCandidate
	}
	var faces []personFace
	referenceCount := 0
	for rows.Next() {
		var face personFace
		var reference, representativeFace int
		var confidence sql.NullFloat64
		if err := rows.Scan(&face.FaceID, &face.PathID, &face.RelativePath, &face.Width, &face.Height, &face.Left, &face.Top, &face.Right, &face.Bottom, &face.Quality, &face.Detection, &confidence, &face.State, &face.PersonID, &reference); err != nil {
			http.Error(w, "person faces lookup failed", http.StatusInternalServerError)
			return
		}
		if confidence.Valid {
			face.Confidence, face.ConfidenceAvailable = confidence.Float64, true
		}
		face.Reference = reference == 1
		_ = s.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM person_representatives WHERE person_id = ? AND face_id = ?)`, personID, face.FaceID).Scan(&representativeFace)
		face.Representative = representativeFace == 1
		candidateRows, candidateErr := s.db.QueryContext(r.Context(), `
			SELECT rc.person_id, COALESCE(NULLIF(p.name, ''), substr(rc.person_id, -8), rc.person_id),
			       rc.similarity, rc.margin, rc.support_count, rc.evidence
			FROM recognition_candidates rc LEFT JOIN people p ON p.id = rc.person_id
			WHERE rc.face_id = ? AND rc.created_at = (SELECT MAX(created_at) FROM recognition_candidates WHERE face_id = ?)
			ORDER BY rc.rank LIMIT 5`, face.FaceID, face.FaceID)
		if candidateErr != nil {
			http.Error(w, "person candidate lookup failed", http.StatusInternalServerError)
			return
		}
		for candidateRows.Next() {
			var candidate personCandidate
			if err := candidateRows.Scan(&candidate.PersonID, &candidate.Name, &candidate.Similarity, &candidate.Margin, &candidate.Support, &candidate.Evidence); err != nil {
				candidateRows.Close()
				http.Error(w, "person candidate lookup failed", http.StatusInternalServerError)
				return
			}
			face.Candidates = append(face.Candidates, candidate)
		}
		if err := candidateRows.Err(); err != nil {
			candidateRows.Close()
			http.Error(w, "person candidate lookup failed", http.StatusInternalServerError)
			return
		}
		candidateRows.Close()
		foundCurrent := false
		for _, candidate := range face.Candidates {
			if candidate.PersonID == face.PersonID {
				foundCurrent = true
				break
			}
		}
		if !foundCurrent && face.PersonID != "" {
			var candidate personCandidate
			if err := s.db.QueryRowContext(r.Context(), `
				SELECT rc.person_id, COALESCE(NULLIF(p.name, ''), substr(rc.person_id, -8), rc.person_id),
				       rc.similarity, rc.margin, rc.support_count, rc.evidence
				FROM recognition_candidates rc LEFT JOIN people p ON p.id = rc.person_id
				WHERE rc.face_id = ? AND rc.person_id = ?
				ORDER BY rc.created_at DESC, rc.rank LIMIT 1`, face.FaceID, face.PersonID).
				Scan(&candidate.PersonID, &candidate.Name, &candidate.Similarity, &candidate.Margin, &candidate.Support, &candidate.Evidence); err == nil {
				face.Candidates = append([]personCandidate{candidate}, face.Candidates[:min(4, len(face.Candidates))]...)
			}
		}
		if face.Confidence == 0 {
			for _, candidate := range face.Candidates {
				if candidate.PersonID == face.PersonID {
					face.Confidence, face.ConfidenceAvailable = candidate.Similarity, true
					break
				}
			}
		}
		var recommendation int
		if err := s.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM reference_recommendations WHERE face_id = ? AND person_id = ? AND is_current = 1)`, face.FaceID, personID).Scan(&recommendation); err == nil {
			face.Recommended = recommendation == 1
		}
		var cluster personCluster
		var purity, consistency, connectivity sql.NullFloat64
		if err := s.db.QueryRowContext(r.Context(), `
			SELECT c.id, COALESCE(c.proposed_person_id, ''), COALESCE(NULLIF(p.name, ''), CASE WHEN c.proposed_person_id IS NULL THEN 'Unknown' ELSE substr(c.proposed_person_id, -8) END),
			       c.purity, c.consistency, c.connectivity,
			       COALESCE(AVG(f2.quality), 0), COALESCE(AVG(f2.detection_confidence), 0)
			FROM cluster_memberships cm
			JOIN clusters c ON c.id = cm.cluster_id
			JOIN clustering_runs cr ON cr.id = c.clustering_run_id AND cr.status = 'active'
			LEFT JOIN people p ON p.id = c.proposed_person_id
			JOIN cluster_memberships cm2 ON cm2.cluster_id = c.id
			JOIN faces f2 ON f2.id = cm2.face_id
			WHERE cm.face_id = ?
			GROUP BY c.id, p.name, c.proposed_person_id, c.purity, c.consistency, c.connectivity
			ORDER BY c.id LIMIT 1`, face.FaceID).Scan(&cluster.ID, &cluster.PersonID, &cluster.PersonName, &purity, &consistency, &connectivity, &cluster.Quality, &cluster.Detection); err == nil {
			if purity.Valid {
				cluster.Purity, cluster.PurityAvailable = purity.Float64, true
			}
			if consistency.Valid {
				cluster.Consistency, cluster.ConsistencyAvailable = consistency.Float64, true
			}
			if connectivity.Valid {
				cluster.Connectivity, cluster.ConnectivityAvailable = connectivity.Float64, true
			}
			face.Cluster = &cluster
		}
		if face.Reference {
			referenceCount++
		}
		faces = append(faces, face)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "person faces lookup failed", http.StatusInternalServerError)
		return
	}
	faceFilter := r.URL.Query().Get("state")
	if faceFilter != "" {
		filtered := faces[:0]
		for _, face := range faces {
			if face.State == faceFilter {
				filtered = append(filtered, face)
			}
		}
		faces = filtered
	}
	faceSort := r.URL.Query().Get("sort")
	sort.SliceStable(faces, func(i, j int) bool {
		less := false
		switch faceSort {
		case "confidence":
			less = faces[i].Confidence < faces[j].Confidence
		case "quality":
			less = faces[i].Quality < faces[j].Quality
		case "detection":
			less = faces[i].Detection < faces[j].Detection
		default:
			less = faces[i].RelativePath < faces[j].RelativePath
		}
		if r.URL.Query().Get("direction") == "desc" {
			return !less && faces[i].FaceID != faces[j].FaceID
		}
		return less
	})
	maxConfidence, minConfidence := 0.0, 1.0
	hasConfidence := false
	minConsistency, maxConsistency := 0.0, 0.0
	hasConsistency := false
	clusterSummary := make(map[string][]string)
	clusterPersonIDs := make(map[string]string)
	clusterSummaryOrder := make([]string, 0)
	for _, face := range faces {
		if face.ConfidenceAvailable {
			if !hasConfidence || face.Confidence > maxConfidence {
				maxConfidence = face.Confidence
			}
			if !hasConfidence || face.Confidence < minConfidence {
				minConfidence = face.Confidence
			}
			hasConfidence = true
		}
		if face.Cluster != nil {
			if face.Cluster.PersonID != "" {
				clusterPersonIDs[face.Cluster.PersonName] = face.Cluster.PersonID
			}
			if face.Cluster.ConsistencyAvailable {
				if !hasConsistency || face.Cluster.Consistency < minConsistency {
					minConsistency = face.Cluster.Consistency
				}
				if !hasConsistency || face.Cluster.Consistency > maxConsistency {
					maxConsistency = face.Cluster.Consistency
				}
				hasConsistency = true
			}
			if _, exists := clusterSummary[face.Cluster.PersonName]; !exists {
				clusterSummaryOrder = append(clusterSummaryOrder, face.Cluster.PersonName)
			}
			seen := false
			for _, clusterID := range clusterSummary[face.Cluster.PersonName] {
				if clusterID == face.Cluster.ID {
					seen = true
					break
				}
			}
			if !seen {
				clusterSummary[face.Cluster.PersonName] = append(clusterSummary[face.Cluster.PersonName], face.Cluster.ID)
			}
		}
	}
	if !hasConfidence {
		minConfidence = 0
	}
	maxConfidenceText := formatScoreUnavailable(maxConfidence, hasConfidence)
	minConfidenceText := formatScoreUnavailable(minConfidence, hasConfidence)
	minConsistencyText := formatScoreUnavailable(minConsistency, hasConsistency)
	maxConsistencyText := formatScoreUnavailable(maxConsistency, hasConsistency)

	body := strings.Builder{}
	pageStyle := template.CSS(`.person-header{
  position:sticky;
  top:0;z-index:10;
  background:#fff;
  padding:10px 0}
.grid{
  display:flex;
  flex-wrap:wrap;
  gap:18px}
.card{
  width:min(420px,45vw);
  border:1px solid #ccc;
  padding:10px}
.card.provisional{border:2px solid #d6a800}
.card.needs-attention{border:2px solid #c62828}
.viewer{
  position:relative;
  display:inline-block;
  max-width:100%}
.viewer img{
  max-width:100%;
  max-height:55vh}
.box{
  position:absolute;
  border:2px solid #f00;
  background:rgba(204,0,0,.15);
  pointer-events:none}
.box.reference{
  border-color:#0a2;
  background:rgba(0,170,34,.15)}
.path{
  margin-top:8px;
  overflow-wrap:anywhere}
.actions{
  display:flex;
  justify-content:
  space-between;
  margin-top:8px}
.person-status{
  margin-top:8px;
  padding:6px;
  background:#f6f6f6;
  border-radius:3px}
.person-status strong{font-weight:600}
.person-scores{margin-top:8px;font-size:.9rem}
.person-scores table{width:100%;border-collapse:collapse}
.person-scores th,.person-scores td{padding:3px 4px;text-align:left;border-bottom:1px solid #eee}
.person-scores .current{font-weight:600;background:#fff8d6}
.person-assignment{display:flex;flex-wrap:wrap;gap:6px;margin-top:8px}
.person-assignment select{min-width:0;flex:1}
.person-empty{margin-top:1rem}
.person-summary{font-size:.8em;font-weight:normal}
.person-metadata{font-size:.9em;line-height:1.35}
.person-candidates{font-size:.9em;line-height:1.35}
.person-identity{display:flex;gap:12px;align-items:flex-start}
.person-identity img{flex:0 0 auto}
.person-name-form{display:flex;flex-direction:column;align-items:flex-start}
.person-page-controls{margin:8px 0}
.person-page-actions{display:flex;justify-content:space-between;align-items:center;margin-top:18px;padding:10px 0;border-top:1px solid #ccc}
.person-page-actions select{max-width:14rem}
.person-actions{display:flex;justify-content:space-between;align-items:center;margin-top:8px}
.person-dispositions{display:flex;gap:20px}
.person-assignment{display:flex;justify-content:flex-end;gap:6px;margin-top:8px}
.person-assignment select{min-width:10rem}`)
	peopleQuery := `SELECT id, COALESCE(NULLIF(name, ''), substr(id, -8), id) FROM people`
	peopleArgs := []any{}
	if allowed != nil {
		placeholders := make([]string, len(allowed))
		for i, collectionID := range allowed {
			placeholders[i] = "?"
			peopleArgs = append(peopleArgs, collectionID)
		}
		peopleQuery += ` WHERE EXISTS (SELECT 1 FROM current_face_assignments ca2 JOIN faces f2 ON f2.id = ca2.face_id JOIN analyses a2 ON a2.id = f2.analysis_id JOIN photo_assets pa2 ON pa2.id = a2.asset_id JOIN photo_paths pp2 ON pp2.asset_id = pa2.id WHERE ca2.person_id = people.id AND pp2.collection_id IN (` + strings.Join(placeholders, ",") + `))`
	}
	peopleQuery += ` ORDER BY LOWER(COALESCE(NULLIF(name, ''), substr(id, -8), id)), id`
	peopleRows, peopleErr := s.db.QueryContext(r.Context(), peopleQuery, peopleArgs...)
	if peopleErr != nil {
		http.Error(w, "people lookup failed", http.StatusInternalServerError)
		return
	}
	type personOption struct{ ID, Name string }
	var people []personOption
	for peopleRows.Next() {
		var option personOption
		if err := peopleRows.Scan(&option.ID, &option.Name); err != nil {
			peopleRows.Close()
			http.Error(w, "people lookup failed", http.StatusInternalServerError)
			return
		}
		people = append(people, option)
	}
	peopleRows.Close()

	canModify := account.Role != auth.Viewer
	recommendButton := ""
	if canModify {
		recommendButton = `<button type="button" data-action="recommend-references">Recommend References</button>`
	}
	body.WriteString(`<div id="person-page" data-person-id="` + template.HTMLEscapeString(personID) + `" data-csrf="` + template.HTMLEscapeString(csrfCookie(r)) + `" class="person-header"><div class="person-identity"><img loading="lazy" width="96" height="96" alt="Representative image" src="/api/people/` + template.HTMLEscapeString(personID) + `/representative"><div><h1>` + template.HTMLEscapeString(personName) + ` <span class="person-summary">(` + fmt.Sprint(len(faces)) + ` faces, ` + fmt.Sprint(referenceCount) + ` references, Similarity Max: ` + maxConfidenceText + `, Min: ` + minConfidenceText + `, Cluster Min: ` + minConsistencyText + `, Max: ` + maxConsistencyText + `)</span></h1><form id="rename-person" class="person-name-form"><input name="name" value="` + template.HTMLEscapeString(personName) + `" required><button>Rename</button>` + recommendButton + `</form></div></div>`)
	body.WriteString(`<form method="get" class="person-page-controls"><label>Filter <select name="state"><option value="">All faces</option>`)
	for _, state := range []string{"provisional", "assigned-user", "assigned-system", "unknown", "ambiguous", "unassigned"} {
		body.WriteString(`<option value="` + state + `"` + selectedAttr(state, faceFilter) + `>` + state + `</option>`)
	}
	body.WriteString(`</select></label> <label>Sort <select name="sort">`)
	for _, option := range []string{"path", "confidence", "quality", "detection"} {
		body.WriteString(`<option value="` + option + `"` + selectedAttr(option, faceSort) + `>` + option + `</option>`)
	}
	body.WriteString(`</select></label> <select name="direction"><option value="asc"` + selectedAttr("asc", r.URL.Query().Get("direction")) + `>Ascending</option><option value="desc"` + selectedAttr("desc", r.URL.Query().Get("direction")) + `>Descending</option></select> <button>Apply</button></form>`)
	if len(clusterSummaryOrder) > 0 {
		body.WriteString(`<div>Clusters: `)
		for index, clusterPerson := range clusterSummaryOrder {
			if index > 0 {
				body.WriteString(`, `)
			}
			if clusterPersonIDs[clusterPerson] != "" && clusterPersonIDs[clusterPerson] != personID {
				body.WriteString(`<a href="/review/people/` + template.HTMLEscapeString(clusterPersonIDs[clusterPerson]) + `">` + template.HTMLEscapeString(clusterPerson) + `</a>`)
			} else {
				body.WriteString(template.HTMLEscapeString(clusterPerson))
			}
			body.WriteString(` (`)
			for clusterIndex, clusterID := range clusterSummary[clusterPerson] {
				if clusterIndex > 0 {
					body.WriteString(`, `)
				}
				body.WriteString(`<a href="/review/clusters/` + template.HTMLEscapeString(clusterID) + `">` + template.HTMLEscapeString(idSuffix(clusterID)) + `</a>`)
			}
			body.WriteString(`)`)
		}
		body.WriteString(`</div>`)
	}
	body.WriteString(`</div>`)
	body.WriteString(`<div id="photos" class="grid">`)
	for _, face := range faces {
		boxClass := "box"
		if face.Reference {
			boxClass += " reference"
		}
		cardClass := "card"
		if face.State == string(review.Provisional) {
			cardClass += " provisional"
		} else if face.State != string(review.AssignedUser) && face.State != string(review.AssignedSystem) {
			cardClass += " needs-attention"
		}
		body.WriteString(`<article class="` + cardClass + `" id="face-` + template.HTMLEscapeString(idSuffix(face.FaceID)) + `" data-face-id="` + template.HTMLEscapeString(face.FaceID) + `" data-face-state="` + template.HTMLEscapeString(face.State) + `"><div class="viewer"><img loading="lazy" src="/api/photos/` + template.HTMLEscapeString(face.PathID) + `" alt="` + template.HTMLEscapeString(idSuffix(face.FaceID)) + `"><span class="` + boxClass + `" style="left:` + percent(face.Left, face.Width) + `%;top:` + percent(face.Top, face.Height) + `%;width:` + percent(face.Right-face.Left, face.Width) + `%;height:` + percent(face.Bottom-face.Top, face.Height) + `%"></span></div><div class="path"><a href="/review/photos?path_id=` + template.HTMLEscapeString(face.PathID) + `">` + template.HTMLEscapeString(face.RelativePath) + `</a></div>`)
		confidence := formatScoreUnavailable(face.Confidence, face.ConfidenceAvailable)
		body.WriteString(`<div class="person-metadata"><strong>` + template.HTMLEscapeString(assignmentLabel(face.State)) + `:</strong> C: ` + confidence + ` Q: ` + formatScore(face.Quality) + `% D: ` + formatScore(face.Detection) + `%`)
		if face.Recommended {
			body.WriteString(` <strong>Ref Rec</strong>`)
		}
		body.WriteString(`</div>`)
		otherCandidates := make([]personCandidate, 0, len(face.Candidates))
		for _, candidate := range face.Candidates {
			if candidate.PersonID != face.PersonID && candidate.Similarity > 0.15 {
				otherCandidates = append(otherCandidates, candidate)
			}
		}
		if len(otherCandidates) > 1 {
			body.WriteString(`<div class="person-candidates">Other candidates: `)
			for index, candidate := range otherCandidates {
				if index > 0 {
					body.WriteString(`, `)
				}
				if candidate.PersonID != personID {
					body.WriteString(`<a href="/review/people/` + template.HTMLEscapeString(candidate.PersonID) + `">` + template.HTMLEscapeString(candidate.Name) + `</a>`)
				} else {
					body.WriteString(template.HTMLEscapeString(candidate.Name))
				}
				body.WriteString(` (` + formatScore(candidate.Similarity) + `%)`)
			}
			body.WriteString(`</div>`)
		}
		if face.Cluster != nil {
			clusterPerson := template.HTMLEscapeString(face.Cluster.PersonName)
			if face.Cluster.PersonID != "" && face.Cluster.PersonID != personID {
				clusterPerson = `<a href="/review/people/` + template.HTMLEscapeString(face.Cluster.PersonID) + `">` + clusterPerson + `</a>`
			}
			body.WriteString(`<div class="person-metadata"><strong>Cluster:</strong> <a href="/review/clusters/` + template.HTMLEscapeString(face.Cluster.ID) + `">` + template.HTMLEscapeString(idSuffix(face.Cluster.ID)) + `</a>: ` + clusterPerson + ` (<strong>Cs:</strong> ` + formatScoreUnavailable(face.Cluster.Consistency, face.Cluster.ConsistencyAvailable) + ` <strong>Cn:</strong> ` + formatScoreUnavailable(face.Cluster.Connectivity, face.Cluster.ConnectivityAvailable) + ` <strong>Q:</strong> ` + formatScore(face.Cluster.Quality) + `%, <strong>D:</strong> ` + formatScore(face.Cluster.Detection) + `%)</div>`)
		}
		if canModify {
			body.WriteString(`<div class="actions"><div class="person-dispositions"><button type="button" data-action="unknown" data-face-id="` + template.HTMLEscapeString(face.FaceID) + `">Unknown</button><button type="button" data-action="ambiguous" data-face-id="` + template.HTMLEscapeString(face.FaceID) + `">Ambiguous</button>`)
			if !face.Representative {
				body.WriteString(`<button type="button" data-action="representative" data-face-id="` + template.HTMLEscapeString(face.FaceID) + `">Key Photo</button>`)
			}
			body.WriteString(`</div>`)
			if face.Reference || face.State == string(review.AssignedSystem) || face.State == string(review.AssignedUser) {
				label := "Reference"
				if face.Reference {
					label = "Evaluation"
				}
				body.WriteString(`<button type="button" data-action="role" data-face-id="` + template.HTMLEscapeString(face.FaceID) + `" data-reference="` + fmt.Sprint(face.Reference) + `">` + label + `</button>`)
			}
			body.WriteString(`</div><div class="person-assignment"><select data-assignment-person="` + template.HTMLEscapeString(face.FaceID) + `"><option value="">Unknown</option><option value="__ambiguous__">Ambiguous</option>`)
			for _, option := range people {
				selected := ""
				if option.ID == face.PersonID {
					selected = ` selected`
				}
				body.WriteString(`<option value="` + template.HTMLEscapeString(option.ID) + `"` + selected + `>` + template.HTMLEscapeString(option.Name) + `</option>`)
			}
			body.WriteString(`</select><button type="button" data-action="assign" data-face-id="` + template.HTMLEscapeString(face.FaceID) + `">Save</button></div>`)
		}
		body.WriteString(`</article>`)
	}
	if len(faces) == 0 {
		body.WriteString(`<p class="person-empty">No faces are assigned to this person.</p>`)
	}
	if canModify && len(faces) > 0 {
		visibleStates := make(map[string]bool)
		for _, face := range faces {
			visibleStates[face.State] = true
		}
		body.WriteString(`<div class="person-page-actions"><div><label>Assignment <select id="page-assignment-state"><option value="__all_non_assigned__">All non-assigned</option>`)
		for _, state := range []string{"provisional", "assigned-system", "assigned-user", "unassigned", "unknown", "ambiguous"} {
			if visibleStates[state] {
				body.WriteString(`<option value="` + state + `">` + state + `</option>`)
			}
		}
		body.WriteString(`</select></label> <button type="button" data-action="page-unknown">Unknown</button><button type="button" data-action="page-ambiguous">Ambiguous</button></div><div><select id="page-assignment-person">`)
		for _, option := range people {
			body.WriteString(`<option value="` + template.HTMLEscapeString(option.ID) + `"` + selectedAttr(option.ID, personID) + `>` + template.HTMLEscapeString(option.Name) + `</option>`)
		}
		body.WriteString(`</select><button type="button" data-action="page-assign">Save</button></div></div>`)
	}
	body.WriteString(`</div>`)
	if canModify {
		body.WriteString(`<script src="/static/person.js" defer></script>`)
	}
	s.render(w, map[string]any{"Title": personName, "Account": account, "PageStyle": pageStyle, "Body": template.HTML(body.String())})
}

func (s *Server) createPerson(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	if account.Role == auth.Viewer || !validCSRFApi(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var input struct {
		Name        string `json:"name"`
		Provisional bool   `json:"provisional"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || strings.TrimSpace(input.Name) == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	personID, err := id.NewUUIDv7()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.ExecContext(r.Context(), `INSERT INTO people (id, name, is_provisional, dont_track, created_at, updated_at) VALUES (?, ?, ?, 0, ?, ?)`, personID, strings.TrimSpace(input.Name), boolInt(input.Provisional), now, now); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = audit.NewStore(s.db).Record(r.Context(), audit.Event{AccountID: account.ID, Action: "person-create", EntityType: "person", EntityID: personID, New: map[string]any{"name": input.Name}})
	writeJSON(w, http.StatusCreated, map[string]string{"id": personID})
}

func (s *Server) renamePerson(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	if account.Role == auth.Viewer || !validCSRFApi(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var input struct {
		Name string `json:"name"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || strings.TrimSpace(input.Name) == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	result, err := s.db.ExecContext(r.Context(), `UPDATE people SET name = ?, is_provisional = 0, updated_at = ? WHERE id = ?`, strings.TrimSpace(input.Name), time.Now().UTC().Format(time.RFC3339Nano), r.PathValue("personID"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if count, _ := result.RowsAffected(); count != 1 {
		http.NotFound(w, r)
		return
	}
	if err := audit.NewStore(s.db).Record(r.Context(), audit.Event{AccountID: account.ID, Action: "person-rename", EntityType: "person", EntityID: r.PathValue("personID"), New: map[string]any{"name": strings.TrimSpace(input.Name)}}); err != nil {
		http.Error(w, "record rename audit event: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": r.PathValue("personID"), "name": strings.TrimSpace(input.Name)})
}

func (s *Server) referenceReprocess(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	if !validCSRFApi(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	personID := r.PathValue("personID")
	allowed, err := s.personPermission(r, account, personID, true)
	if err != nil || !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	job, err := jobs.NewStore(s.db).Enqueue(r.Context(), "reference-reprocess", 1, map[string]string{"person_id": personID})
	if err != nil {
		http.Error(w, "queue reference recommendations: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := audit.NewStore(s.db).Record(r.Context(), audit.Event{AccountID: account.ID, Action: "reference-reprocess-request", EntityType: "person", EntityID: personID, New: map[string]any{"job_id": job.ID}}); err != nil {
		http.Error(w, "record reference recommendation audit event: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"person_id": personID, "job_id": job.ID, "status": job.Status})
}

func (s *Server) personReferences(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	personID := r.PathValue("personID")
	var exists int
	if err := s.db.QueryRowContext(r.Context(), `SELECT 1 FROM people WHERE id = ?`, personID).Scan(&exists); err != nil {
		http.NotFound(w, r)
		return
	}
	allowed, err := s.auth.AccessibleCollections(r.Context(), account.ID)
	if err != nil {
		http.Error(w, "permission lookup failed", http.StatusInternalServerError)
		return
	}
	if allowed != nil && len(allowed) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"person_id": personID, "recommendations": []any{}})
		return
	}
	referenceQuery := `SELECT rr.id, rr.face_id, rr.is_current, COALESCE(rr.recommendation_score, 0), COALESCE(rr.quality, 0), COALESCE(rr.detection_confidence, 0), COALESCE(rr.similarity, 0), COALESCE(rr.margin, 0), COALESCE(rr.support_count, 0), COALESCE(rr.purity, 0), COALESCE(rr.connectivity, 0), COALESCE(cfr.reference, 0) FROM reference_recommendations rr JOIN faces f ON f.id = rr.face_id JOIN analyses a ON a.id = f.analysis_id JOIN photo_assets pa ON pa.id = a.asset_id JOIN photo_paths pp ON pp.asset_id = pa.id LEFT JOIN current_face_references cfr ON cfr.face_id = rr.face_id AND cfr.person_id = rr.person_id WHERE rr.person_id = ?`
	referenceArgs := []any{personID}
	if allowed != nil {
		placeholders := make([]string, len(allowed))
		for i, collectionID := range allowed {
			placeholders[i] = "?"
			referenceArgs = append(referenceArgs, collectionID)
		}
		referenceQuery += ` AND pp.collection_id IN (` + strings.Join(placeholders, ",") + `)`
	}
	referenceQuery += ` ORDER BY rr.is_current DESC, rr.recommendation_score DESC LIMIT 20`
	rows, err := s.db.QueryContext(r.Context(), referenceQuery, referenceArgs...)
	if err != nil {
		http.Error(w, "reference lookup failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type item struct {
		RecommendationID, FaceID                                            string
		Current, Reference                                                  bool
		Score, Quality, Detection, Similarity, Margin, Purity, Connectivity float64
		Support                                                             int
	}
	var result []item
	for rows.Next() {
		var value item
		var current, reference int
		if err := rows.Scan(&value.RecommendationID, &value.FaceID, &current, &value.Score, &value.Quality, &value.Detection, &value.Similarity, &value.Margin, &value.Support, &value.Purity, &value.Connectivity, &reference); err != nil {
			http.Error(w, "reference lookup failed", http.StatusInternalServerError)
			return
		}
		value.Current, value.Reference = current == 1, reference == 1
		result = append(result, value)
	}
	writeJSON(w, http.StatusOK, map[string]any{"person_id": personID, "recommendations": result, "account_id": account.ID})
}

func (s *Server) reviewPersonReferences(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	personID := r.PathValue("personID")
	var name string
	if err := s.db.QueryRowContext(r.Context(), `SELECT COALESCE(NULLIF(name, ''), substr(id, -8)) FROM people WHERE id = ?`, personID).Scan(&name); err != nil {
		http.NotFound(w, r)
		return
	}
	allowed, err := s.auth.AccessibleCollections(r.Context(), account.ID)
	if err != nil {
		http.Error(w, "permission lookup failed", http.StatusInternalServerError)
		return
	}
	if allowed != nil && len(allowed) == 0 {
		s.render(w, map[string]any{"Title": "Person references", "Account": account, "Body": template.HTML(`<h1>References</h1><p>No collections are assigned to this account.</p>`)})
		return
	}
	referenceQuery := `SELECT rr.face_id, rr.is_current, COALESCE(rr.recommendation_score, 0), pp.id, pp.relative_path, COALESCE(pa.width, 0), COALESCE(pa.height, 0), f.box_left, f.box_top, f.box_right, f.box_bottom, COALESCE(f.quality, 0), COALESCE(f.detection_confidence, 0), COALESCE(cfr.reference, 0) FROM reference_recommendations rr JOIN faces f ON f.id = rr.face_id JOIN analyses a ON a.id = f.analysis_id JOIN photo_assets pa ON pa.id = a.asset_id JOIN photo_paths pp ON pp.asset_id = pa.id LEFT JOIN current_face_references cfr ON cfr.face_id = rr.face_id AND cfr.person_id = rr.person_id WHERE rr.person_id = ?`
	referenceArgs := []any{personID}
	if allowed != nil {
		placeholders := make([]string, len(allowed))
		for i, collectionID := range allowed {
			placeholders[i] = "?"
			referenceArgs = append(referenceArgs, collectionID)
		}
		referenceQuery += ` AND pp.collection_id IN (` + strings.Join(placeholders, ",") + `)`
	}
	referenceQuery += ` ORDER BY rr.is_current DESC, rr.recommendation_score DESC LIMIT 20`
	rows, err := s.db.QueryContext(r.Context(), referenceQuery, referenceArgs...)
	if err != nil {
		http.Error(w, "reference lookup failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	body := strings.Builder{}
	body.WriteString(`<div id="photo-page" data-csrf="` + template.HTMLEscapeString(csrfCookie(r)) + `"><h1>References for ` + template.HTMLEscapeString(name) + `</h1><p>Current approved references and the latest recommendations are shown below.</p><div class="grid">`)
	for rows.Next() {
		var faceID, pathID, relative string
		var current, reference int
		var score float64
		var quality, detection float64
		var width, height int
		var left, top, right, bottom float64
		if err := rows.Scan(&faceID, &current, &score, &pathID, &relative, &width, &height, &left, &top, &right, &bottom, &quality, &detection, &reference); err != nil {
			http.Error(w, "reference lookup failed", http.StatusInternalServerError)
			return
		}
		class := "box"
		if reference == 1 {
			class += " reference"
		}
		label := map[bool]string{true: "Approved reference", false: "Recommendation"}[reference == 1]
		body.WriteString(fmt.Sprintf(`<article class="card"><div class="viewer"><img loading="lazy" src="/api/photos/%s"><span class="%s" style="left:%s%%;top:%s%%;width:%s%%;height:%s%%"></span></div><div class="path"><a href="/review/photos?path_id=%s">%s</a></div><div>%s · score %s%%</div><div>Quality %s%% · Detection %s%%</div><div class="actions"><button type="button" data-action="role" data-face-id="%s" data-person-id="%s" data-reference="%t">%s</button></div></article>`, template.HTMLEscapeString(pathID), class, percent(left, width), percent(top, height), percent(right-left, width), percent(bottom-top, height), template.HTMLEscapeString(pathID), template.HTMLEscapeString(relative), label, formatScore(score), formatScore(quality), formatScore(detection), template.HTMLEscapeString(faceID), template.HTMLEscapeString(personID), reference == 1, map[bool]string{true: "Make evaluation", false: "Approve reference"}[reference == 1]))
	}
	body.WriteString(`</div></div><script src="/static/photo.js" defer></script>`)
	s.render(w, map[string]any{"Title": "Person references", "Account": account, "Body": template.HTML(body.String())})
}

func (s *Server) personScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	_, _ = w.Write([]byte(`(function () {
  const page = document.getElementById('person-page');
  if (!page) return;
  const token = page.dataset.csrf || '';
  const renameForm = document.getElementById('rename-person');
  if (renameForm) renameForm.onsubmit = async (event) => {
    event.preventDefault();
    const response = await fetch('/api/people/' + encodeURIComponent(page.dataset.personId), {
      method: 'POST',
      headers: {'Content-Type': 'application/json', 'X-CSRF-Token': token},
      body: JSON.stringify({name: renameForm.name.value})
    });
    if (!response.ok) { alert(await response.text()); return; }
    location.reload();
  };
  const recommendButton = document.querySelector('[data-action="recommend-references"]');
  if (recommendButton) recommendButton.onclick = async () => {
    const response = await fetch('/api/people/' + encodeURIComponent(page.dataset.personId) + '/reference-reprocess', {
      method: 'POST', headers: {'X-CSRF-Token': token}
    });
    if (!response.ok) { alert(await response.text()); return; }
    const result = await response.json();
    alert('Reference recommendation job queued: ' + result.job_id);
  };
  async function send(faceID, url, payload) {
    const response = await fetch(url, {
      method: 'POST',
      headers: {'Content-Type': 'application/json', 'X-CSRF-Token': token},
      body: JSON.stringify(payload)
    });
    if (!response.ok) { alert(await response.text()); return; }
    location.reload();
  }
  document.querySelectorAll('[data-action="unknown"], [data-action="ambiguous"]').forEach((button) => {
    button.onclick = () => send(button.dataset.faceId,
      '/api/faces/' + encodeURIComponent(button.dataset.faceId) + '/assignment',
      {person_id: '', state: button.dataset.action});
  });
  document.querySelectorAll('[data-action="assign"]').forEach((button) => {
    button.onclick = () => {
      const faceID = button.dataset.faceId;
      const person = document.querySelector('[data-assignment-person="' + CSS.escape(faceID) + '"]').value;
      const payload = {person_id: person, state: 'assigned-user'};
      if (person === '__ambiguous__') { payload.person_id = ''; payload.state = 'ambiguous'; }
      if (!person) { payload.state = 'unknown'; }
      send(faceID, '/api/faces/' + encodeURIComponent(faceID) + '/assignment', payload);
    };
  });
  document.querySelectorAll('[data-action="role"]').forEach((button) => {
    button.onclick = () => send(button.dataset.faceId,
      '/api/faces/' + encodeURIComponent(button.dataset.faceId) + '/reference',
      {person_id: page.dataset.personId, reference: button.dataset.reference !== 'true'});
  });
  document.querySelectorAll('[data-action="representative"]').forEach((button) => {
    button.onclick = () => send(button.dataset.faceId,
      '/api/people/' + encodeURIComponent(page.dataset.personId) + '/representative',
      {face_id: button.dataset.faceId});
  });
  function bulkFaces() {
    const selected = document.getElementById('page-assignment-state').value;
    return Array.from(document.querySelectorAll('#photos [data-face-state]')).filter((card) => {
      const state = card.dataset.faceState;
      return selected === '__all_non_assigned__'
        ? state !== 'assigned-user' && state !== 'assigned-system'
        : state === selected;
    }).map((card) => card.dataset.faceId);
  }
  async function sendBulk(state, personID) {
    const faceIDs = bulkFaces();
    if (!faceIDs.length) { alert('No visible faces match the selected assignment.'); return; }
    for (const faceID of faceIDs) {
      const response = await fetch('/api/faces/' + encodeURIComponent(faceID) + '/assignment', {
        method: 'POST',
        headers: {'Content-Type': 'application/json', 'X-CSRF-Token': token},
        body: JSON.stringify({person_id: personID || '', state: state})
      });
      if (!response.ok) { alert(await response.text()); return; }
    }
    location.reload();
  }
  document.querySelectorAll('[data-action="page-unknown"], [data-action="page-ambiguous"]').forEach((button) => {
    button.onclick = () => sendBulk(button.dataset.action === 'page-unknown' ? 'unknown' : 'ambiguous', '');
  });
  document.querySelectorAll('[data-action="page-assign"]').forEach((button) => {
    button.onclick = () => {
      const person = document.getElementById('page-assignment-person').value;
      sendBulk('assigned-user', person);
    };
  });
})();
`))
}

func (s *Server) photoScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	_, _ = w.Write([]byte(`(function () {
  const page = document.getElementById('photo-page'); if (!page) return;
  async function send(faceID, url, payload) {
    const response = await fetch(url, {method:'POST', headers:{'Content-Type':'application/json','X-CSRF-Token':page.dataset.csrf}, body:JSON.stringify(payload)});
    if (!response.ok) { alert(await response.text()); return; }
    location.reload();
  }
  document.querySelectorAll('[data-action="unknown"],[data-action="ambiguous"]').forEach((button) => {
    button.onclick = () => send(button.dataset.faceId, '/api/faces/' + encodeURIComponent(button.dataset.faceId) + '/assignment', {person_id:'', state:button.dataset.action});
  });
  document.querySelectorAll('[data-action="assign"]').forEach((button) => {
    button.onclick = () => {
      const person = document.querySelector('[data-assignment-person="' + CSS.escape(button.dataset.faceId) + '"]').value;
      const state = person === '__ambiguous__' ? 'ambiguous' : (person ? 'assigned-user' : 'unknown');
      send(button.dataset.faceId, '/api/faces/' + encodeURIComponent(button.dataset.faceId) + '/assignment', {person_id:person === '__ambiguous__' ? '' : person, state:state});
    };
  });
  document.querySelectorAll('[data-action="role"]').forEach((button) => {
    button.onclick = () => send(button.dataset.faceId, '/api/faces/' + encodeURIComponent(button.dataset.faceId) + '/reference', {person_id:button.dataset.personId, reference:button.dataset.reference !== 'true'});
  });
  document.querySelectorAll('[data-action="disposition"]').forEach((button) => {
    button.onclick = () => send(button.dataset.faceId, '/api/faces/' + encodeURIComponent(button.dataset.faceId) + '/disposition', {disposition:button.dataset.disposition, active:true});
  });
})();
`))
}

func (s *Server) reviewClusters(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	allowed, err := s.auth.AccessibleCollections(r.Context(), account.ID)
	if err != nil {
		http.Error(w, "permission lookup failed", http.StatusInternalServerError)
		return
	}
	query := r.URL.Query()
	clusterQuery := `SELECT c.id, c.cluster_key, c.purity, c.connectivity, c.is_mixed, c.review_status, COUNT(DISTINCT cm.face_id), COUNT(DISTINCT rc.person_id), COALESCE(c.proposed_person_id, ''), COALESCE(NULLIF(p.name, ''), substr(c.proposed_person_id, -8), '') FROM clusters c JOIN clustering_runs cr ON cr.id = c.clustering_run_id LEFT JOIN cluster_memberships cm ON cm.cluster_id = c.id LEFT JOIN recognition_candidates rc ON rc.face_id = cm.face_id LEFT JOIN people p ON p.id = c.proposed_person_id WHERE cr.status = 'active'`
	var clusterArgs []any
	if allowed != nil {
		if len(allowed) == 0 {
			s.render(w, map[string]any{"Title": "Clusters", "Account": account, "Body": template.HTML(`<h1>Clusters</h1><p>No collections are assigned to this account.</p>`)})
			return
		}
		placeholders := make([]string, len(allowed))
		for i, id := range allowed {
			placeholders[i] = "?"
			clusterArgs = append(clusterArgs, id)
		}
		clusterQuery += ` AND EXISTS (SELECT 1 FROM cluster_memberships cm2 JOIN faces f2 ON f2.id = cm2.face_id JOIN analyses a2 ON a2.id = f2.analysis_id JOIN photo_assets pa2 ON pa2.id = a2.asset_id JOIN photo_paths pp2 ON pp2.asset_id = pa2.id WHERE cm2.cluster_id = c.id AND pp2.collection_id IN (` + strings.Join(placeholders, ",") + `)`
	}
	if query.Get("include_dont_track") != "true" {
		clusterQuery += ` AND NOT EXISTS (SELECT 1 FROM cluster_memberships cm3 JOIN current_face_dispositions fd3 ON fd3.face_id = cm3.face_id WHERE cm3.cluster_id = c.id AND fd3.disposition = 'dont-track')`
	}
	if mixed := query.Get("mixed"); mixed == "true" || mixed == "false" {
		clusterQuery += ` AND c.is_mixed = ?`
		if mixed == "true" {
			clusterArgs = append(clusterArgs, 1)
		} else {
			clusterArgs = append(clusterArgs, 0)
		}
	}
	if status := query.Get("status"); status != "" {
		clusterQuery += ` AND c.review_status = ?`
		clusterArgs = append(clusterArgs, status)
	}
	if personID := query.Get("person_id"); personID != "" {
		clusterQuery += ` AND c.proposed_person_id = ?`
		clusterArgs = append(clusterArgs, personID)
	}
	switch query.Get("rating") {
	case "high":
		clusterQuery += ` AND COALESCE(c.purity, 0) >= 0.8 AND COALESCE(c.connectivity, 0) >= 0.8`
	case "medium":
		clusterQuery += ` AND COALESCE(c.purity, 0) >= 0.5 AND COALESCE(c.purity, 0) < 0.8 AND COALESCE(c.connectivity, 0) >= 0.5`
	case "low":
		clusterQuery += ` AND (COALESCE(c.purity, 0) < 0.5 OR COALESCE(c.connectivity, 0) < 0.5)`
	}
	clusterQuery += ` GROUP BY c.id, c.cluster_key, c.purity, c.connectivity, c.is_mixed, c.review_status, p.name, c.proposed_person_id`
	if minFaces := query.Get("min_faces"); minFaces != "" {
		clusterQuery += ` HAVING COUNT(DISTINCT cm.face_id) >= ?`
		clusterArgs = append(clusterArgs, minFaces)
	}
	if maxFaces := query.Get("max_faces"); maxFaces != "" {
		if strings.Contains(clusterQuery, " HAVING ") {
			clusterQuery += ` AND COUNT(DISTINCT cm.face_id) <= ?`
		} else {
			clusterQuery += ` HAVING COUNT(DISTINCT cm.face_id) <= ?`
		}
		clusterArgs = append(clusterArgs, maxFaces)
	}
	sortColumn := map[string]string{"face_count": "COUNT(DISTINCT cm.face_id)", "candidate_count": "COUNT(DISTINCT rc.person_id)", "person": "LOWER(COALESCE(p.name, c.proposed_person_id))", "mixed": "c.is_mixed", "rating": "COALESCE(c.purity, 0)"}[query.Get("sort")]
	if sortColumn == "" {
		sortColumn = "c.is_mixed"
	}
	direction := "DESC"
	if strings.EqualFold(query.Get("direction"), "asc") {
		direction = "ASC"
	}
	clusterQuery += ` ORDER BY ` + sortColumn + ` ` + direction + `, c.cluster_key`
	rows, err := s.db.QueryContext(r.Context(), clusterQuery, clusterArgs...)
	if err != nil {
		http.Error(w, "cluster lookup failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var body strings.Builder
	body.WriteString(`<h1>Clusters</h1><form method="get"><label>Mixed <select name="mixed"><option value="">Any</option><option value="true">Mixed</option><option value="false">Pure</option></select></label> <label>Status <input name="status"></label> <label>Rating <select name="rating"><option value="">Any</option><option value="high">High</option><option value="medium">Medium</option><option value="low">Low</option></select></label> <label><input type="checkbox" name="include_dont_track" value="true"> Include don't-track</label> <label>Min faces <input name="min_faces" type="number" min="1"></label> <label>Max faces <input name="max_faces" type="number" min="1"></label> <label>Sort <select name="sort"><option value="mixed">Mixed</option><option value="face_count">Face count</option><option value="candidate_count">Candidate count</option><option value="person">Person</option><option value="rating">Rating</option></select></label> <button>Filter</button></form><table><tr><th>Cluster</th><th>Person</th><th>Faces</th><th>Candidates</th><th>Purity</th><th>Connectivity</th><th>Mixed</th><th>Status</th></tr>`)
	for rows.Next() {
		var id, key, status, personID, personName string
		var purity, connectivity sql.NullFloat64
		var mixed, faceCount, candidateCount int
		if err := rows.Scan(&id, &key, &purity, &connectivity, &mixed, &status, &faceCount, &candidateCount, &personID, &personName); err != nil {
			http.Error(w, "cluster lookup failed", http.StatusInternalServerError)
			return
		}
		personCell := template.HTMLEscapeString(personName)
		if personID != "" {
			personCell = `<a href="/review/people/` + template.HTMLEscapeString(personID) + `">` + personCell + `</a>`
		}
		body.WriteString(`<tr><td><a href="/review/clusters/` + template.HTMLEscapeString(id) + `">` + template.HTMLEscapeString(key) + `</a></td><td>` + personCell + `</td><td>` + fmt.Sprint(faceCount) + `</td><td>` + fmt.Sprint(candidateCount) + `</td><td>` + fmt.Sprint(purity.Float64) + `</td><td>` + fmt.Sprint(connectivity.Float64) + `</td><td>` + fmt.Sprint(mixed == 1) + `</td><td>` + template.HTMLEscapeString(status) + `</td></tr>`)
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
	var runStatus, supersededBy, reviewStatus, collectionID string
	var reviewVersion, mixed int
	err := s.db.QueryRowContext(r.Context(), `
		SELECT cr.status, COALESCE(cr.superseded_by, ''), c.review_status, c.review_version, c.is_mixed,
		       (SELECT pp.collection_id FROM cluster_memberships cm
				JOIN faces f ON f.id = cm.face_id JOIN analyses a ON a.id = f.analysis_id
				JOIN photo_assets pa ON pa.id = a.asset_id JOIN photo_paths pp ON pp.asset_id = pa.id
		WHERE cm.cluster_id = c.id LIMIT 1)
		FROM clusters c JOIN clustering_runs cr ON cr.id = c.clustering_run_id WHERE c.id = ?`, clusterID).
		Scan(&runStatus, &supersededBy, &reviewStatus, &reviewVersion, &mixed, &collectionID)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "cluster lookup failed", http.StatusInternalServerError)
		return
	}
	allowed, err := s.auth.CanAccessCollection(r.Context(), account.ID, collectionID, false, false)
	if err != nil || !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	peopleRows, err := s.db.QueryContext(r.Context(), `SELECT id, COALESCE(NULLIF(name, ''), substr(id, -8)) FROM people ORDER BY LOWER(COALESCE(NULLIF(name, ''), substr(id, -8))), id`)
	if err != nil {
		http.Error(w, "people lookup failed", http.StatusInternalServerError)
		return
	}
	type personOption struct{ ID, Name string }
	var people []personOption
	for peopleRows.Next() {
		var person personOption
		if err := peopleRows.Scan(&person.ID, &person.Name); err != nil {
			peopleRows.Close()
			http.Error(w, "people lookup failed", http.StatusInternalServerError)
			return
		}
		people = append(people, person)
	}
	peopleRows.Close()
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT cm.face_id, COALESCE(ca.state, 'unassigned'), COALESCE(ca.person_id, ''),
		       COALESCE(pp.id, ''), COALESCE(pp.relative_path, ''), COALESCE(pa.width, 0), COALESCE(pa.height, 0),
		       f.box_left, f.box_top, f.box_right, f.box_bottom,
		       COALESCE(f.quality, 0), COALESCE(f.detection_confidence, 0)
		FROM cluster_memberships cm
		JOIN faces f ON f.id = cm.face_id JOIN analyses a ON a.id = f.analysis_id
		JOIN photo_assets pa ON pa.id = a.asset_id
		LEFT JOIN current_face_assignments ca ON ca.face_id = cm.face_id
		LEFT JOIN photo_paths pp ON pp.asset_id = pa.id AND pp.collection_id = ?
		WHERE cm.cluster_id = ? ORDER BY cm.face_id`, collectionID, clusterID)
	if err != nil {
		http.Error(w, "cluster lookup failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var body strings.Builder
	body.WriteString(`<style>.cluster-grid{display:flex;flex-wrap:wrap;gap:18px}.cluster-face{width:min(420px,45vw);border:4px solid;padding:10px}.photo-frame{position:relative;display:inline-block;max-width:100%}.photo-frame img{max-width:100%;max-height:55vh;display:block}.face-box{position:absolute;border:2px solid #f00;box-sizing:border-box;pointer-events:none}.cluster-face select{max-width:100%}.candidate{font-size:.9rem;margin-top:6px}.cluster-controls{position:sticky;top:0;z-index:10;background:#fff;padding:10px 0;border-bottom:1px solid #ccc}</style>`)
	body.WriteString(`<div id="cluster-page" data-cluster-id="` + template.HTMLEscapeString(clusterID) + `" data-csrf="` + template.HTMLEscapeString(csrfCookie(r)) + `" data-version="` + fmt.Sprint(reviewVersion) + `">`)
	body.WriteString(`<h1>Cluster ` + template.HTMLEscapeString(idSuffix(clusterID)) + `</h1>`)
	body.WriteString(`<p>Review status: ` + template.HTMLEscapeString(reviewStatus) + `; version ` + fmt.Sprint(reviewVersion) + `; run: ` + template.HTMLEscapeString(runStatus) + `</p>`)
	if mixed != 0 {
		body.WriteString(`<p class="error">This cluster is mixed: review each face explicitly. A default person cannot bulk-confirm it.</p>`)
	}
	if runStatus == "superseded" {
		body.WriteString(`<p class="error">This is a historical clustering run. A newer run is active`)
		if supersededBy != "" {
			body.WriteString(` (` + template.HTMLEscapeString(idSuffix(supersededBy)) + `)`)
		}
		body.WriteString(`. Review changes are still recorded against this historical cluster.</p>`)
	}
	body.WriteString(`<div class="cluster-controls"><label>Default person <select id="default-person"`)
	if mixed != 0 {
		body.WriteString(` disabled`)
	}
	body.WriteString(`><option value="">No default</option>`)
	for _, person := range people {
		body.WriteString(`<option value="` + template.HTMLEscapeString(person.ID) + `">` + template.HTMLEscapeString(person.Name) + `</option>`)
	}
	body.WriteString(`</select></label> <label>Status <select id="cluster-status"><option>in-progress</option><option>partially-reviewed</option><option>reviewed</option><option>needs-review</option></select></label> <label>Note <input id="cluster-note" size="45"></label> <button id="save-cluster" type="button">Save review</button></div><p id="cluster-message"></p><div class="cluster-grid">`)
	for rows.Next() {
		var faceID, state, personID, pathID, relativePath string
		var width, height int
		var left, top, right, bottom, quality, detection float64
		if err := rows.Scan(&faceID, &state, &personID, &pathID, &relativePath, &width, &height, &left, &top, &right, &bottom, &quality, &detection); err != nil {
			http.Error(w, "cluster lookup failed", http.StatusInternalServerError)
			return
		}
		color := faceStateColor(state)
		body.WriteString(`<article class="cluster-face" data-face-id="` + template.HTMLEscapeString(faceID) + `" data-original-state="` + template.HTMLEscapeString(state) + `" data-original-person="` + template.HTMLEscapeString(personID) + `" style="border-color:` + color + `"><div class="photo-frame"><img src="/api/photos/` + template.HTMLEscapeString(pathID) + `" alt="face ` + template.HTMLEscapeString(idSuffix(faceID)) + `"><span class="face-box" style="left:` + percent(left, width) + `%;top:` + percent(top, height) + `%;width:` + percent(right-left, width) + `%;height:` + percent(bottom-top, height) + `%"></span></div><div class="path"><a href="/review/photos?path_id=` + template.HTMLEscapeString(pathID) + `">` + template.HTMLEscapeString(relativePath) + `</a></div><div>Face ` + template.HTMLEscapeString(idSuffix(faceID)) + ` · ` + template.HTMLEscapeString(state) + ` · Quality ` + formatScore(quality) + `% · Detection ` + formatScore(detection) + `%</div><label>Person <select class="face-person"><option value="">Unknown</option><option value="__ambiguous__">Ambiguous</option>`)
		for _, person := range people {
			selected := selectedAttr(person.ID, personID)
			body.WriteString(`<option value="` + template.HTMLEscapeString(person.ID) + `"` + selected + `>` + template.HTMLEscapeString(person.Name) + `</option>`)
		}
		body.WriteString(`</select></label> <label>State <select class="face-state">`)
		for _, option := range []string{"unassigned", "unknown", "ambiguous", "provisional", "assigned-system", "assigned-user"} {
			body.WriteString(`<option value="` + option + `"` + selectedAttr(option, state) + `>` + option + `</option>`)
		}
		body.WriteString(`</select></label> <label>Disposition <select class="face-disposition"><option value="">None</option><option value="dont-track">Don't track</option><option value="false-detection">False detection</option><option value="incorrect-assignment">Incorrect assignment</option></select></label><div class="candidate">`)
		candidateRows, candidateErr := s.db.QueryContext(r.Context(), `SELECT rc.person_id, COALESCE(NULLIF(p.name, ''), substr(rc.person_id, -8), rc.person_id), rc.similarity, rc.margin, rc.support_count FROM recognition_candidates rc LEFT JOIN people p ON p.id = rc.person_id WHERE rc.face_id = ? ORDER BY rc.created_at DESC, rc.rank LIMIT 5`, faceID)
		if candidateErr == nil {
			for candidateRows.Next() {
				var candidateID, name string
				var similarity, margin float64
				var support int
				if candidateRows.Scan(&candidateID, &name, &similarity, &margin, &support) == nil {
					body.WriteString(`<a href="/review/people/` + template.HTMLEscapeString(candidateID) + `">` + template.HTMLEscapeString(name) + `</a>` + template.HTMLEscapeString(fmt.Sprintf(" %.3f (margin %.3f, support %d)", similarity, margin, support)) + `<br>`)
				}
			}
			candidateRows.Close()
		}
		body.WriteString(`</div></article>`)
	}
	body.WriteString(`</div><p>Green means user-assigned, yellow means the selected/default person is provisional or system-assigned, and red means the face needs attention. Save may be used before every face is assigned.</p></div><script src="/static/cluster.js" defer></script>`)
	s.render(w, map[string]any{"Title": "Cluster review", "Account": account, "Body": template.HTML(body.String())})
}

func (s *Server) clusterScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	_, _ = w.Write([]byte(`(function () {
  const page = document.getElementById('cluster-page'); if (!page) return;
  const message = document.getElementById('cluster-message');
  function stateFor(card) { const person = card.querySelector('.face-person').value; const selectedState = card.querySelector('.face-state').value; if (person === '__ambiguous__') return 'ambiguous'; if (person && person === card.dataset.originalPerson && (selectedState === 'assigned-system' || selectedState === 'provisional')) return selectedState; if (person) return 'assigned-user'; return selectedState; }
  function refresh(card) { const state = stateFor(card); card.style.borderColor = state === 'assigned-user' ? '#2a9d50' : (state === 'unknown' || state === 'ambiguous' ? '#c62828' : '#d6a800'); }
  document.querySelectorAll('.cluster-face').forEach((card) => { card.querySelector('.face-person').addEventListener('change', () => { if (card.querySelector('.face-person').value !== card.dataset.originalPerson) card.querySelector('.face-state').value = 'assigned-user'; refresh(card); }); refresh(card); });
  document.getElementById('save-cluster').onclick = async () => {
    const decisions = Array.from(document.querySelectorAll('.cluster-face')).map((card) => { const person = card.querySelector('.face-person').value; const disposition = card.querySelector('.face-disposition').value; const state = stateFor(card); return {face_id: card.dataset.faceId, person_id: person === '__ambiguous__' ? '' : person, state: state, disposition: disposition, original_state: card.dataset.originalState, original_person: card.dataset.originalPerson}; }).filter((decision) => decision.disposition || decision.state !== decision.original_state || decision.person_id !== decision.original_person);
    const payload = {default_person_id: document.getElementById('default-person').value, status: document.getElementById('cluster-status').value, note: document.getElementById('cluster-note').value, expected_version: Number(page.dataset.version), decisions: decisions};
    const response = await fetch('/api/clusters/' + encodeURIComponent(page.dataset.clusterId) + '/review', {method:'POST', headers:{'Content-Type':'application/json','X-CSRF-Token':page.dataset.csrf}, body:JSON.stringify(payload)});
    if (!response.ok) { message.textContent = await response.text(); message.className = 'error'; return; }
    message.textContent = 'Review saved.'; location.reload();
  };
})();
`))
}

func faceStateColor(state string) string {
	switch state {
	case string(review.AssignedUser):
		return "#2a9d50"
	case string(review.AssignedSystem), string(review.Provisional):
		return "#d6a800"
	default:
		return "#c62828"
	}
}

func percent(value float64, denominator int) string {
	if denominator <= 0 {
		return "0"
	}
	return fmt.Sprintf("%.3f", value/float64(denominator)*100)
}

func selectedAttr(value, expected string) string {
	if value == expected {
		return ` selected`
	}
	return ""
}

func formatScore(value float64) string {
	if value >= 1 {
		return "100"
	}
	if value <= 0 {
		return "0.0"
	}
	return fmt.Sprintf("%.1f", value*100)
}

func formatScoreUnavailable(value float64, available bool) string {
	if !available {
		return "—"
	}
	return formatScore(value) + "%"
}

func assignmentLabel(state string) string {
	switch state {
	case string(review.AssignedUser):
		return "User"
	case string(review.AssignedSystem):
		return "System"
	case string(review.Provisional):
		return "Provisional"
	case "unknown":
		return "Unknown"
	case "ambiguous":
		return "Ambiguous"
	default:
		return state
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func idSuffix(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[len(value)-8:]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func csrfCookie(r *http.Request) string {
	if cookie, err := r.Cookie("csrf"); err == nil {
		return cookie.Value
	}
	return ""
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
	var collectionID, runStatus string
	if err := s.db.QueryRowContext(r.Context(), `SELECT pp.collection_id, cr.status FROM cluster_memberships cm JOIN clusters c ON c.id = cm.cluster_id JOIN clustering_runs cr ON cr.id = c.clustering_run_id JOIN faces f ON f.id = cm.face_id JOIN analyses a ON a.id = f.analysis_id JOIN photo_assets pa ON pa.id = a.asset_id JOIN photo_paths pp ON pp.asset_id = pa.id WHERE cm.cluster_id = ? LIMIT 1`, clusterID).Scan(&collectionID, &runStatus); err != nil {
		http.Error(w, "cluster not found", http.StatusNotFound)
		return
	}
	if runStatus == "superseded" {
		http.Error(w, "historical cluster runs are read-only", http.StatusConflict)
		return
	}
	allowed, err := s.auth.CanAccessCollection(r.Context(), account.ID, collectionID, true, false)
	if account.Role == auth.Viewer || err != nil || !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var input struct {
		DefaultPersonID string                       `json:"default_person_id"`
		Status          string                       `json:"status"`
		Note            string                       `json:"note"`
		Decisions       []review.ClusterFaceDecision `json:"decisions"`
		ExpectedVersion int                          `json:"expected_version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid review", http.StatusBadRequest)
		return
	}
	if err := review.NewStore(s.db).ReviewCluster(r.Context(), clusterID, input.DefaultPersonID, input.Status, input.ExpectedVersion, input.Decisions, review.Actor{Type: "user", AccountID: account.ID}, input.Note); err != nil {
		if errors.Is(err, review.ErrUndoConflict) || strings.Contains(err.Error(), "review conflict") {
			http.Error(w, err.Error(), http.StatusConflict)
		} else {
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cluster_id": clusterID, "status": input.Status})
}

func (s *Server) photos(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	allowed, err := s.auth.AccessibleCollections(r.Context(), account.ID)
	if err != nil {
		http.Error(w, "permission lookup failed", http.StatusInternalServerError)
		return
	}
	query := r.URL.Query()
	options := search.Options{
		AllowedCollectionIDs: allowed,
		CollectionID:         query.Get("collection_id"), PersonID: query.Get("person_id"),
		RelativePrefix: query.Get("directory"), CaptureDateFrom: query.Get("capture_date_from"),
		CaptureDateUntil:      query.Get("capture_date_until"),
		IncludeProvisional:    query.Get("include_provisional") == "true",
		IncludeUnknown:        query.Get("include_unknown") == "true",
		IncludeAmbiguous:      query.Get("include_ambiguous") == "true",
		IncludeDontTrack:      query.Get("include_dont_track") == "true",
		OnlyConfirmed:         query.Get("only_confirmed") == "true",
		OnlyUnconfirmed:       query.Get("only_unconfirmed") == "true",
		ReviewMode:            query.Get("review_mode") == "true",
		IncludeAssignedSystem: query.Get("include_assigned_system") == "true",
		IncludeAssignedUser:   query.Get("include_assigned_user") == "true",
		AssignmentStates:      splitCSV(query.Get("states")),
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
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		http.Error(w, "invalid collection root", http.StatusForbidden)
		return
	}
	target, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		http.Error(w, "invalid photo path", http.StatusInternalServerError)
		return
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		http.Error(w, "invalid photo path", http.StatusForbidden)
		return
	}
	within, err := filepath.Rel(root, target)
	if err != nil || within == ".." || len(within) >= 3 && within[:3] == ".."+string(filepath.Separator) {
		http.Error(w, "invalid photo path", http.StatusForbidden)
		return
	}
	http.ServeFile(w, r, target)
}

func (s *Server) personRepresentative(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	personID := r.PathValue("personID")
	allowed, err := s.personPermission(r, account, personID, false)
	if err != nil || !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	image, err := representative.NewStoreWithPython(s.db, s.pythonPath).Ensure(r.Context(), personID)
	if errors.Is(err, representative.ErrNoUsableFace) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "representative lookup failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", image.MIMEType)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(image.Data)
}

func (s *Server) setPersonRepresentative(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	if !validCSRFApi(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	personID := r.PathValue("personID")
	allowed, err := s.personPermission(r, account, personID, true)
	if err != nil || !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var input struct {
		FaceID string `json:"face_id"`
		Clear  bool   `json:"clear"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || (input.FaceID == "" && !input.Clear) || (input.FaceID != "" && input.Clear) {
		http.Error(w, "provide exactly one of face_id or clear", http.StatusBadRequest)
		return
	}
	store := representative.NewStoreWithPython(s.db, s.pythonPath)
	if input.Clear {
		if err := store.Clear(r.Context(), personID, review.Actor{Type: "user", AccountID: account.ID}); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		image, err := store.Ensure(r.Context(), personID)
		if errors.Is(err, representative.ErrNoUsableFace) {
			writeJSON(w, http.StatusOK, map[string]any{"person_id": personID, "face_id": "", "selection_mode": "automatic"})
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, representativeSummary(image))
		return
	}
	faceAllowed, err := s.facePermission(r, account, input.FaceID, true)
	if err != nil || !faceAllowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	image, err := store.Select(r.Context(), personID, input.FaceID, review.Actor{Type: "user", AccountID: account.ID})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, representativeSummary(image))
}

func representativeSummary(image representative.Image) map[string]any {
	return map[string]any{"person_id": image.PersonID, "face_id": image.FaceID, "selection_mode": image.SelectionMode, "face_quality": image.FaceQuality, "detection_confidence": image.DetectionConfidence, "crop_version": image.CropVersion, "mime_type": image.MIMEType, "width": image.Width, "height": image.Height, "updated_at": image.UpdatedAt}
}

func (s *Server) personPermission(r *http.Request, account auth.Account, personID string, modify bool) (bool, error) {
	if modify && account.Role == auth.Viewer {
		return false, nil
	}
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT DISTINCT pp.collection_id
		FROM current_face_assignments ca JOIN faces f ON f.id = ca.face_id
		JOIN analyses a ON a.id = f.analysis_id JOIN photo_assets pa ON pa.id = a.asset_id
		JOIN photo_paths pp ON pp.asset_id = pa.id
		WHERE ca.person_id = ?`, personID)
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
	return false, fmt.Errorf("person %q is not in an accessible collection (found=%t)", personID, found)
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

func (s *Server) faceDisposition(w http.ResponseWriter, r *http.Request) {
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
		Disposition string `json:"disposition"`
		Active      *bool  `json:"active"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || input.Disposition == "" {
		http.Error(w, "invalid disposition", http.StatusBadRequest)
		return
	}
	active := true
	if input.Active != nil {
		active = *input.Active
	}
	if err := review.NewStore(s.db).SetDisposition(r.Context(), faceID, input.Disposition, active, review.Actor{Type: "user", AccountID: account.ID}, "web-review"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"face_id": faceID, "disposition": input.Disposition, "active": active})
}

func (s *Server) facePermission(r *http.Request, account auth.Account, faceID string, modify bool) (bool, error) {
	if modify && account.Role == auth.Viewer {
		return false, nil
	}
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

func (s *Server) undoAudit(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	if account.Role == auth.Viewer || !validCSRFApi(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	eventID := r.PathValue("eventID")
	event, err := audit.NewStore(s.db).Get(r.Context(), eventID)
	if err != nil {
		http.Error(w, "audit event not found", http.StatusNotFound)
		return
	}
	var collectionID string
	switch event.EntityType {
	case "face":
		err = s.db.QueryRowContext(r.Context(), `SELECT pp.collection_id FROM faces f JOIN analyses a ON a.id = f.analysis_id JOIN photo_assets pa ON pa.id = a.asset_id JOIN photo_paths pp ON pp.asset_id = pa.id WHERE f.id = ? LIMIT 1`, event.EntityID).Scan(&collectionID)
	case "cluster":
		err = s.db.QueryRowContext(r.Context(), `SELECT pp.collection_id FROM cluster_memberships cm JOIN faces f ON f.id = cm.face_id JOIN analyses a ON a.id = f.analysis_id JOIN photo_assets pa ON pa.id = a.asset_id JOIN photo_paths pp ON pp.asset_id = pa.id WHERE cm.cluster_id = ? LIMIT 1`, event.EntityID).Scan(&collectionID)
	default:
		http.Error(w, "audit event is not an identity review action", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, "entity not found", http.StatusNotFound)
		return
	}
	allowed, err := s.auth.CanAccessCollection(r.Context(), account.ID, collectionID, false, true)
	if err != nil || !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err := review.NewStore(s.db).UndoAuditEvent(r.Context(), eventID, review.Actor{Type: "user", AccountID: account.ID}); err != nil {
		if errors.Is(err, review.ErrUndoConflict) {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"undone_audit_event": eventID})
}

func (s *Server) adminAccounts(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	if account.Role != auth.SystemAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT username, role, disabled_at IS NOT NULL FROM accounts ORDER BY username`)
	if err != nil {
		http.Error(w, "account lookup failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var body strings.Builder
	body.WriteString(`<h1>Accounts</h1><table><tr><th>Username</th><th>Role</th><th>Disabled</th></tr>`)
	for rows.Next() {
		var username, role string
		var disabled int
		if rows.Scan(&username, &role, &disabled) == nil {
			body.WriteString(`<tr><td>` + template.HTMLEscapeString(username) + `</td><td>` + template.HTMLEscapeString(role) + `</td><td>` + fmt.Sprint(disabled == 1) + `</td></tr>`)
		}
	}
	body.WriteString(`</table><p>Use the account CLI for creation, password reset, disablement, and collection permission changes.</p>`)
	s.render(w, map[string]any{"Title": "Accounts", "Account": account, "Body": template.HTML(body.String())})
}

func (s *Server) adminCollections(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	if account.Role != auth.SystemAdmin && account.Role != auth.CollectionManager {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT name, root_path, enabled FROM collections ORDER BY name`)
	if err != nil {
		http.Error(w, "collection lookup failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var body strings.Builder
	body.WriteString(`<h1>Collections</h1><table><tr><th>Name</th><th>Root</th><th>Enabled</th></tr>`)
	for rows.Next() {
		var name, root string
		var enabled int
		if rows.Scan(&name, &root, &enabled) == nil {
			body.WriteString(`<tr><td>` + template.HTMLEscapeString(name) + `</td><td>` + template.HTMLEscapeString(root) + `</td><td>` + fmt.Sprint(enabled == 1) + `</td></tr>`)
		}
	}
	body.WriteString(`</table><p>Use the collection CLI for creation and lifecycle changes.</p>`)
	s.render(w, map[string]any{"Title": "Collections", "Account": account, "Body": template.HTML(body.String())})
}

func (s *Server) adminActor(w http.ResponseWriter, r *http.Request, managerOK bool) (auth.Account, bool) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return auth.Account{}, false
	}
	if account.Role != auth.SystemAdmin && (!managerOK || account.Role != auth.CollectionManager) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return auth.Account{}, false
	}
	if !validCSRFApi(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return auth.Account{}, false
	}
	return account, true
}

func (s *Server) adminCreateAccount(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.adminActor(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Username   string    `json:"username"`
		Password   string    `json:"password"`
		Role       auth.Role `json:"role"`
		MustChange bool      `json:"must_change"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		http.Error(w, "invalid account", http.StatusBadRequest)
		return
	}
	if actor.Role == auth.CollectionManager && (input.Role == auth.SystemAdmin || input.Role == auth.CollectionManager) {
		http.Error(w, "collection managers may only create reviewer or viewer accounts", http.StatusForbidden)
		return
	}
	account, err := s.auth.CreateAccount(r.Context(), input.Username, input.Password, input.Role, input.MustChange)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": account.ID, "username": account.Username, "role": account.Role})
}

func (s *Server) adminResetPassword(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.adminActor(w, r, false); !ok {
		return
	}
	var input struct {
		Password   string `json:"password"`
		MustChange bool   `json:"must_change"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		http.Error(w, "invalid password", http.StatusBadRequest)
		return
	}
	if err := s.auth.ResetPassword(r.Context(), r.PathValue("accountID"), input.Password, input.MustChange); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"reset": true})
}

func (s *Server) adminDisableAccount(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.adminActor(w, r, false); !ok {
		return
	}
	var input struct {
		Disabled bool `json:"disabled"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		http.Error(w, "invalid account state", http.StatusBadRequest)
		return
	}
	if err := s.auth.SetDisabled(r.Context(), r.PathValue("accountID"), input.Disabled); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"disabled": input.Disabled})
}

func (s *Server) adminPermission(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.adminActor(w, r, false); !ok {
		return
	}
	var input struct {
		AccountID    string `json:"account_id"`
		CollectionID string `json:"collection_id"`
		View         bool   `json:"view"`
		Modify       bool   `json:"modify"`
		Undo         bool   `json:"undo"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil {
		http.Error(w, "invalid permission", http.StatusBadRequest)
		return
	}
	if err := s.auth.SetCollectionPermission(r.Context(), input.AccountID, input.CollectionID, input.View, input.Modify, input.Undo); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"saved": true})
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
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self'; form-action 'self'; frame-ancestors 'none'")
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

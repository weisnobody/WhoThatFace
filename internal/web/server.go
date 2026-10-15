// Package web implements the authenticated local review HTTP service.
package web

import (
	"context"
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

	"github.com/pjg/face_me_bro/internal/analysis"
	"github.com/pjg/face_me_bro/internal/audit"
	"github.com/pjg/face_me_bro/internal/auth"
	"github.com/pjg/face_me_bro/internal/candidates"
	"github.com/pjg/face_me_bro/internal/id"
	"github.com/pjg/face_me_bro/internal/jobs"
	"github.com/pjg/face_me_bro/internal/media"
	"github.com/pjg/face_me_bro/internal/representative"
	"github.com/pjg/face_me_bro/internal/review"
	"github.com/pjg/face_me_bro/internal/search"
)

type Server struct {
	auth       auth.Store
	db         *sql.DB
	pythonPath string
	templates  *template.Template
	statusJSON []byte
	setupJSON  []byte
}

type possibleCandidateView struct {
	ID, RunID, PersonID, PersonName string
	PipelineID, ReferenceSetVersion string
	Rank                            int
	Similarity, Margin              float64
	Support                         int
	Quality, Detection              float64
	Evidence, RetentionReason       string
	CreatedAt                       string
	Current                         bool
}

func (s *Server) currentPossibleCandidates(ctx context.Context, faceID string) ([]possibleCandidateView, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT pc.id, pc.run_id, pc.person_id,
		       COALESCE(NULLIF(p.name, ''), substr(p.id, -8), p.id),
		       pc.rank, pc.similarity, pc.margin, pc.support_count,
		       pc.quality, pc.detection_confidence, pc.evidence,
		       pc.retention_reason, pc.created_at, cr.pipeline_id,
		       cr.reference_set_version
		FROM possible_face_candidates pc
		JOIN people p ON p.id = pc.person_id
		JOIN possible_candidate_runs cr ON cr.id = pc.run_id
		WHERE pc.face_id = ? AND pc.is_current = 1
		ORDER BY pc.rank`, faceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []possibleCandidateView
	for rows.Next() {
		var value possibleCandidateView
		if err := rows.Scan(&value.ID, &value.RunID, &value.PersonID, &value.PersonName,
			&value.Rank, &value.Similarity, &value.Margin, &value.Support,
			&value.Quality, &value.Detection, &value.Evidence,
			&value.RetentionReason, &value.CreatedAt, &value.PipelineID,
			&value.ReferenceSetVersion); err != nil {
			return nil, err
		}
		value.Current = true
		result = append(result, value)
	}
	return result, rows.Err()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func checkedAttr(value bool) string {
	if value {
		return ` checked`
	}
	return ""
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

// NewServerWithDiagnostics configures the dashboard's status and setup-check
// pages with the same snapshots used when the serve command starts.
func NewServerWithDiagnostics(authStore auth.Store, database *sql.DB, pythonPath string, statusJSON, setupJSON []byte) (*Server, error) {
	server, err := newServer(authStore, database, pythonPath)
	if err != nil {
		return nil, err
	}
	server.statusJSON = append([]byte(nil), statusJSON...)
	server.setupJSON = append([]byte(nil), setupJSON...)
	return server, nil
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
	mux.HandleFunc("GET /static/admin.js", s.adminScript)
	mux.HandleFunc("POST /api/faces/{faceID}/assignment", s.assignFace)
	mux.HandleFunc("POST /api/faces/assignments", s.assignFaces)
	mux.HandleFunc("POST /api/faces/{faceID}/disposition", s.faceDisposition)
	mux.HandleFunc("POST /api/faces/{faceID}/reference", s.referenceFace)
	mux.HandleFunc("GET /", s.dashboard)
	mux.HandleFunc("GET /review/photos", s.reviewPhotos)
	mux.HandleFunc("GET /review/people", s.reviewPeople)
	mux.HandleFunc("GET /review/people/{personID}", s.reviewPerson)
	mux.HandleFunc("GET /review/candidate-runs/{runID}", s.reviewCandidateRun)
	mux.HandleFunc("GET /review/people/{personID}/references", s.reviewPersonReferences)
	mux.HandleFunc("POST /api/people", s.createPerson)
	mux.HandleFunc("POST /api/people/{personID}", s.renamePerson)
	mux.HandleFunc("POST /api/people/{personID}/reference-reprocess", s.referenceReprocess)
	mux.HandleFunc("POST /api/people/{personID}/dont-track", s.setPersonDontTrack)
	mux.HandleFunc("GET /api/people/{personID}/references", s.personReferences)
	mux.HandleFunc("GET /api/people/{personID}/representative", s.personRepresentative)
	mux.HandleFunc("POST /api/people/{personID}/representative", s.setPersonRepresentative)
	mux.HandleFunc("GET /review/clusters", s.reviewClusters)
	mux.HandleFunc("GET /review/clusters/{clusterID}", s.reviewCluster)
	mux.HandleFunc("POST /api/clusters/{clusterID}/review", s.reviewClusterAPI)
	mux.HandleFunc("POST /api/audit/{eventID}/undo", s.undoAudit)
	mux.HandleFunc("GET /admin/accounts", s.adminAccounts)
	mux.HandleFunc("GET /admin", s.adminDashboard)
	mux.HandleFunc("GET /admin/jobs", s.adminJobs)
	mux.HandleFunc("GET /admin/jobs/{jobID}", s.adminJob)
	mux.HandleFunc("GET /admin/runs", s.adminRuns)
	mux.HandleFunc("GET /admin/runs/{runID}", s.adminRun)
	mux.HandleFunc("GET /admin/scans", s.adminScans)
	mux.HandleFunc("GET /admin/scans/{scanID}", s.adminScan)
	mux.HandleFunc("GET /admin/audit", s.adminAudit)
	mux.HandleFunc("GET /admin/audit/{eventID}", s.adminAuditEvent)
	mux.HandleFunc("GET /admin/pipelines", s.adminPipelines)
	mux.HandleFunc("GET /admin/pipelines/{pipelineID}", s.adminPipeline)
	mux.HandleFunc("GET /admin/accounts/{accountID}", s.adminAccount)
	mux.HandleFunc("GET /admin/status", s.adminStatus)
	mux.HandleFunc("GET /admin/setup-check", s.adminSetupCheck)
	mux.HandleFunc("GET /admin/collections", s.adminCollections)
	mux.HandleFunc("POST /api/admin/accounts", s.adminCreateAccount)
	mux.HandleFunc("POST /api/admin/collections", s.adminCreateCollection)
	mux.HandleFunc("POST /api/admin/pipelines", s.adminCreatePipeline)
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
	if candidatePerson := strings.TrimSpace(query.Get("candidate_person")); candidatePerson != "" {
		candidateQuery := `SELECT DISTINCT pp.id, pp.relative_path, COALESCE(pa.width, 0), COALESCE(pa.height, 0), COUNT(DISTINCT f.id)
			FROM possible_face_candidates pc JOIN faces f ON f.id = pc.face_id JOIN analyses a ON a.id = f.analysis_id
			JOIN photo_assets pa ON pa.id = a.asset_id JOIN photo_paths pp ON pp.asset_id = pa.id
			WHERE pc.person_id = ? AND pc.is_current = 1 AND pp.missing_at IS NULL`
		candidateArgs := []any{candidatePerson}
		if allowed != nil {
			candidateQuery += ` AND pp.collection_id IN (` + strings.TrimRight(strings.Repeat("?,", len(allowed)), ",") + `)`
			for _, collectionID := range allowed {
				candidateArgs = append(candidateArgs, collectionID)
			}
		}
		candidateQuery += ` GROUP BY pp.id, pp.relative_path, pa.width, pa.height ORDER BY pp.relative_path`
		candidateRows, candidateErr := s.db.QueryContext(r.Context(), candidateQuery, candidateArgs...)
		if candidateErr != nil {
			http.Error(w, "candidate photo lookup failed", http.StatusInternalServerError)
			return
		}
		var body strings.Builder
		body.WriteString(`<h1>Possible candidate photos</h1><p>Photos containing current possible matches for ` + template.HTMLEscapeString(idSuffix(candidatePerson)) + `.</p>`)
		for candidateRows.Next() {
			var pathID, relative string
			var width, height, faceCount int
			if err := candidateRows.Scan(&pathID, &relative, &width, &height, &faceCount); err != nil {
				candidateRows.Close()
				http.Error(w, "candidate photo lookup failed", http.StatusInternalServerError)
				return
			}
			body.WriteString(`<figure><a href="/review/photos?path_id=` + template.HTMLEscapeString(pathID) + `"><img loading="lazy" style="max-width:20rem;max-height:15rem" src="/api/photos/` + template.HTMLEscapeString(pathID) + `"></a><figcaption><a href="/review/photos?path_id=` + template.HTMLEscapeString(pathID) + `">` + template.HTMLEscapeString(relative) + `</a> (` + fmt.Sprint(faceCount) + ` faces)</figcaption></figure>`)
		}
		candidateRows.Close()
		s.render(w, map[string]any{"Title": "Possible candidate photos", "Account": account, "Body": template.HTML(body.String())})
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
		PossibleCandidates       []possibleCandidateView
	}
	var faces []face
	for rows.Next() {
		var value face
		var reference int
		if err := rows.Scan(&value.ID, &value.Index, &value.Left, &value.Top, &value.Right, &value.Bottom, &value.State, &value.PersonID, &value.Person, &value.Similarity, &value.Margin, &reference); err != nil {
			return
		}
		value.Reference = reference == 1
		value.PossibleCandidates, err = s.currentPossibleCandidates(r.Context(), value.ID)
		if err != nil {
			http.Error(w, "possible candidate lookup failed", http.StatusInternalServerError)
			return
		}
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
		if len(value.PossibleCandidates) > 0 {
			body.WriteString(` · possible matches: `)
			for index, candidate := range value.PossibleCandidates {
				if index > 0 {
					body.WriteString(`, `)
				}
				body.WriteString(`<a href="/review/people/` + template.HTMLEscapeString(candidate.PersonID) + `">` + template.HTMLEscapeString(candidate.PersonName) + `</a> (S: ` + formatScore(candidate.Similarity) + `%, M: ` + formatScore(candidate.Margin) + `%, Support: ` + fmt.Sprint(candidate.Support) + `, Q: ` + formatScore(candidate.Quality) + `%, D: ` + formatScore(candidate.Detection) + `%, ` + template.HTMLEscapeString(candidate.Evidence) + `, possible/current, generated: ` + template.HTMLEscapeString(candidate.CreatedAt) + `, pipeline: ` + template.HTMLEscapeString(candidate.PipelineID) + `, <a href="/review/candidate-runs/` + template.HTMLEscapeString(candidate.RunID) + `">run</a>)`)
				if account.Role != auth.Viewer {
					body.WriteString(` <button type="button" data-action="assign-candidate" data-face-id="` + template.HTMLEscapeString(value.ID) + `" data-person-id="` + template.HTMLEscapeString(candidate.PersonID) + `" data-candidate-id="` + template.HTMLEscapeString(candidate.ID) + `">Assign</button>`)
				}
			}
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
	view := r.URL.Query().Get("view")
	if view != "possibles" && view != "dont-track" && view != "no-faces" {
		view = "all"
	}
	possibleCount := `(SELECT COUNT(DISTINCT pc.face_id) FROM possible_face_candidates pc WHERE pc.person_id = p.id AND pc.is_current = 1)`
	clusterCount := `(SELECT COUNT(DISTINCT c.id) FROM clusters c JOIN clustering_runs cr ON cr.id = c.clustering_run_id WHERE cr.status = 'active' AND (c.proposed_person_id = p.id OR EXISTS (SELECT 1 FROM cluster_memberships cm JOIN current_face_assignments cca ON cca.face_id = cm.face_id WHERE cm.cluster_id = c.id AND cca.person_id = p.id)))`
	var possibleArgs []any
	if allowed != nil {
		placeholders := make([]string, len(allowed))
		for i := range allowed {
			placeholders[i] = "?"
			possibleArgs = append(possibleArgs, allowed[i])
		}
		possibleCount = `(SELECT COUNT(DISTINCT pc.face_id) FROM possible_face_candidates pc JOIN faces pcf ON pcf.id = pc.face_id JOIN analyses pca ON pca.id = pcf.analysis_id JOIN photo_paths pcp ON pcp.asset_id = pca.asset_id WHERE pc.person_id = p.id AND pc.is_current = 1 AND pcp.collection_id IN (` + strings.Join(placeholders, ",") + `))`
	}
	peopleQuery := `SELECT p.id, COALESCE(NULLIF(p.name, ''), substr(p.id, -8)), p.dont_track,
		COUNT(DISTINCT CASE WHEN pp.missing_at IS NULL THEN ca.face_id END),
		COUNT(DISTINCT CASE WHEN pp.missing_at IS NULL AND ca.state = 'assigned-user' THEN ca.face_id END),
		COUNT(DISTINCT CASE WHEN pp.missing_at IS NULL AND ca.state = 'assigned-system' THEN ca.face_id END),
		COUNT(DISTINCT CASE WHEN pp.missing_at IS NULL AND ca.state = 'provisional' THEN ca.face_id END),
		COUNT(DISTINCT CASE WHEN pp.missing_at IS NULL AND cfr.reference = 1 THEN ca.face_id END),
		COUNT(DISTINCT CASE WHEN pp.missing_at IS NULL AND rr.is_current = 1 THEN ca.face_id END),
		` + clusterCount + `,
		` + possibleCount + `
		FROM people p
		LEFT JOIN current_face_assignments ca ON ca.person_id = p.id
		LEFT JOIN faces f ON f.id = ca.face_id
		LEFT JOIN analyses a ON a.id = f.analysis_id
		LEFT JOIN photo_assets pa ON pa.id = a.asset_id
		LEFT JOIN photo_paths pp ON pp.asset_id = pa.id
		LEFT JOIN current_face_references cfr ON cfr.face_id = ca.face_id AND cfr.person_id = p.id
		LEFT JOIN reference_recommendations rr ON rr.face_id = ca.face_id AND rr.person_id = p.id
		WHERE `
	args := append([]any(nil), possibleArgs...)
	if allowed != nil && view != "no-faces" {
		if len(allowed) == 0 {
			body := `<h1>People</h1><p>No collections are assigned to this account.</p>`
			s.render(w, map[string]any{"Title": "People", "Account": account, "Body": template.HTML(body)})
			return
		}
		placeholders := make([]string, len(allowed))
		for i := range allowed {
			placeholders[i] = "?"
		}
		peopleQuery += ` (EXISTS (SELECT 1 FROM faces f2 JOIN analyses a2 ON a2.id = f2.analysis_id JOIN photo_assets pa2 ON pa2.id = a2.asset_id JOIN photo_paths pp2 ON pp2.asset_id = pa2.id JOIN current_face_assignments ca2 ON ca2.face_id = f2.id WHERE ca2.person_id = p.id AND pp2.collection_id IN (` + strings.Join(placeholders, ",") + `))`
		peopleQuery += `)`
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
		ID, Name                                                                                  string
		DontTrack, Faces, User, System, Provisional, References, Recommended, Clusters, Possibles int
	}
	var people []personRow
	for rows.Next() {
		var id, name string
		var row personRow
		if err := rows.Scan(&id, &name, &row.DontTrack, &row.Faces, &row.User, &row.System, &row.Provisional, &row.References, &row.Recommended, &row.Clusters, &row.Possibles); err != nil {
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
	includeDontTrack := r.URL.Query().Get("include_dont_track") == "true"
	includePossibles := r.URL.Query().Get("include_possibles") != "false"
	includeNoFaces := r.URL.Query().Get("include_no_faces") == "true"
	assignmentFiltersSpecified := false
	for _, name := range []string{"assignment_user", "assignment_system", "assignment_provisional", "assignment_other"} {
		if _, ok := r.URL.Query()[name]; ok {
			assignmentFiltersSpecified = true
			break
		}
	}
	includeAssignmentUser := !assignmentFiltersSpecified || r.URL.Query().Get("assignment_user") == "true"
	includeAssignmentSystem := !assignmentFiltersSpecified || r.URL.Query().Get("assignment_system") == "true"
	includeAssignmentProvisional := !assignmentFiltersSpecified || r.URL.Query().Get("assignment_provisional") == "true"
	includeAssignmentOther := !assignmentFiltersSpecified || r.URL.Query().Get("assignment_other") == "true"
	filtered := people[:0]
	for _, person := range people {
		if q != "" && !strings.Contains(strings.ToLower(person.Name), q) {
			continue
		}
		switch view {
		case "possibles":
			if person.Possibles == 0 {
				continue
			}
		case "dont-track":
			if person.DontTrack == 0 {
				continue
			}
		case "no-faces":
			if person.Faces != 0 {
				continue
			}
		}
		if view != "no-faces" {
			if view != "dont-track" && !includeDontTrack && person.DontTrack != 0 {
				continue
			}
			if !includePossibles && person.Possibles > 0 {
				continue
			}
			if !includeNoFaces && person.Faces == 0 {
				continue
			}
		}
		other := person.Faces - person.User - person.System - person.Provisional
		if view == "no-faces" {
			filtered = append(filtered, person)
			continue
		}
		if (includeAssignmentUser && person.User > 0) ||
			(includeAssignmentSystem && person.System > 0) ||
			(includeAssignmentProvisional && person.Provisional > 0) ||
			(includeAssignmentOther && other > 0) {
			filtered = append(filtered, person)
		}
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
		case "clusters":
			left, right = fmt.Sprintf("%09d", people[i].Clusters), fmt.Sprintf("%09d", people[j].Clusters)
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
	body.WriteString(`<h1>People (` + fmt.Sprint(len(pagePeople)) + `/` + fmt.Sprint(total) + `)</h1><form id="create-person"><input name="name" placeholder="New person name" required><button>Create person</button></form><form id="people-controls" method="get"><strong>Filter</strong> <input name="q" value="` + template.HTMLEscapeString(r.URL.Query().Get("q")) + `" placeholder="Filter name"><label><strong>View</strong> <select name="view"><option value="all"` + selectedAttr("all", view) + `>All</option><option value="possibles"` + selectedAttr("possibles", view) + `>Possibles</option><option value="dont-track"` + selectedAttr("dont-track", view) + `>Don't Track</option><option value="no-faces"` + selectedAttr("no-faces", view) + `>No Faces</option></select></label><strong>Include:</strong> <label><input type="checkbox" name="include_possibles" value="true"` + checkedAttr(includePossibles) + `> Possibles</label><label><input type="checkbox" name="include_no_faces" value="true"` + checkedAttr(includeNoFaces) + `> No Faces</label><label><input type="checkbox" name="include_dont_track" value="true"` + checkedAttr(includeDontTrack) + `> Don't Track</label><strong>Assignment:</strong> <label><input type="checkbox" name="assignment_user" value="true"` + checkedAttr(includeAssignmentUser) + `> User</label><label><input type="checkbox" name="assignment_system" value="true"` + checkedAttr(includeAssignmentSystem) + `> System</label><label><input type="checkbox" name="assignment_provisional" value="true"` + checkedAttr(includeAssignmentProvisional) + `> Provisional</label><label><input type="checkbox" name="assignment_other" value="true"` + checkedAttr(includeAssignmentOther) + `> Other</label><span class="people-controls-break"></span><label><strong>Sort</strong> <select name="sort">`)
	for _, option := range []struct{ Value, Label string }{{"name", "Name"}, {"faces", "Faces"}, {"user", "User"}, {"system", "System"}, {"provisional", "Provisional"}, {"clusters", "Clusters"}} {
		body.WriteString(`<option value="` + option.Value + `"` + selectedAttr(option.Value, sortKey) + `>` + option.Label + `</option>`)
	}
	body.WriteString(`</select></label><label><strong>Direction</strong> <select name="direction"><option value="asc"` + selectedAttr("asc", r.URL.Query().Get("direction")) + `>Ascending</option><option value="desc"` + selectedAttr("desc", r.URL.Query().Get("direction")) + `>Descending</option></select></label><label><strong>Per Page</strong> <select name="per_page" onchange="this.form.submit()">`)
	for _, size := range []int{10, 20, 25, 50, 100} {
		body.WriteString(`<option value="` + fmt.Sprint(size) + `"` + selectedAttr(fmt.Sprint(size), fmt.Sprint(perPage)) + `>` + fmt.Sprint(size) + `</option>`)
	}
	body.WriteString(`</select></label><button>Apply</button></form>`)
	renderPager := func() {
		body.WriteString(`<nav class="pager people-page-pager"><a href="` + template.HTMLEscapeString(pageURL(1)) + `">First</a> <a href="` + template.HTMLEscapeString(pageURL(maxInt(1, page-1))) + `">Previous</a> <span>Page ` + fmt.Sprint(page) + `/` + fmt.Sprint(pageCount) + `</span> <a href="` + template.HTMLEscapeString(pageURL(minInt(pageCount, page+1))) + `">Next</a> <a href="` + template.HTMLEscapeString(pageURL(pageCount)) + `">Last</a> <form class="page-jump" method="get"><label>Jump to Page <input type="number" name="page" min="1" value="` + fmt.Sprint(page) + `" onkeydown="if(event.key==='Enter')this.form.submit()"></label><input type="hidden" name="per_page" value="` + fmt.Sprint(perPage) + `"><input type="hidden" name="q" value="` + template.HTMLEscapeString(r.URL.Query().Get("q")) + `"><input type="hidden" name="view" value="` + template.HTMLEscapeString(view) + `"><input type="hidden" name="include_possibles" value="` + template.HTMLEscapeString(r.URL.Query().Get("include_possibles")) + `"><input type="hidden" name="include_no_faces" value="` + template.HTMLEscapeString(r.URL.Query().Get("include_no_faces")) + `"><input type="hidden" name="include_dont_track" value="` + template.HTMLEscapeString(r.URL.Query().Get("include_dont_track")) + `"><input type="hidden" name="assignment_user" value="` + template.HTMLEscapeString(r.URL.Query().Get("assignment_user")) + `"><input type="hidden" name="assignment_system" value="` + template.HTMLEscapeString(r.URL.Query().Get("assignment_system")) + `"><input type="hidden" name="assignment_provisional" value="` + template.HTMLEscapeString(r.URL.Query().Get("assignment_provisional")) + `"><input type="hidden" name="assignment_other" value="` + template.HTMLEscapeString(r.URL.Query().Get("assignment_other")) + `"><input type="hidden" name="sort" value="` + template.HTMLEscapeString(sortKey) + `"><input type="hidden" name="direction" value="` + template.HTMLEscapeString(r.URL.Query().Get("direction")) + `"></form></nav>`)
	}
	renderPager()
	body.WriteString(`<table class="people-table"><tr><th>Name</th><th>Faces</th><th>User</th><th>System</th><th>Provisional</th><th>Clusters</th><th>References</th><th>Ref Recs</th><th>Don't track</th><th>Possibles</th></tr>`)
	for _, person := range pagePeople {
		dontTrack := ""
		if person.DontTrack != 0 {
			dontTrack = "X"
		}
		possible := ""
		if person.Possibles > 0 {
			possible = `<a href="/review/photos?candidate_person=` + template.HTMLEscapeString(person.ID) + `">` + fmt.Sprint(person.Possibles) + `</a>`
		}
		body.WriteString(`<tr><td><img loading="lazy" style="max-height:32px;width:auto;border-radius:4px;vertical-align:middle" alt="" src="/api/people/` + template.HTMLEscapeString(person.ID) + `/representative"> <a href="/review/people/` + template.HTMLEscapeString(person.ID) + `">` + template.HTMLEscapeString(person.Name) + `</a></td><td>` + fmt.Sprint(person.Faces) + `</td><td>` + fmt.Sprint(person.User) + `</td><td>` + fmt.Sprint(person.System) + `</td><td>` + fmt.Sprint(person.Provisional) + `</td><td>` + fmt.Sprint(person.Clusters) + `</td><td>` + fmt.Sprint(person.References) + `</td><td>` + fmt.Sprint(person.Recommended) + `</td><td>` + dontTrack + `</td><td>` + possible + `</td></tr>`)
	}
	body.WriteString(`</table>`)
	renderPager()
	body.WriteString(`<script>(function(){const form=document.getElementById('create-person');form.onsubmit=async function(e){e.preventDefault();const response=await fetch('/api/people',{method:'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':'` + template.JSEscapeString(csrfCookie(r)) + `'},body:JSON.stringify({name:form.name.value})});if(!response.ok){alert(await response.text());return}location.reload()}})();</script>`)
	s.render(w, map[string]any{"Title": "People", "Account": account, "Body": template.HTML(body.String()), "PageStyle": template.CSS(`#create-person{display:flex;align-items:center;flex-wrap:wrap;gap:.4rem;margin:.5rem 0}#people-controls{display:flex;align-items:center;flex-wrap:wrap;gap:.4rem;margin:.5rem 0;width:max-content;max-width:100%;overflow-x:auto}#people-controls .people-controls-break{flex-basis:100%;height:0}#people-controls>button{margin-left:4rem}.people-page-pager{width:max-content;display:flex;align-items:center;gap:.4rem;margin:.5rem 0}.people-page-pager .page-jump{display:flex;align-items:center;gap:.4rem}.people-table{width:max-content}`)})
}

func (s *Server) reviewCandidateRun(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	run, err := candidates.NewStore(s.db).GetRun(r.Context(), r.PathValue("runID"))
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "candidate run lookup failed", http.StatusInternalServerError)
		return
	}
	allowed, err := s.auth.AccessibleCollections(r.Context(), account.ID)
	if err != nil {
		http.Error(w, "permission lookup failed", http.StatusInternalServerError)
		return
	}
	query := `
		SELECT pc.face_id, pp.id, pp.relative_path, pc.person_id,
		       COALESCE(NULLIF(p.name, ''), substr(p.id, -8), p.id), pc.rank,
		       pc.similarity, pc.margin, pc.support_count, pc.evidence,
		       pc.retention_reason, pc.is_current, pc.created_at
		FROM possible_face_candidates pc
		JOIN faces f ON f.id = pc.face_id
		JOIN analyses a ON a.id = f.analysis_id
		JOIN photo_paths pp ON pp.asset_id = a.asset_id
		JOIN people p ON p.id = pc.person_id
		WHERE pc.run_id = ?`
	args := []any{run.ID}
	rowState := r.URL.Query().Get("state")
	if rowState != "current" && rowState != "stale" {
		rowState = "all"
	}
	if rowState == "current" {
		query += ` AND pc.is_current = 1`
	} else if rowState == "stale" {
		query += ` AND pc.is_current = 0`
	}
	if allowed != nil {
		if len(allowed) == 0 {
			s.render(w, map[string]any{"Title": "Candidate run", "Account": account, "Body": template.HTML(`<h1>Candidate run</h1><p>No collections are assigned to this account.</p>`)})
			return
		}
		query += ` AND pp.collection_id IN (` + strings.TrimRight(strings.Repeat("?,", len(allowed)), ",") + `)`
		for _, collectionID := range allowed {
			args = append(args, collectionID)
		}
	}
	query += ` ORDER BY pc.face_id, pc.rank`
	rows, err := s.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		http.Error(w, "candidate lookup failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var body strings.Builder
	body.WriteString(`<h1>Possible candidate run ` + template.HTMLEscapeString(idSuffix(run.ID)) + `</h1><p>Status: ` + template.HTMLEscapeString(run.Status) + `; scope: ` + template.HTMLEscapeString(run.Scope) + `; targets: ` + fmt.Sprint(run.TargetCount) + `; candidates: ` + fmt.Sprint(run.CandidateCount) + `</p><p>Pipeline: ` + template.HTMLEscapeString(run.PipelineID) + `; reference version: ` + template.HTMLEscapeString(run.ReferenceSetVersion) + `</p><p>Rows: <a href="?state=all">all</a> · <a href="?state=current">current</a> · <a href="?state=stale">stale</a></p><table><tr><th>Face</th><th>Photo</th><th>Candidate</th><th>Rank</th><th>Similarity</th><th>Margin</th><th>Support</th><th>Evidence</th><th>Reason</th><th>Row state</th><th>Generated</th></tr>`)
	for rows.Next() {
		var faceID, pathID, relative, personID, personName, evidence, reason, createdAt string
		var rank, support int
		var current int
		var similarity, margin float64
		if err := rows.Scan(&faceID, &pathID, &relative, &personID, &personName, &rank, &similarity, &margin, &support, &evidence, &reason, &current, &createdAt); err != nil {
			http.Error(w, "candidate lookup failed", http.StatusInternalServerError)
			return
		}
		state := "stale"
		if current == 1 {
			state = "current"
		}
		body.WriteString(`<tr><td>` + template.HTMLEscapeString(idSuffix(faceID)) + `</td><td><a href="/review/photos?path_id=` + template.HTMLEscapeString(pathID) + `">` + template.HTMLEscapeString(relative) + `</a></td><td><a href="/review/people/` + template.HTMLEscapeString(personID) + `">` + template.HTMLEscapeString(personName) + `</a></td><td>` + fmt.Sprint(rank) + `</td><td>` + formatScore(similarity) + `%</td><td>` + formatScore(margin) + `%</td><td>` + fmt.Sprint(support) + `</td><td>` + template.HTMLEscapeString(evidence) + `</td><td>` + template.HTMLEscapeString(reason) + `</td><td>` + state + `</td><td>` + template.HTMLEscapeString(createdAt) + `</td></tr>`)
	}
	body.WriteString(`</table>`)
	s.render(w, map[string]any{"Title": "Possible candidate run", "Account": account, "Body": template.HTML(body.String())})
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
	var personDontTrack int
	if err := s.db.QueryRowContext(r.Context(), `SELECT dont_track FROM people WHERE id = ?`, personID).Scan(&personDontTrack); err != nil {
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
	query := `SELECT f.id, pp.id, pp.relative_path, COALESCE(col.name, ''), COALESCE(pa.width, a.source_width, 0), COALESCE(pa.height, a.source_height, 0),
		f.box_left, f.box_top, f.box_right, f.box_bottom, COALESCE(f.quality, 0), COALESCE(f.detection_confidence, 0),
		CASE WHEN ae.reason = 'cluster-provisional' AND COALESCE(ae.support_count, 0) = 0 AND COALESCE(ae.quality, 0) = 0 AND COALESCE(ae.detection_confidence, 0) = 0 AND ae.agreement IS NULL THEN NULL ELSE ae.similarity END, COALESCE(ca.state, 'unassigned'), COALESCE(ca.person_id, ''), COALESCE(cfr.reference, 0)
		FROM faces f JOIN analyses a ON a.id = f.analysis_id
		JOIN photo_assets pa ON pa.id = a.asset_id JOIN photo_paths pp ON pp.asset_id = pa.id
		JOIN collections col ON col.id = pp.collection_id
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
		PersonCount                                                  int
		Purity, Consistency, Connectivity                            float64
		PurityAvailable, ConsistencyAvailable, ConnectivityAvailable bool
		Quality, Detection                                           float64
	}
	type personFace struct {
		FaceID, PathID, RelativePath, Collection string
		Width, Height                            int
		Left, Top, Right, Bottom                 float64
		Quality, Detection                       float64
		Confidence                               float64
		ConfidenceAvailable                      bool
		State, PersonID                          string
		Reference                                bool
		Recommended                              bool
		RecommendationPriority                   int
		Representative                           bool
		Cluster                                  *personCluster
		Candidates                               []personCandidate
		PossibleCandidates                       []possibleCandidateView
	}
	var faces []personFace
	referenceCount := 0
	for rows.Next() {
		var face personFace
		var reference, representativeFace int
		var confidence sql.NullFloat64
		if err := rows.Scan(&face.FaceID, &face.PathID, &face.RelativePath, &face.Collection, &face.Width, &face.Height, &face.Left, &face.Top, &face.Right, &face.Bottom, &face.Quality, &face.Detection, &confidence, &face.State, &face.PersonID, &reference); err != nil {
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
		if face.Recommended {
			_ = s.db.QueryRowContext(r.Context(), `SELECT COUNT(*) + 1 FROM reference_recommendations rr2 WHERE rr2.person_id = ? AND rr2.is_current = 1 AND (rr2.recommendation_score > (SELECT rr1.recommendation_score FROM reference_recommendations rr1 WHERE rr1.face_id = ? AND rr1.person_id = ? AND rr1.is_current = 1 LIMIT 1) OR (rr2.recommendation_score = (SELECT rr1.recommendation_score FROM reference_recommendations rr1 WHERE rr1.face_id = ? AND rr1.person_id = ? AND rr1.is_current = 1 LIMIT 1) AND rr2.face_id < ?))`, personID, face.FaceID, personID, face.FaceID, personID, face.FaceID).Scan(&face.RecommendationPriority)
		}
		var cluster personCluster
		var purity, consistency, connectivity sql.NullFloat64
		if err := s.db.QueryRowContext(r.Context(), `
			SELECT c.id, COALESCE(c.proposed_person_id, ''), COALESCE(NULLIF(p.name, ''), CASE WHEN c.proposed_person_id IS NULL THEN 'Unknown' ELSE substr(c.proposed_person_id, -8) END),
			       COUNT(DISTINCT ca2.person_id), c.purity, c.consistency, c.connectivity,
			       COALESCE(AVG(f2.quality), 0), COALESCE(AVG(f2.detection_confidence), 0)
			FROM cluster_memberships cm
			JOIN clusters c ON c.id = cm.cluster_id
			JOIN clustering_runs cr ON cr.id = c.clustering_run_id AND cr.status = 'active'
			LEFT JOIN people p ON p.id = c.proposed_person_id
			JOIN cluster_memberships cm2 ON cm2.cluster_id = c.id
			JOIN faces f2 ON f2.id = cm2.face_id
			LEFT JOIN current_face_assignments ca2 ON ca2.face_id = cm2.face_id
			WHERE cm.face_id = ?
			GROUP BY c.id, p.name, c.proposed_person_id, c.purity, c.consistency, c.connectivity
			ORDER BY c.id LIMIT 1`, face.FaceID).Scan(&cluster.ID, &cluster.PersonID, &cluster.PersonName, &cluster.PersonCount, &purity, &consistency, &connectivity, &cluster.Quality, &cluster.Detection); err == nil {
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
		face.PossibleCandidates, err = s.currentPossibleCandidates(r.Context(), face.FaceID)
		if err != nil {
			http.Error(w, "possible candidate lookup failed", http.StatusInternalServerError)
			return
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
	totalFaceCount := len(faces)
	hasTrustedFaces := false
	for _, face := range faces {
		if face.State == string(review.AssignedUser) || face.State == string(review.AssignedSystem) {
			hasTrustedFaces = true
			break
		}
	}
	faceFilter := r.URL.Query().Get("state")
	referenceFilter := r.URL.Query().Get("reference")
	clusterFilter := r.URL.Query().Get("cluster")
	possibleFilter := r.URL.Query().Get("possible")
	candidatePersonFilter := strings.TrimSpace(r.URL.Query().Get("candidate_person"))
	evidenceFilter := strings.TrimSpace(r.URL.Query().Get("evidence"))
	if faceFilter != "" || referenceFilter != "" || clusterFilter != "" {
		filtered := faces[:0]
		for _, face := range faces {
			if faceFilter != "" && face.State != faceFilter {
				continue
			}
			if referenceFilter == "recommended" && !face.Recommended {
				continue
			}
			if referenceFilter == "reference" && !face.Reference {
				continue
			}
			if referenceFilter == "reference-or-recommended" && !face.Reference && !face.Recommended {
				continue
			}
			if referenceFilter == "non-reference" && face.Reference {
				continue
			}
			if clusterFilter != "" && (face.Cluster == nil || face.Cluster.ID != clusterFilter) {
				continue
			}
			filtered = append(filtered, face)
		}
		faces = filtered
	}
	if possibleFilter == "" {
		possibleFilter = "without"
	}
	if possibleFilter == "possibles" || possibleFilter == "without" || candidatePersonFilter != "" || evidenceFilter != "" {
		filtered := faces[:0]
		for _, face := range faces {
			matched := true
			if possibleFilter == "possibles" || possibleFilter == "true" {
				matched = len(face.PossibleCandidates) > 0
			} else if possibleFilter == "without" || possibleFilter == "false" {
				matched = len(face.PossibleCandidates) == 0
			}
			if matched && candidatePersonFilter != "" {
				matched = false
				for _, candidate := range face.PossibleCandidates {
					if candidate.PersonID == candidatePersonFilter || strings.EqualFold(candidate.PersonName, candidatePersonFilter) {
						matched = true
						break
					}
				}
			}
			if matched && evidenceFilter != "" {
				matched = false
				for _, candidate := range face.PossibleCandidates {
					if candidate.Evidence == evidenceFilter {
						matched = true
						break
					}
				}
			}
			if matched {
				filtered = append(filtered, face)
			}
		}
		faces = filtered
	}
	faceSort := r.URL.Query().Get("sort")
	sort.SliceStable(faces, func(i, j int) bool {
		less := false
		switch faceSort {
		case "collection":
			less = faces[i].Collection < faces[j].Collection
		case "cluster":
			left, right := "", ""
			if faces[i].Cluster != nil {
				left = faces[i].Cluster.ID
			}
			if faces[j].Cluster != nil {
				right = faces[j].Cluster.ID
			}
			less = left < right
		case "consistency":
			left, right := 0.0, 0.0
			if faces[i].Cluster != nil && faces[i].Cluster.ConsistencyAvailable {
				left = faces[i].Cluster.Consistency
			}
			if faces[j].Cluster != nil && faces[j].Cluster.ConsistencyAvailable {
				right = faces[j].Cluster.Consistency
			}
			less = left < right
		case "ref_recs":
			refRecRank := func(face personFace) int {
				if face.Recommended {
					return 0
				}
				if face.Reference {
					return 1
				}
				return 2
			}
			leftRank, rightRank := refRecRank(faces[i]), refRecRank(faces[j])
			if leftRank != rightRank {
				less = leftRank < rightRank
			} else if leftRank == 0 && faces[i].RecommendationPriority != faces[j].RecommendationPriority {
				less = faces[i].RecommendationPriority < faces[j].RecommendationPriority
			} else {
				less = faces[i].FaceID < faces[j].FaceID
			}
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
	clusterLine := strings.Builder{}
	if len(clusterSummaryOrder) > 0 {
		clusterLine.WriteString(`<div class="person-clusters">Clusters: `)
		for index, clusterPerson := range clusterSummaryOrder {
			if index > 0 {
				clusterLine.WriteString(`, `)
			}
			if clusterPersonIDs[clusterPerson] != "" && clusterPersonIDs[clusterPerson] != personID {
				clusterLine.WriteString(`<a href="/review/people/` + template.HTMLEscapeString(clusterPersonIDs[clusterPerson]) + `">` + template.HTMLEscapeString(clusterPerson) + `</a>`)
			} else {
				clusterLine.WriteString(template.HTMLEscapeString(clusterPerson))
			}
			clusterLine.WriteString(` (`)
			for clusterIndex, clusterID := range clusterSummary[clusterPerson] {
				if clusterIndex > 0 {
					clusterLine.WriteString(`, `)
				}
				clusterLine.WriteString(`<a href="/review/clusters/` + template.HTMLEscapeString(clusterID) + `">` + template.HTMLEscapeString(idSuffix(clusterID)) + `</a>`)
			}
			clusterLine.WriteString(`)`)
		}
		clusterLine.WriteString(`</div>`)
	}

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
.card.assigned{border:2px solid #0a2}
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
.person-identity{display:flex;gap:12px;align-items:stretch}
.person-identity img{flex:0 0 auto;border-radius:8px}
.person-identity-info{display:flex;flex-direction:column;min-width:0}
.person-identity-info h1{margin:0}
.person-name-form{display:flex;align-items:center;gap:8px;margin-top:auto}
.person-name-form .dont-track-button{margin-right:4px}
.person-name-form .recommend-button{margin-left:8px}
.person-clusters{margin-top:6px;font-size:.9em}
.ref-rec{float:right}
.person-footer-note{margin-top:1rem;font-size:.9em;color:#555}
.person-select{min-width:10rem}
.person-select-filter{min-width:10rem}
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
		if hasTrustedFaces {
			recommendButton = `<button type="button" class="recommend-button" data-action="recommend-references">Recommend References</button>`
		}
	}
	dontTrackLabel := "Don't Track"
	if personDontTrack != 0 {
		dontTrackLabel = "Track"
	}
	dontTrackButton := ""
	if canModify {
		dontTrackButton = `<button type="button" class="dont-track-button" data-action="person-dont-track" data-dont-track="` + fmt.Sprint(personDontTrack == 0) + `">` + dontTrackLabel + `</button>`
	}
	body.WriteString(`<div id="person-page" data-person-id="` + template.HTMLEscapeString(personID) + `" data-person-name="` + template.HTMLEscapeString(personName) + `" data-csrf="` + template.HTMLEscapeString(csrfCookie(r)) + `" class="person-header"><div class="person-identity"><img loading="lazy" width="96" height="96" alt="Representative image" src="/api/people/` + template.HTMLEscapeString(personID) + `/representative"><div class="person-identity-info"><h1>` + template.HTMLEscapeString(personName) + ` <span class="person-summary">(` + fmt.Sprint(totalFaceCount) + ` / ` + fmt.Sprint(len(faces)) + ` faces, ` + fmt.Sprint(referenceCount) + ` references, Similarity Max: ` + maxConfidenceText + `, Min: ` + minConfidenceText + `, Cluster Min: ` + minConsistencyText + `, Max: ` + maxConsistencyText + `)</span></h1>` + clusterLine.String() + `<form id="rename-person" class="person-name-form">` + dontTrackButton + `<input name="name" value="` + template.HTMLEscapeString(personName) + `" required><button>Rename</button>` + recommendButton + `</form></div></div>`)
	body.WriteString(`<form method="get" class="person-page-controls"><label>Filter <select name="state"><option value="">All faces</option>`)
	for _, state := range []string{"provisional", "assigned-user", "assigned-system", "unknown", "ambiguous", "unassigned"} {
		body.WriteString(`<option value="` + state + `"` + selectedAttr(state, faceFilter) + `>` + state + `</option>`)
	}
	body.WriteString(`</select></label> <label>Reference <select name="reference"><option value="">All</option><option value="recommended"` + selectedAttr("recommended", referenceFilter) + `>Recommended</option><option value="reference"` + selectedAttr("reference", referenceFilter) + `>Reference</option><option value="reference-or-recommended"` + selectedAttr("reference-or-recommended", referenceFilter) + `>Ref / Rec Refs</option><option value="non-reference"` + selectedAttr("non-reference", referenceFilter) + `>Non-reference</option></select></label> <label>Cluster <select name="cluster"><option value="">All</option>`)
	clusterOptions := make(map[string]string)
	for _, face := range faces {
		if face.Cluster != nil {
			clusterOptions[face.Cluster.ID] = idSuffix(face.Cluster.ID) + ": " + face.Cluster.PersonName
		}
	}
	clusterIDs := make([]string, 0, len(clusterOptions))
	for clusterID := range clusterOptions {
		clusterIDs = append(clusterIDs, clusterID)
	}
	sort.Strings(clusterIDs)
	for _, clusterID := range clusterIDs {
		body.WriteString(`<option value="` + template.HTMLEscapeString(clusterID) + `"` + selectedAttr(clusterID, clusterFilter) + `>` + template.HTMLEscapeString(clusterOptions[clusterID]) + `</option>`)
	}
	body.WriteString(`</select></label> <label>Possible <select name="possible"><option value="without"` + selectedAttr("without", possibleFilter) + `>With Out</option><option value="possibles"` + selectedAttr("possibles", possibleFilter) + `>Possibles</option><option value="all"` + selectedAttr("all", possibleFilter) + `>All</option></select></label> <label>Candidate <input name="candidate_person" value="` + template.HTMLEscapeString(candidatePersonFilter) + `" placeholder="name or ID"></label> <label>Evidence <select name="evidence"><option value="">All</option>`)
	for _, evidence := range []string{"high", "medium", "low", "manual-review"} {
		body.WriteString(`<option value="` + evidence + `"` + selectedAttr(evidence, evidenceFilter) + `>` + evidence + `</option>`)
	}
	body.WriteString(`</select></label> <label>Sort <select name="sort">`)
	for _, option := range []struct{ Value, Label string }{
		{Value: "path", Label: "Path"},
		{Value: "collection", Label: "Collection"},
		{Value: "cluster", Label: "Cluster"},
		{Value: "consistency", Label: "Consistency"},
		{Value: "confidence", Label: "Confidence"},
		{Value: "quality", Label: "Quality"},
		{Value: "detection", Label: "Detection"},
		{Value: "ref_recs", Label: "Ref Recs"},
	} {
		body.WriteString(`<option value="` + option.Value + `"` + selectedAttr(option.Value, faceSort) + `>` + option.Label + `</option>`)
	}
	body.WriteString(`</select></label> <select name="direction"><option value="asc"` + selectedAttr("asc", r.URL.Query().Get("direction")) + `>Ascending</option><option value="desc"` + selectedAttr("desc", r.URL.Query().Get("direction")) + `>Descending</option></select> <button>Apply</button></form>`)
	body.WriteString(`</div>`)
	body.WriteString(`<div id="photos" class="grid">`)
	for _, face := range faces {
		boxClass := "box"
		if face.Reference {
			boxClass += " reference"
		}
		cardClass := "card"
		if face.State == string(review.AssignedUser) || face.State == string(review.AssignedSystem) {
			cardClass += " assigned"
		} else if face.State == string(review.Provisional) {
			cardClass += " provisional"
		} else if face.State != string(review.AssignedUser) && face.State != string(review.AssignedSystem) {
			cardClass += " needs-attention"
		}
		body.WriteString(`<article class="` + cardClass + `" id="face-` + template.HTMLEscapeString(idSuffix(face.FaceID)) + `" data-face-id="` + template.HTMLEscapeString(face.FaceID) + `" data-face-state="` + template.HTMLEscapeString(face.State) + `"><div class="viewer"><img loading="lazy" src="/api/photos/` + template.HTMLEscapeString(face.PathID) + `" alt="` + template.HTMLEscapeString(idSuffix(face.FaceID)) + `"><span class="` + boxClass + `" style="left:` + percent(face.Left, face.Width) + `%;top:` + percent(face.Top, face.Height) + `%;width:` + percent(face.Right-face.Left, face.Width) + `%;height:` + percent(face.Bottom-face.Top, face.Height) + `%"></span></div><div class="path"><a href="/review/photos?path_id=` + template.HTMLEscapeString(face.PathID) + `">` + template.HTMLEscapeString(face.RelativePath) + `</a></div>`)
		confidence := formatScoreUnavailable(face.Confidence, face.ConfidenceAvailable)
		body.WriteString(`<div class="person-metadata"><strong>` + template.HTMLEscapeString(assignmentLabel(face.State)) + `:</strong> <strong>C:</strong> ` + confidence + ` Q: ` + formatScore(face.Quality) + `% D: ` + formatScore(face.Detection) + `%`)
		if face.Recommended {
			body.WriteString(`<span class="ref-rec"><strong>Ref Rec #` + fmt.Sprint(face.RecommendationPriority) + `</strong></span>`)
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
		if len(face.PossibleCandidates) > 0 {
			body.WriteString(`<div class="person-candidates possible-candidates"><strong>Possible matches:</strong> `)
			for index, candidate := range face.PossibleCandidates {
				if index > 0 {
					body.WriteString(`, `)
				}
				body.WriteString(`<a href="/review/people/` + template.HTMLEscapeString(candidate.PersonID) + `">` + template.HTMLEscapeString(candidate.PersonName) + `</a> (S: ` + formatScore(candidate.Similarity) + `%, M: ` + formatScore(candidate.Margin) + `%, Support: ` + fmt.Sprint(candidate.Support) + `, Q: ` + formatScore(candidate.Quality) + `%, D: ` + formatScore(candidate.Detection) + `%, ` + template.HTMLEscapeString(candidate.Evidence) + `, possible/current, generated: ` + template.HTMLEscapeString(candidate.CreatedAt) + `, pipeline: ` + template.HTMLEscapeString(candidate.PipelineID) + `, <a href="/review/candidate-runs/` + template.HTMLEscapeString(candidate.RunID) + `">run</a>)`)
				if canModify {
					body.WriteString(` <button type="button" data-action="assign-candidate" data-face-id="` + template.HTMLEscapeString(face.FaceID) + `" data-person-id="` + template.HTMLEscapeString(candidate.PersonID) + `" data-candidate-id="` + template.HTMLEscapeString(candidate.ID) + `">Assign</button>`)
				}
			}
			body.WriteString(`</div>`)
		}
		if face.Cluster != nil {
			clusterPerson := ""
			if face.Cluster.PersonCount > 1 {
				clusterPerson = template.HTMLEscapeString(face.Cluster.PersonName) + `:`
			}
			if face.Cluster.PersonID != "" && face.Cluster.PersonID != personID {
				clusterPerson = `<a href="/review/people/` + template.HTMLEscapeString(face.Cluster.PersonID) + `">` + template.HTMLEscapeString(face.Cluster.PersonName) + `</a>:`
			}
			body.WriteString(`<div class="person-metadata"><strong>Cluster:</strong> <a href="/review/clusters/` + template.HTMLEscapeString(face.Cluster.ID) + `">` + template.HTMLEscapeString(idSuffix(face.Cluster.ID)) + `</a>: ` + clusterPerson + ` <strong>Cs:</strong> ` + formatScoreUnavailable(face.Cluster.Consistency, face.Cluster.ConsistencyAvailable) + ` <strong>Cn:</strong> ` + formatScoreUnavailable(face.Cluster.Connectivity, face.Cluster.ConnectivityAvailable) + ` <strong>Q:</strong> ` + formatScore(face.Cluster.Quality) + `% <strong>D:</strong> ` + formatScore(face.Cluster.Detection) + `%</div>`)
		}
		if canModify {
			body.WriteString(`<div class="actions"><div class="person-dispositions"><button type="button" data-action="unknown" data-face-id="` + template.HTMLEscapeString(face.FaceID) + `">Unknown</button><button type="button" data-action="ambiguous" data-face-id="` + template.HTMLEscapeString(face.FaceID) + `">Ambiguous</button>`)
			body.WriteString(`</div>`)
			if !face.Representative && (!hasTrustedFaces || face.State == string(review.AssignedUser) || face.State == string(review.AssignedSystem)) {
				body.WriteString(`<button type="button" data-action="representative" data-face-id="` + template.HTMLEscapeString(face.FaceID) + `">Key Photo</button>`)
			}
			if face.Reference || face.State == string(review.AssignedSystem) || face.State == string(review.AssignedUser) {
				label := "Reference"
				if face.Reference {
					label = "Evaluation"
				}
				if face.Recommended && !face.Reference {
					label = "<strong>Reference</strong>"
				}
				body.WriteString(`<button type="button" data-action="role" data-face-id="` + template.HTMLEscapeString(face.FaceID) + `" data-reference="` + fmt.Sprint(face.Reference) + `">` + label + `</button>`)
			}
			body.WriteString(`</div>`)
			selectedName := ""
			for _, option := range people {
				if option.ID == face.PersonID {
					selectedName = option.Name
					break
				}
			}
			body.WriteString(`<div class="person-assignment"><input class="person-select-filter" data-person-filter="assignment-options-` + template.HTMLEscapeString(face.FaceID) + `" value="` + template.HTMLEscapeString(selectedName) + `" placeholder="Type to filter people"><select class="person-select" id="assignment-options-` + template.HTMLEscapeString(face.FaceID) + `" data-assignment-person="` + template.HTMLEscapeString(face.FaceID) + `"><option value="">Unknown</option><option value="__ambiguous__">Ambiguous</option>`)
			for _, option := range people {
				selected := ""
				if option.ID == face.PersonID {
					selected = ` selected`
				}
				body.WriteString(`<option value="` + template.HTMLEscapeString(option.ID) + `" data-person-name="` + template.HTMLEscapeString(option.Name) + `"` + selected + `>` + template.HTMLEscapeString(option.Name) + `</option>`)
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
		body.WriteString(`<div class="person-page-actions"><div><label>Assignment <input type="checkbox" id="page-bulk-confirm" required> Bulk Assignment <select id="page-assignment-state"><option value="__all_non_assigned__">All non-assigned</option>`)
		for _, state := range []string{"provisional", "assigned-system", "assigned-user", "unassigned", "unknown", "ambiguous"} {
			if state == "assigned-system" || visibleStates[state] {
				body.WriteString(`<option value="` + state + `">` + state + `</option>`)
			}
		}
		body.WriteString(`</select></label> <button type="button" data-action="page-unknown">Unknown</button><button type="button" data-action="page-ambiguous">Ambiguous</button></div><div><input class="person-select-filter" data-person-filter="page-assignment-person" value="` + template.HTMLEscapeString(personName) + `" placeholder="Type to filter people"><select class="person-select" id="page-assignment-person" data-assignment-person="page-assignment-person"><option value="` + template.HTMLEscapeString(personID) + `" selected>` + template.HTMLEscapeString(personName) + `</option>`)
		for _, option := range people {
			if option.ID == personID {
				continue
			}
			body.WriteString(`<option value="` + template.HTMLEscapeString(option.ID) + `" data-person-name="` + template.HTMLEscapeString(option.Name) + `">` + template.HTMLEscapeString(option.Name) + `</option>`)
		}
		body.WriteString(`</select><button type="button" data-action="page-assign">Save</button></div></div>`)
	}
	body.WriteString(`<p class="person-footer-note">Photo card borders: dark green indicates an assigned-user or assigned-system face, gold indicates a provisional assignment, and red indicates a face needing attention.</p>`)
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

func (s *Server) setPersonDontTrack(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	if account.Role == auth.Viewer || !validCSRFApi(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	allowed, err := s.personPermission(r, account, r.PathValue("personID"), true)
	if err != nil || !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var input struct {
		DontTrack bool `json:"dont_track"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid don't-track request", http.StatusBadRequest)
		return
	}
	if err := review.NewStore(s.db).SetPersonDontTrack(r.Context(), r.PathValue("personID"), input.DontTrack, review.Actor{Type: "user", AccountID: account.ID}, "web-review"); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"person_id": r.PathValue("personID"), "dont_track": input.DontTrack})
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
	job, queued, err := jobs.NewStore(s.db).EnqueueReferenceReprocess(r.Context(), personID)
	if err != nil {
		http.Error(w, "queue reference recommendations: "+err.Error(), http.StatusInternalServerError)
		return
	}
	auditDetails := map[string]any{"queued": queued}
	if queued {
		auditDetails["job_id"] = job.ID
	}
	if err := audit.NewStore(s.db).Record(r.Context(), audit.Event{AccountID: account.ID, Action: "reference-reprocess-request", EntityType: "person", EntityID: personID, New: auditDetails}); err != nil {
		http.Error(w, "record reference recommendation audit event: "+err.Error(), http.StatusInternalServerError)
		return
	}
	result := map[string]any{"person_id": personID, "queued": queued}
	if queued {
		result["job_id"] = job.ID
		result["status"] = job.Status
	} else {
		result["status"] = "skipped-no-approved-references"
	}
	writeJSON(w, http.StatusAccepted, result)
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
  function personID(select) { return select ? select.value : ''; }
  document.querySelectorAll('[data-person-filter]').forEach((input) => {
    const select = document.getElementById(input.dataset.personFilter);
    if (!select) return;
    const filter = () => {
      const text = input.value.toLowerCase();
      Array.from(select.options).forEach((option) => {
        const label = (option.dataset.personName || option.textContent || '').toLowerCase();
        option.hidden = text !== '' && !label.includes(text);
      });
      const selected = select.options[select.selectedIndex];
      if (selected && selected.hidden) {
        const first = Array.from(select.options).find((option) => !option.hidden);
        if (first) select.value = first.value;
      }
    };
    input.addEventListener('input', filter);
    filter();
  });
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
    alert('Reference recommendation job queued for ' + page.dataset.personName + ': ' + result.job_id);
  };
  const dontTrackButton = document.querySelector('[data-action="person-dont-track"]');
  if (dontTrackButton) dontTrackButton.onclick = async () => {
    const enabled = dontTrackButton.dataset.dontTrack === 'true';
    if (!confirm((enabled ? 'Mark ' : 'Remove ') + page.dataset.personName + (enabled ? ' as don\'t track?' : ' from don\'t track?'))) return;
    const response = await fetch('/api/people/' + encodeURIComponent(page.dataset.personId) + '/dont-track', {
      method: 'POST', headers: {'Content-Type': 'application/json', 'X-CSRF-Token': token},
      body: JSON.stringify({dont_track: enabled})
    });
    if (!response.ok) { alert(await response.text()); return; }
    location.reload();
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
      const person = personID(document.querySelector('[data-assignment-person="' + CSS.escape(faceID) + '"]'));
      const payload = {person_id: person, state: 'assigned-user'};
      if (person === '__ambiguous__') { payload.person_id = ''; payload.state = 'ambiguous'; }
      if (!person) { payload.state = 'unknown'; }
      send(faceID, '/api/faces/' + encodeURIComponent(faceID) + '/assignment', payload);
    };
  });
  document.querySelectorAll('[data-action="assign-candidate"]').forEach((button) => {
    button.onclick = () => send(button.dataset.faceId,
      '/api/faces/' + encodeURIComponent(button.dataset.faceId) + '/assignment',
      {person_id: button.dataset.personId, state: 'assigned-user', candidate_id: button.dataset.candidateId});
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
    const acknowledgement = document.getElementById('page-bulk-confirm');
    if (!acknowledgement || !acknowledgement.checked) {
      alert('Check "Apply to all matching faces" before using a bulk action.');
      return;
    }
    const faceIDs = bulkFaces();
    if (!faceIDs.length) { alert('No visible faces match the selected assignment.'); return; }
    const action = state === 'assigned-user' ? 'assign' : state;
    if (!confirm('Bulk ' + action + ' will affect ' + faceIDs.length + ' face' + (faceIDs.length === 1 ? '' : 's') + '. Continue?')) return;
    const response = await fetch('/api/faces/assignments', {
      method: 'POST',
      headers: {'Content-Type': 'application/json', 'X-CSRF-Token': token},
      body: JSON.stringify({face_ids: faceIDs, person_id: personID || '', state: state})
    });
    if (!response.ok) { alert(await response.text()); return; }
    location.reload();
  }
  document.querySelectorAll('[data-action="page-unknown"], [data-action="page-ambiguous"]').forEach((button) => {
    button.onclick = () => sendBulk(button.dataset.action === 'page-unknown' ? 'unknown' : 'ambiguous', '');
  });
  document.querySelectorAll('[data-action="page-assign"]').forEach((button) => {
    button.onclick = () => {
      const person = personID(document.getElementById('page-assignment-person'));
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
  document.querySelectorAll('[data-action="assign-candidate"]').forEach((button) => {
    button.onclick = () => send(button.dataset.faceId, '/api/faces/' + encodeURIComponent(button.dataset.faceId) + '/assignment', {person_id:button.dataset.personId, state:'assigned-user', candidate_id:button.dataset.candidateId});
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

func (s *Server) adminScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	_, _ = w.Write([]byte(`(function () {
  document.querySelectorAll('form[data-admin-json]').forEach(function (form) {
    form.addEventListener('submit', async function (event) {
      event.preventDefault();
      const values = new FormData(form);
      const payload = {};
      values.forEach(function (value, key) {
        if (key !== 'csrf') payload[key] = value;
      });
      ['must_change', 'view', 'modify', 'undo', 'disabled'].forEach(function (key) {
        if (key in payload) payload[key] = payload[key] === 'on' || payload[key] === 'true';
      });
      if ('dimension' in payload) payload.dimension = Number(payload.dimension);
      const response = await fetch(form.action, {
        method: 'POST',
        headers: {'Content-Type': 'application/json', 'X-CSRF-Token': values.get('csrf') || ''},
        body: JSON.stringify(payload)
      });
      if (!response.ok) { alert(await response.text()); return; }
      location.reload();
    });
  });
})();
`))
}

func (s *Server) reviewClustersLegacy(w http.ResponseWriter, r *http.Request) {
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
	clusterQuery := `SELECT c.id, c.cluster_key, c.purity, c.connectivity, c.is_mixed, c.review_status, COUNT(DISTINCT cm.face_id), COUNT(DISTINCT rc.person_id), COALESCE(c.proposed_person_id, ''), COALESCE(NULLIF(p.name, ''), substr(c.proposed_person_id, -8), '') FROM clusters c JOIN clustering_runs cr ON cr.id = c.clustering_run_id LEFT JOIN cluster_memberships cm ON cm.cluster_id = c.id LEFT JOIN recognition_candidates rc ON rc.face_id = cm.face_id AND rc.created_at = (SELECT MAX(rc2.created_at) FROM recognition_candidates rc2 WHERE rc2.face_id = rc.face_id) LEFT JOIN people p ON p.id = c.proposed_person_id WHERE cr.status = 'active'`
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
		clusterQuery += ` AND EXISTS (SELECT 1 FROM cluster_memberships cm2 JOIN faces f2 ON f2.id = cm2.face_id JOIN analyses a2 ON a2.id = f2.analysis_id JOIN photo_assets pa2 ON pa2.id = a2.asset_id JOIN photo_paths pp2 ON pp2.asset_id = pa2.id WHERE cm2.cluster_id = c.id AND pp2.collection_id IN (` + strings.Join(placeholders, ",") + `))`
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
		candidateRows, candidateErr := s.db.QueryContext(r.Context(), `SELECT rc.person_id, COALESCE(NULLIF(p.name, ''), substr(rc.person_id, -8), rc.person_id), rc.similarity, rc.margin, rc.support_count FROM recognition_candidates rc LEFT JOIN people p ON p.id = rc.person_id WHERE rc.face_id = ? AND rc.created_at = (SELECT MAX(created_at) FROM recognition_candidates WHERE face_id = ?) ORDER BY rc.rank LIMIT 5`, faceID, faceID)
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
		PersonID    string                 `json:"person_id"`
		State       review.AssignmentState `json:"state"`
		Reject      bool                   `json:"reject"`
		CandidateID string                 `json:"candidate_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid assignment", http.StatusBadRequest)
		return
	}
	candidateRun := ""
	if input.CandidateID != "" && !input.Reject {
		var candidateFace, candidatePerson string
		var current int
		if err := s.db.QueryRowContext(r.Context(), `SELECT face_id, person_id, run_id, is_current FROM possible_face_candidates WHERE id = ?`, input.CandidateID).Scan(&candidateFace, &candidatePerson, &candidateRun, &current); err != nil || candidateFace != faceID || candidatePerson != input.PersonID || current != 1 {
			http.Error(w, "invalid candidate", http.StatusBadRequest)
			return
		}
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
	if input.CandidateID != "" && !input.Reject {
		if err := audit.NewStore(s.db).Record(r.Context(), audit.Event{AccountID: account.ID, TargetEventID: assignment.EventID, Action: "possible-candidate-assignment", EntityType: "face", EntityID: faceID, New: map[string]any{"candidate_id": input.CandidateID, "candidate_run_id": candidateRun, "person_id": input.PersonID}}); err != nil {
			http.Error(w, "audit failed", http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, http.StatusOK, assignment)
}

func (s *Server) assignFaces(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	if !validCSRFApi(r) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return
	}
	var input struct {
		FaceIDs  []string               `json:"face_ids"`
		PersonID string                 `json:"person_id"`
		State    review.AssignmentState `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid assignment", http.StatusBadRequest)
		return
	}
	if len(input.FaceIDs) == 0 {
		http.Error(w, "at least one face is required", http.StatusBadRequest)
		return
	}
	for _, faceID := range input.FaceIDs {
		allowed, err := s.facePermission(r, account, faceID, true)
		if err != nil || !allowed {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}
	assignments, err := review.NewStore(s.db).AssignMany(r.Context(), input.FaceIDs, input.PersonID, input.State, review.Actor{Type: "user", AccountID: account.ID}, review.Evidence{}, "web-review")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"assignments": assignments})
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

func (s *Server) requireSystemAdmin(w http.ResponseWriter, r *http.Request) (auth.Account, bool) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return auth.Account{}, false
	}
	if account.Role != auth.SystemAdmin {
		http.Error(w, "forbidden", http.StatusForbidden)
		return auth.Account{}, false
	}
	return account, true
}

func adminNav() string {
	return `<p><a href="/admin">Admin dashboard</a> · <a href="/admin/jobs">Jobs</a> · <a href="/admin/runs">Runs</a> · <a href="/admin/scans">Scans</a> · <a href="/admin/audit">Audit</a> · <a href="/admin/pipelines">Pipelines</a> · <a href="/admin/accounts">Accounts</a> · <a href="/admin/collections">Collections</a> · <a href="/admin/status">Status</a> · <a href="/admin/setup-check">Setup check</a></p>`
}

func adminPageParams(r *http.Request, defaultSort string, sorts map[string]string) (string, int, int, string) {
	query := r.URL.Query()
	search := strings.TrimSpace(query.Get("q"))
	sortName := query.Get("sort")
	sortSQL, ok := sorts[sortName]
	if !ok {
		sortName = defaultSort
		sortSQL = sorts[defaultSort]
	}
	order := adminOrderValue(r, defaultSort)
	limit := 20
	if value, err := strconv.Atoi(query.Get("limit")); err == nil && value > 0 && value <= 100 {
		limit = value
	}
	page := 1
	if value, err := strconv.Atoi(query.Get("page")); err == nil && value > 0 {
		page = value
	}
	return search, limit, page, sortSQL + " " + order
}

func adminOrderValue(r *http.Request, defaultSort string) string {
	switch strings.ToLower(r.URL.Query().Get("order")) {
	case "asc":
		return "ASC"
	case "desc":
		return "DESC"
	default:
		if defaultSort == "username" {
			return "ASC"
		}
		return "DESC"
	}
}

func adminOrderOptions(r *http.Request, defaultSort string) string {
	selected := strings.ToLower(adminOrderValue(r, defaultSort))
	return `<option value="asc"` + selectedAttr("asc", selected) + `>Ascending</option><option value="desc"` + selectedAttr("desc", selected) + `>Descending</option>`
}

func adminPager(r *http.Request, search, sortSQL string, limit, page, total int) string {
	pages := (total + limit - 1) / limit
	if pages < 1 {
		pages = 1
	}
	link := func(target int) string {
		values := r.URL.Query()
		if search == "" {
			values.Del("q")
		}
		values.Set("limit", strconv.Itoa(limit))
		values.Set("page", strconv.Itoa(target))
		return "?" + values.Encode()
	}
	return fmt.Sprintf(`<p>Showing page %d of %d (%d items) · <a href="%s">Previous</a> · <a href="%s">Next</a></p>`, page, pages, total, link(maxInt(1, page-1)), link(minInt(pages, page+1)))
}

func adminDistinctStrings(ctx context.Context, database *sql.DB, query string) ([]string, error) {
	rows, err := database.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func adminSelectOptions(values []string, selected string) string {
	var result strings.Builder
	for _, value := range values {
		selectedAttr := ""
		if value == selected {
			selectedAttr = ` selected`
		}
		result.WriteString(`<option value="` + template.HTMLEscapeString(value) + `"` + selectedAttr + `>` + template.HTMLEscapeString(value) + `</option>`)
	}
	return result.String()
}

func adminIDLink(prefix, id string) string {
	label := id
	if len(id) > 8 {
		label = id[len(id)-8:]
	}
	return `<a href="` + prefix + template.HTMLEscapeString(url.PathEscape(id)) + `">` + template.HTMLEscapeString(label) + `</a>`
}

func (s *Server) adminDashboard(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireSystemAdmin(w, r)
	if !ok {
		return
	}
	body := `<h1>Administration</h1>` + adminNav() + `<ul><li>Jobs, runs, scans, audit events, pipelines, accounts, and collections can be browsed from the links above.</li><li><a href="/admin/status">Status</a> shows the application status snapshot.</li><li><a href="/admin/setup-check">Setup check</a> shows the worker environment checks.</li></ul>`
	s.render(w, map[string]any{"Title": "Administration", "Account": account, "Body": template.HTML(body)})
}

func (s *Server) adminStatus(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireSystemAdmin(w, r)
	if !ok {
		return
	}
	body := `<h1>Status</h1>` + adminNav() + `<pre>` + template.HTMLEscapeString(string(s.statusJSON)) + `</pre>`
	s.render(w, map[string]any{"Title": "Status", "Account": account, "Body": template.HTML(body)})
}

func (s *Server) adminSetupCheck(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireSystemAdmin(w, r)
	if !ok {
		return
	}
	body := `<h1>Setup check</h1>` + adminNav() + `<pre>` + template.HTMLEscapeString(string(s.setupJSON)) + `</pre>`
	s.render(w, map[string]any{"Title": "Setup check", "Account": account, "Body": template.HTML(body)})
}

func (s *Server) adminJobs(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireSystemAdmin(w, r)
	if !ok {
		return
	}
	search, limit, page, order := adminPageParams(r, "created", map[string]string{"created": "created_at", "kind": "kind", "status": "status"})
	query := r.URL.Query()
	kindFilter, statusFilter, priorityFilter := query.Get("kind_filter"), query.Get("status_filter"), query.Get("priority_filter")
	whereParts := []string{"(kind LIKE ? OR status LIKE ? OR id LIKE ?)"}
	whereArgs := []any{}
	pattern := "%" + search + "%"
	whereArgs = append(whereArgs, pattern, pattern, pattern)
	if kindFilter != "" {
		whereParts = append(whereParts, "kind = ?")
		whereArgs = append(whereArgs, kindFilter)
	}
	if statusFilter != "" {
		whereParts = append(whereParts, "status = ?")
		whereArgs = append(whereArgs, statusFilter)
	}
	if priorityFilter != "" {
		whereParts = append(whereParts, "priority = ?")
		whereArgs = append(whereArgs, priorityFilter)
	}
	where := "WHERE " + strings.Join(whereParts, " AND ")
	var total int
	if err := s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM jobs "+where, whereArgs...).Scan(&total); err != nil {
		http.Error(w, "job lookup failed", 500)
		return
	}
	offset := (page - 1) * limit
	rowsArgs := append(append([]any{}, whereArgs...), limit, offset)
	rows, err := s.db.QueryContext(r.Context(), "SELECT id, kind, status, priority, created_at, COALESCE(started_at,''), COALESCE(completed_at,'') FROM jobs "+where+" ORDER BY "+order+", id LIMIT ? OFFSET ?", rowsArgs...)
	if err != nil {
		http.Error(w, "job lookup failed", 500)
		return
	}
	defer rows.Close()
	kinds, err := adminDistinctStrings(r.Context(), s.db, `SELECT DISTINCT kind FROM jobs ORDER BY kind`)
	if err != nil {
		http.Error(w, "job filter lookup failed", 500)
		return
	}
	statuses, err := adminDistinctStrings(r.Context(), s.db, `SELECT DISTINCT status FROM jobs ORDER BY status`)
	if err != nil {
		http.Error(w, "job filter lookup failed", 500)
		return
	}
	priorities, err := adminDistinctStrings(r.Context(), s.db, `SELECT DISTINCT CAST(priority AS TEXT) FROM jobs ORDER BY priority`)
	if err != nil {
		http.Error(w, "job filter lookup failed", 500)
		return
	}
	body := `<h1>Jobs</h1>` + adminNav() + `<form><input name="q" value="` + template.HTMLEscapeString(search) + `" placeholder="Filter"><label>Kind <select name="kind_filter"><option value="">All</option>` + adminSelectOptions(kinds, kindFilter) + `</select></label><label>Status <select name="status_filter"><option value="">All</option>` + adminSelectOptions(statuses, statusFilter) + `</select></label><label>Priority <select name="priority_filter"><option value="">All</option>` + adminSelectOptions(priorities, priorityFilter) + `</select></label><select name="sort"><option value="created">Created</option><option value="kind">Kind</option><option value="status">Status</option></select><select name="order">` + adminOrderOptions(r, "created") + `</select><select name="limit"><option>20</option><option>50</option><option>100</option></select><button>Filter</button></form>` + adminPager(r, search, order, limit, page, total) + `<table><tr><th>ID</th><th>Kind</th><th>Status</th><th>Priority</th><th>Created</th><th>Started</th><th>Completed</th></tr>`
	for rows.Next() {
		var id, kind, status, created, started, completed string
		var priority int
		if err := rows.Scan(&id, &kind, &status, &priority, &created, &started, &completed); err != nil {
			continue
		}
		body += `<tr><td>` + adminIDLink("/admin/jobs/", id) + `</td><td>` + template.HTMLEscapeString(kind) + `</td><td>` + template.HTMLEscapeString(status) + `</td><td>` + fmt.Sprint(priority) + `</td><td>` + template.HTMLEscapeString(created) + `</td><td>` + template.HTMLEscapeString(started) + `</td><td>` + template.HTMLEscapeString(completed) + `</td></tr>`
	}
	body += `</table>`
	s.render(w, map[string]any{"Title": "Jobs", "Account": account, "Body": template.HTML(body)})
}

func (s *Server) adminRuns(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireSystemAdmin(w, r)
	if !ok {
		return
	}
	search, limit, page, order := adminPageParams(r, "created", map[string]string{"created": "created_at", "status": "status", "algorithm": "algorithm"})
	where := "WHERE algorithm LIKE ? OR status LIKE ? OR id LIKE ?"
	pattern := "%" + search + "%"
	var total int
	if err := s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM clustering_runs "+where, pattern, pattern, pattern).Scan(&total); err != nil {
		http.Error(w, "run lookup failed", 500)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), "SELECT id,status,algorithm,algorithm_version,k,similarity_threshold,created_at,COALESCE(completed_at,'') FROM clustering_runs "+where+" ORDER BY "+order+", id LIMIT ? OFFSET ?", pattern, pattern, pattern, limit, (page-1)*limit)
	if err != nil {
		http.Error(w, "run lookup failed", 500)
		return
	}
	defer rows.Close()
	body := `<h1>Clustering runs</h1>` + adminNav() + `<form><input name="q" value="` + template.HTMLEscapeString(search) + `" placeholder="Filter"><select name="sort"><option value="created">Created</option><option value="status">Status</option><option value="algorithm">Algorithm</option></select><select name="order">` + adminOrderOptions(r, "created") + `</select><select name="limit"><option>20</option><option>50</option><option>100</option></select><button>Filter</button></form>` + adminPager(r, search, order, limit, page, total) + `<table><tr><th>ID</th><th>Status</th><th>Algorithm</th><th>Version</th><th>k</th><th>Similarity</th><th>Created</th><th>Completed</th></tr>`
	for rows.Next() {
		var id, status, algorithm, version, created, completed string
		var k int
		var similarity float64
		if err := rows.Scan(&id, &status, &algorithm, &version, &k, &similarity, &created, &completed); err != nil {
			continue
		}
		body += `<tr><td>` + adminIDLink("/admin/runs/", id) + `</td><td>` + template.HTMLEscapeString(status) + `</td><td>` + template.HTMLEscapeString(algorithm) + `</td><td>` + template.HTMLEscapeString(version) + `</td><td>` + fmt.Sprint(k) + `</td><td>` + fmt.Sprintf("%.3f", similarity) + `</td><td>` + template.HTMLEscapeString(created) + `</td><td>` + template.HTMLEscapeString(completed) + `</td></tr>`
	}
	body += `</table>`
	s.render(w, map[string]any{"Title": "Clustering runs", "Account": account, "Body": template.HTML(body)})
}

func (s *Server) adminScans(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireSystemAdmin(w, r)
	if !ok {
		return
	}
	search, limit, page, order := adminPageParams(r, "created", map[string]string{"created": "sr.created_at", "status": "sr.status", "collection": "c.name"})
	pattern := "%" + search + "%"
	where := "WHERE sr.status LIKE ? OR c.name LIKE ? OR sr.id LIKE ?"
	var total int
	if err := s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM scan_runs sr JOIN collections c ON c.id=sr.collection_id "+where, pattern, pattern, pattern).Scan(&total); err != nil {
		http.Error(w, "scan lookup failed", 500)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), "SELECT sr.id,c.name,sr.status,sr.created_at,COALESCE(sr.started_at,''),COALESCE(sr.completed_at,''), (SELECT COUNT(*) FROM scan_items si WHERE si.scan_run_id=sr.id) FROM scan_runs sr JOIN collections c ON c.id=sr.collection_id "+where+" ORDER BY "+order+", sr.id LIMIT ? OFFSET ?", pattern, pattern, pattern, limit, (page-1)*limit)
	if err != nil {
		http.Error(w, "scan lookup failed", 500)
		return
	}
	defer rows.Close()
	body := `<h1>Scans</h1>` + adminNav() + `<form><input name="q" value="` + template.HTMLEscapeString(search) + `" placeholder="Filter"><select name="sort"><option value="created">Created</option><option value="status">Status</option><option value="collection">Collection</option></select><select name="order">` + adminOrderOptions(r, "created") + `</select><select name="limit"><option>20</option><option>50</option><option>100</option></select><button>Filter</button></form>` + adminPager(r, search, order, limit, page, total) + `<table><tr><th>ID</th><th>Collection</th><th>Status</th><th>Created</th><th>Started</th><th>Completed</th><th>Items</th></tr>`
	for rows.Next() {
		var id, name, status, created, started, completed string
		var items int
		if err := rows.Scan(&id, &name, &status, &created, &started, &completed, &items); err != nil {
			continue
		}
		body += `<tr><td>` + adminIDLink("/admin/scans/", id) + `</td><td>` + template.HTMLEscapeString(name) + `</td><td>` + template.HTMLEscapeString(status) + `</td><td>` + template.HTMLEscapeString(created) + `</td><td>` + template.HTMLEscapeString(started) + `</td><td>` + template.HTMLEscapeString(completed) + `</td><td>` + fmt.Sprint(items) + `</td></tr>`
	}
	body += `</table>`
	s.render(w, map[string]any{"Title": "Scans", "Account": account, "Body": template.HTML(body)})
}

func (s *Server) adminAudit(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireSystemAdmin(w, r)
	if !ok {
		return
	}
	search, limit, page, order := adminPageParams(r, "created", map[string]string{"created": "created_at", "action": "action", "entity": "entity_type"})
	query := r.URL.Query()
	actionFilter, entityFilter, accountFilter := query.Get("action_filter"), query.Get("entity_filter"), query.Get("account_filter")
	pattern := "%" + search + "%"
	whereParts := []string{"(action LIKE ? OR entity_type LIKE ? OR entity_id LIKE ? OR id LIKE ?)"}
	whereArgs := []any{pattern, pattern, pattern, pattern}
	if actionFilter != "" {
		whereParts = append(whereParts, "action = ?")
		whereArgs = append(whereArgs, actionFilter)
	}
	if entityFilter != "" {
		whereParts = append(whereParts, "entity_type = ?")
		whereArgs = append(whereArgs, entityFilter)
	}
	if accountFilter != "" {
		whereParts = append(whereParts, "COALESCE(account_id, '') = ?")
		whereArgs = append(whereArgs, accountFilter)
	}
	where := "WHERE " + strings.Join(whereParts, " AND ")
	var total int
	if err := s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM audit_events "+where, whereArgs...).Scan(&total); err != nil {
		http.Error(w, "audit lookup failed", 500)
		return
	}
	rowsArgs := append(append([]any{}, whereArgs...), limit, (page-1)*limit)
	rows, err := s.db.QueryContext(r.Context(), "SELECT id,action,entity_type,entity_id,COALESCE(account_id,''),created_at FROM audit_events "+where+" ORDER BY "+order+", id LIMIT ? OFFSET ?", rowsArgs...)
	if err != nil {
		http.Error(w, "audit lookup failed", 500)
		return
	}
	defer rows.Close()
	actions, err := adminDistinctStrings(r.Context(), s.db, `SELECT DISTINCT action FROM audit_events ORDER BY action`)
	if err != nil {
		http.Error(w, "audit filter lookup failed", 500)
		return
	}
	entities, err := adminDistinctStrings(r.Context(), s.db, `SELECT DISTINCT entity_type FROM audit_events ORDER BY entity_type`)
	if err != nil {
		http.Error(w, "audit filter lookup failed", 500)
		return
	}
	accounts, err := adminDistinctStrings(r.Context(), s.db, `SELECT DISTINCT COALESCE(account_id, '') FROM audit_events ORDER BY account_id`)
	if err != nil {
		http.Error(w, "audit filter lookup failed", 500)
		return
	}
	body := `<h1>Audit events</h1>` + adminNav() + `<form><input name="q" value="` + template.HTMLEscapeString(search) + `" placeholder="Filter"><label>Action <select name="action_filter"><option value="">All</option>` + adminSelectOptions(actions, actionFilter) + `</select></label><label>Entity <select name="entity_filter"><option value="">All</option>` + adminSelectOptions(entities, entityFilter) + `</select></label><label>Account <select name="account_filter"><option value="">All</option>` + adminSelectOptions(accounts, accountFilter) + `</select></label><select name="sort"><option value="created">Created</option><option value="action">Action</option><option value="entity">Entity</option></select><select name="order">` + adminOrderOptions(r, "created") + `</select><select name="limit"><option>20</option><option>50</option><option>100</option></select><button>Filter</button></form>` + adminPager(r, search, order, limit, page, total) + `<table><tr><th>ID</th><th>Action</th><th>Entity</th><th>Entity ID</th><th>Account</th><th>Created</th></tr>`
	for rows.Next() {
		var id, action, entity, entityID, accountID, created string
		if err := rows.Scan(&id, &action, &entity, &entityID, &accountID, &created); err != nil {
			continue
		}
		body += `<tr><td>` + adminIDLink("/admin/audit/", id) + `</td><td>` + template.HTMLEscapeString(action) + `</td><td>` + template.HTMLEscapeString(entity) + `</td><td>` + template.HTMLEscapeString(entityID) + `</td><td>` + template.HTMLEscapeString(accountID) + `</td><td>` + template.HTMLEscapeString(created) + `</td></tr>`
	}
	body += `</table>`
	s.render(w, map[string]any{"Title": "Audit events", "Account": account, "Body": template.HTML(body)})
}

func (s *Server) adminPipelines(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireSystemAdmin(w, r)
	if !ok {
		return
	}
	search, limit, page, order := adminPageParams(r, "created", map[string]string{"created": "created_at", "family": "model_family", "provider": "execution_provider"})
	pattern := "%" + search + "%"
	where := "WHERE model_family LIKE ? OR execution_provider LIKE ? OR id LIKE ?"
	var total int
	if err := s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM pipelines "+where, pattern, pattern, pattern).Scan(&total); err != nil {
		http.Error(w, "pipeline lookup failed", 500)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), "SELECT id,model_family,model_version,execution_provider,embedding_dimension,created_at FROM pipelines "+where+" ORDER BY "+order+", id LIMIT ? OFFSET ?", pattern, pattern, pattern, limit, (page-1)*limit)
	if err != nil {
		http.Error(w, "pipeline lookup failed", 500)
		return
	}
	defer rows.Close()
	body := `<h1>Pipelines</h1>` + adminNav() + `<form><input name="q" value="` + template.HTMLEscapeString(search) + `" placeholder="Filter"><select name="sort"><option value="created">Created</option><option value="family">Model</option><option value="provider">Provider</option></select><select name="order">` + adminOrderOptions(r, "created") + `</select><select name="limit"><option>20</option><option>50</option><option>100</option></select><button>Filter</button></form>` + adminPager(r, search, order, limit, page, total) + `<table><tr><th>ID</th><th>Model</th><th>Version</th><th>Provider</th><th>Embedding dimension</th><th>Created</th></tr>`
	for rows.Next() {
		var id, family, version, provider, created string
		var dimension int
		if err := rows.Scan(&id, &family, &version, &provider, &dimension, &created); err != nil {
			continue
		}
		body += `<tr><td>` + adminIDLink("/admin/pipelines/", id) + `</td><td>` + template.HTMLEscapeString(family) + `</td><td>` + template.HTMLEscapeString(version) + `</td><td>` + template.HTMLEscapeString(provider) + `</td><td>` + fmt.Sprint(dimension) + `</td><td>` + template.HTMLEscapeString(created) + `</td></tr>`
	}
	csrf := ""
	if cookie, err := r.Cookie("csrf"); err == nil {
		csrf = cookie.Value
	}
	body += `</table><h2>Add pipeline</h2><form method="post" action="/api/admin/pipelines" data-admin-json><input type="hidden" name="csrf" value="` + template.HTMLEscapeString(csrf) + `"><label>Model family <input name="model_family" required></label><label>Model version <input name="model_version" required></label><label>Provider <input name="provider" required></label><label>Embedding dimension <input type="number" name="dimension" min="1" required></label><button>Create</button></form><script src="/static/admin.js" defer></script>`
	s.render(w, map[string]any{"Title": "Pipelines", "Account": account, "CSRF": csrf, "Body": template.HTML(body)})
}

func (s *Server) adminObjectDetail(w http.ResponseWriter, r *http.Request, table, id, title, prefix string) {
	account, ok := s.requireSystemAdmin(w, r)
	if !ok {
		return
	}
	columnsRows, err := s.db.QueryContext(r.Context(), "PRAGMA table_info("+table+")")
	if err != nil {
		http.Error(w, "object lookup failed", 500)
		return
	}
	var columns []string
	for columnsRows.Next() {
		var cid int
		var name, columnType string
		var notNull, primary int
		var defaultValue sql.NullString
		if err := columnsRows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primary); err == nil {
			columns = append(columns, name)
		}
	}
	columnsRows.Close()
	if len(columns) == 0 {
		http.Error(w, "object not found", 404)
		return
	}
	values := make([]any, len(columns))
	pointers := make([]any, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	query := "SELECT " + strings.Join(columns, ",") + " FROM " + table + " WHERE id = ?"
	if err := s.db.QueryRowContext(r.Context(), query, id).Scan(pointers...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "object not found", 404)
		} else {
			http.Error(w, "object lookup failed", 500)
		}
		return
	}
	body := `<h1>` + template.HTMLEscapeString(title) + `</h1>` + adminNav() + `<table>`
	for i, name := range columns {
		value := fmt.Sprint(values[i])
		if data, ok := values[i].([]byte); ok {
			value = string(data)
		}
		body += `<tr><th>` + template.HTMLEscapeString(name) + `</th><td><pre>` + template.HTMLEscapeString(value) + `</pre></td></tr>`
	}
	body += `</table><p><a href="` + template.HTMLEscapeString(prefix) + `">Back to list</a></p>`
	s.render(w, map[string]any{"Title": title, "Account": account, "Body": template.HTML(body)})
}

func (s *Server) adminJob(w http.ResponseWriter, r *http.Request) {
	s.adminObjectDetail(w, r, "jobs", r.PathValue("jobID"), "Job", "/admin/jobs")
}
func (s *Server) adminRun(w http.ResponseWriter, r *http.Request) {
	s.adminObjectDetail(w, r, "clustering_runs", r.PathValue("runID"), "Clustering run", "/admin/runs")
}
func (s *Server) adminScan(w http.ResponseWriter, r *http.Request) {
	s.adminObjectDetail(w, r, "scan_runs", r.PathValue("scanID"), "Scan", "/admin/scans")
}
func (s *Server) adminAuditEvent(w http.ResponseWriter, r *http.Request) {
	s.adminObjectDetail(w, r, "audit_events", r.PathValue("eventID"), "Audit event", "/admin/audit")
}
func (s *Server) adminPipeline(w http.ResponseWriter, r *http.Request) {
	s.adminObjectDetail(w, r, "pipelines", r.PathValue("pipelineID"), "Pipeline", "/admin/pipelines")
}

func (s *Server) adminAccounts(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireSystemAdmin(w, r)
	if !ok {
		return
	}
	search, limit, page, order := adminPageParams(r, "username", map[string]string{"username": "username", "role": "role", "created": "created_at"})
	pattern := "%" + search + "%"
	where := "WHERE username LIKE ? OR role LIKE ? OR id LIKE ?"
	var total int
	if err := s.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM accounts "+where, pattern, pattern, pattern).Scan(&total); err != nil {
		http.Error(w, "account lookup failed", 500)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), "SELECT id, username, role, disabled_at IS NOT NULL, created_at FROM accounts "+where+" ORDER BY "+order+", id LIMIT ? OFFSET ?", pattern, pattern, pattern, limit, (page-1)*limit)
	if err != nil {
		http.Error(w, "account lookup failed", 500)
		return
	}
	defer rows.Close()
	csrf := ""
	if cookie, err := r.Cookie("csrf"); err == nil {
		csrf = cookie.Value
	}
	body := `<h1>Accounts</h1>` + adminNav() + `<form><input name="q" value="` + template.HTMLEscapeString(search) + `" placeholder="Filter"><select name="sort"><option value="username">Username</option><option value="role">Role</option><option value="created">Created</option></select><select name="order">` + adminOrderOptions(r, "username") + `</select><select name="limit"><option>20</option><option>50</option><option>100</option></select><button>Filter</button></form>` + adminPager(r, search, order, limit, page, total) + `<table><tr><th>ID</th><th>Username</th><th>Role</th><th>Disabled</th><th>Created</th></tr>`
	for rows.Next() {
		var id, username, role, created string
		var disabled int
		if err := rows.Scan(&id, &username, &role, &disabled, &created); err != nil {
			continue
		}
		body += `<tr><td>` + adminIDLink("/admin/accounts/", id) + `</td><td>` + template.HTMLEscapeString(username) + `</td><td>` + template.HTMLEscapeString(role) + `</td><td>` + fmt.Sprint(disabled == 1) + `</td><td>` + template.HTMLEscapeString(created) + `</td></tr>`
	}
	body += `</table><h2>Add account</h2><form method="post" action="/api/admin/accounts" data-admin-json><input type="hidden" name="csrf" value="` + template.HTMLEscapeString(csrf) + `"><label>Username <input name="username" required></label><label>Password <input type="password" name="password" required></label><label>Role <select name="role"><option>viewer</option><option>reviewer</option><option>collection-manager</option><option>system-admin</option></select></label><button>Create</button></form><script src="/static/admin.js" defer></script>`
	s.render(w, map[string]any{"Title": "Accounts", "Account": account, "CSRF": csrf, "Body": template.HTML(body)})
}

func (s *Server) adminAccount(w http.ResponseWriter, r *http.Request) {
	account, ok := s.requireSystemAdmin(w, r)
	if !ok {
		return
	}
	id := r.PathValue("accountID")
	var username, role, created, updated string
	var disabled, mustChange int
	if err := s.db.QueryRowContext(r.Context(), `SELECT username, role, disabled_at IS NOT NULL, must_change_password, created_at, updated_at FROM accounts WHERE id = ?`, id).Scan(&username, &role, &disabled, &mustChange, &created, &updated); err != nil {
		http.Error(w, "account not found", 404)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `SELECT c.id, c.name, COALESCE(cp.can_view, 0), COALESCE(cp.can_modify, 0), COALESCE(cp.can_undo, 0) FROM collections c LEFT JOIN collection_permissions cp ON cp.collection_id=c.id AND cp.account_id=? ORDER BY c.name`, id)
	if err != nil {
		http.Error(w, "permission lookup failed", 500)
		return
	}
	defer rows.Close()
	csrf := ""
	if cookie, err := r.Cookie("csrf"); err == nil {
		csrf = cookie.Value
	}
	body := `<h1>Account ` + template.HTMLEscapeString(username) + `</h1>` + adminNav() + `<table><tr><th>Field</th><th>Value</th></tr><tr><td>ID</td><td>` + template.HTMLEscapeString(id) + `</td></tr><tr><td>Username</td><td>` + template.HTMLEscapeString(username) + `</td></tr><tr><td>Role</td><td>` + template.HTMLEscapeString(role) + `</td></tr><tr><td>Disabled</td><td>` + fmt.Sprint(disabled == 1) + `</td></tr><tr><td>Must change password</td><td>` + fmt.Sprint(mustChange == 1) + `</td></tr><tr><td>Created</td><td>` + template.HTMLEscapeString(created) + `</td></tr><tr><td>Updated</td><td>` + template.HTMLEscapeString(updated) + `</td></tr></table><h2>Change password</h2><form method="post" action="/api/admin/accounts/` + template.HTMLEscapeString(id) + `/password" data-admin-json><input type="hidden" name="csrf" value="` + template.HTMLEscapeString(csrf) + `"><input type="password" name="password" placeholder="New password" required><label><input type="checkbox" name="must_change"> Require change at next login</label><button>Reset password</button></form><h2>Disable account</h2><form method="post" action="/api/admin/accounts/` + template.HTMLEscapeString(id) + `/disabled" data-admin-json><input type="hidden" name="csrf" value="` + template.HTMLEscapeString(csrf) + `"><input type="hidden" name="disabled" value="` + fmt.Sprint(disabled == 0) + `"><button>` + map[bool]string{true: "Disable", false: "Enable"}[disabled == 0] + `</button></form><h2>Collection permissions</h2><table><tr><th>Collection</th><th>View</th><th>Modify</th><th>Undo</th></tr>`
	for rows.Next() {
		var collectionID, name string
		var view, modify, undo int
		if err := rows.Scan(&collectionID, &name, &view, &modify, &undo); err != nil {
			continue
		}
		body += `<tr><td>` + template.HTMLEscapeString(name) + `</td><td>` + fmt.Sprint(view == 1) + `</td><td>` + fmt.Sprint(modify == 1) + `</td><td>` + fmt.Sprint(undo == 1) + `</td><td><form method="post" action="/api/admin/permissions" data-admin-json><input type="hidden" name="csrf" value="` + template.HTMLEscapeString(csrf) + `"><input type="hidden" name="account_id" value="` + template.HTMLEscapeString(id) + `"><input type="hidden" name="collection_id" value="` + template.HTMLEscapeString(collectionID) + `"><label>View <input type="checkbox" name="view"` + map[bool]string{true: " checked", false: ""}[view == 1] + `></label><label>Modify <input type="checkbox" name="modify"` + map[bool]string{true: " checked", false: ""}[modify == 1] + `></label><label>Undo <input type="checkbox" name="undo"` + map[bool]string{true: " checked", false: ""}[undo == 1] + `></label><button>Save</button></form></td></tr>`
	}
	body += `</table><script src="/static/admin.js" defer></script>`
	s.render(w, map[string]any{"Title": "Account", "Account": account, "CSRF": csrf, "Body": template.HTML(body)})
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
	csrf := ""
	if cookie, err := r.Cookie("csrf"); err == nil {
		csrf = cookie.Value
	}
	body.WriteString(`</table><h2>Add collection</h2><form method="post" action="/api/admin/collections" data-admin-json><input type="hidden" name="csrf" value="` + template.HTMLEscapeString(csrf) + `"><label>Name <input name="name" required></label><label>Root path <input name="root" required></label><button>Create</button></form><script src="/static/admin.js" defer></script>`)
	s.render(w, map[string]any{"Title": "Collections", "Account": account, "CSRF": csrf, "Body": template.HTML(body.String())})
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

func (s *Server) adminCreateCollection(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.adminActor(w, r, true)
	if !ok {
		return
	}
	var input struct {
		Name string `json:"name"`
		Root string `json:"root"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.Root) == "" {
		http.Error(w, "name and root are required", http.StatusBadRequest)
		return
	}
	collectionID, err := media.NewStore(s.db).CreateCollection(r.Context(), strings.TrimSpace(input.Name), strings.TrimSpace(input.Root))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := audit.NewStore(s.db).Record(r.Context(), audit.Event{AccountID: actor.ID, Action: "collection-create", EntityType: "collection", EntityID: collectionID, New: map[string]any{"name": input.Name, "root": input.Root}}); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": collectionID, "name": input.Name})
}

func (s *Server) adminCreatePipeline(w http.ResponseWriter, r *http.Request) {
	actor, ok := s.adminActor(w, r, false)
	if !ok {
		return
	}
	var input struct {
		ModelFamily  string `json:"model_family"`
		ModelVersion string `json:"model_version"`
		Provider     string `json:"provider"`
		Dimension    int    `json:"dimension"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || input.ModelFamily == "" || input.ModelVersion == "" || input.Provider == "" || input.Dimension <= 0 {
		http.Error(w, "model family, version, provider, and positive dimension are required", 400)
		return
	}
	pipelineID, err := analysis.NewStore(s.db).RegisterPipeline(r.Context(), analysis.Pipeline{ModelFamily: input.ModelFamily, ModelVersion: input.ModelVersion, ModelChecksums: map[string]string{}, DetectorConfig: map[string]any{}, PreprocessingConfig: map[string]any{}, EmbeddingDimension: input.Dimension, EmbeddingDType: "float32", Normalization: "l2", ExecutionProvider: input.Provider, DependencyVersions: map[string]string{}})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := audit.NewStore(s.db).Record(r.Context(), audit.Event{AccountID: actor.ID, Action: "pipeline-create", EntityType: "pipeline", EntityID: pipelineID}); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": pipelineID})
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

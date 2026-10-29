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
	"github.com/pjg/face_me_bro/internal/recognition"
	"github.com/pjg/face_me_bro/internal/representative"
	"github.com/pjg/face_me_bro/internal/review"
	"github.com/pjg/face_me_bro/internal/search"
)

type Server struct {
	auth                  auth.Store
	db                    *sql.DB
	pythonPath            string
	templates             *template.Template
	statusJSON            []byte
	setupJSON             []byte
	recognitionThresholds recognition.Thresholds
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

type recognitionCandidateView struct {
	PersonID, Name, Evidence string
	Similarity, Margin       float64
	Support                  int
	Quality, Detection       float64
}

func (s *Server) latestRecognitionCandidates(ctx context.Context, faceID string) ([]recognitionCandidateView, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT rc.person_id, COALESCE(NULLIF(p.name, ''), substr(rc.person_id, -8), rc.person_id),
		       rc.similarity, rc.margin, rc.support_count, rc.quality, rc.detection_confidence, rc.evidence
		FROM recognition_candidates rc LEFT JOIN people p ON p.id = rc.person_id
		WHERE rc.face_id = ? AND rc.created_at = (SELECT MAX(created_at) FROM recognition_candidates WHERE face_id = ?)
		ORDER BY rc.rank LIMIT 5`, faceID, faceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []recognitionCandidateView
	for rows.Next() {
		var candidate recognitionCandidateView
		if err := rows.Scan(&candidate.PersonID, &candidate.Name, &candidate.Similarity, &candidate.Margin, &candidate.Support, &candidate.Quality, &candidate.Detection, &candidate.Evidence); err != nil {
			return nil, err
		}
		result = append(result, candidate)
	}
	return result, rows.Err()
}

func renderRecognitionCandidates(candidates []recognitionCandidateView, faceID, assignedPersonID string, canAssign bool) string {
	if len(candidates) == 0 {
		return ""
	}
	var body strings.Builder
	body.WriteString(`<div class="person-candidates recognition-candidates"><strong>Candidates:</strong><div class="candidate-list">`)
	for index, candidate := range candidates {
		if index > 0 {
			body.WriteString(`<br>`)
		}
		name := `<a href="/review/people/` + template.HTMLEscapeString(candidate.PersonID) + `">` + template.HTMLEscapeString(candidate.Name) + `</a>`
		if candidate.PersonID == assignedPersonID {
			name = `<strong>` + name + `</strong>`
		}
		body.WriteString(name + ` (S: ` + formatScore(candidate.Similarity) + `% M: ` + formatScore(candidate.Margin) + `% Sp: ` + fmt.Sprint(candidate.Support) + ` Q: ` + formatScore(candidate.Quality) + `% D: ` + formatScore(candidate.Detection) + `%)`)
		if canAssign {
			body.WriteString(` <button type="button" data-action="assign-candidate" data-face-id="` + template.HTMLEscapeString(faceID) + `" data-person-id="` + template.HTMLEscapeString(candidate.PersonID) + `" data-candidate-id="">Assign</button>`)
		}
	}
	body.WriteString(`</div></div>`)
	return body.String()
}

func possibleFaceForPerson(state, assignedPersonID, personID string, candidates []possibleCandidateView) bool {
	_, possible := possibleSimilarityForPerson(state, assignedPersonID, personID, candidates)
	return possible
}

func possibleSimilarityForPerson(state, assignedPersonID, personID string, candidates []possibleCandidateView) (float64, bool) {
	if assignedPersonID == personID || (state != string(review.Unknown) && state != string(review.Provisional)) {
		return 0, false
	}
	for _, candidate := range candidates {
		if candidate.PersonID == personID {
			return candidate.Similarity, true
		}
	}
	return 0, false
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

func renderPossibleMatches(candidates []possibleCandidateView, faceID, assignedPersonID string, canAssign bool) string {
	if len(candidates) == 0 {
		return ""
	}
	var body strings.Builder
	body.WriteString(`<div class="person-candidates possible-candidates"><strong>Possible matches:</strong><div class="candidate-list">`)
	for _, candidate := range candidates {
		name := `<a href="/review/people/` + template.HTMLEscapeString(candidate.PersonID) + `">` + template.HTMLEscapeString(candidate.PersonName) + `</a>`
		if candidate.PersonID == assignedPersonID {
			name = `<strong>` + name + `</strong>`
		}
		body.WriteString(`<div class="possible-match">` + name + ` (S: ` + formatScore(candidate.Similarity) + `% M: ` + formatScore(candidate.Margin) + `% Sp: ` + fmt.Sprint(candidate.Support) + ` Q: ` + formatScore(candidate.Quality) + `% D: ` + formatScore(candidate.Detection) + `% <a href="/review/candidate-runs/` + template.HTMLEscapeString(candidate.RunID) + `">run</a>)`)
		if canAssign {
			body.WriteString(` <button type="button" data-action="assign-candidate" data-face-id="` + template.HTMLEscapeString(faceID) + `" data-person-id="` + template.HTMLEscapeString(candidate.PersonID) + `" data-candidate-id="` + template.HTMLEscapeString(candidate.ID) + `">Assign</button>`)
		}
		body.WriteString(`</div>`)
	}
	body.WriteString(`</div></div>`)
	return body.String()
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
	return newServer(authStore, database, "", recognition.DefaultThresholds())
}

func NewServerWithPython(authStore auth.Store, database *sql.DB, pythonPath string) (*Server, error) {
	return newServer(authStore, database, pythonPath, recognition.DefaultThresholds())
}

// NewServerWithDiagnostics configures the dashboard's status and setup-check
// pages with the same snapshots used when the serve command starts.
func NewServerWithDiagnostics(authStore auth.Store, database *sql.DB, pythonPath string, statusJSON, setupJSON []byte) (*Server, error) {
	return NewServerWithDiagnosticsAndThresholds(authStore, database, pythonPath, statusJSON, setupJSON, recognition.DefaultThresholds())
}

// NewServerWithDiagnosticsAndThresholds configures the dashboard with the
// resolved recognition thresholds used by recognition jobs.
func NewServerWithDiagnosticsAndThresholds(authStore auth.Store, database *sql.DB, pythonPath string, statusJSON, setupJSON []byte, thresholds recognition.Thresholds) (*Server, error) {
	server, err := newServer(authStore, database, pythonPath, thresholds)
	if err != nil {
		return nil, err
	}
	server.statusJSON = append([]byte(nil), statusJSON...)
	server.setupJSON = append([]byte(nil), setupJSON...)
	return server, nil
}

func newServer(authStore auth.Store, database *sql.DB, pythonPath string, thresholds recognition.Thresholds) (*Server, error) {
	//  body{font-family:system-ui,sans-serif;max-width:70rem;margin:2rem auto;padding:0 1rem}

	page := template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>FMB: {{.Title}}</title>
<style>
 body { font: 14px system-ui; margin: 20px; color: #222 }
 .page-header { position: sticky; top: 0; z-index: 10; background: #fff; padding: 2px 0}
 nav{display:flex;align-items:center;gap:.5rem;justify-content:space-between}
 nav .breadcrumb{display:flex;align-items:center;gap:.5rem;margin-right:auto}
 table{border-collapse:collapse}
 td,th{padding:.4rem;border:1px solid #ccc}
 input,button{padding:.4rem;margin:.2rem 0}
 .person-candidates{font-size:.9em;line-height:1.35;margin-top:6px}
 .candidate-list{margin-left:1.5em}
 .error{color:#a00}
</style>{{with .PageStyle}}<style>{{.}}</style>{{end}}</head>
<body><nav><a href="/">Face-Me-Bro</a>{{with .Breadcrumb}}<span class="breadcrumb">› {{.}}</span>{{end}}{{if .Account}}<span>{{.Account.Username}} <form method="post" action="/logout" style="display:inline"><input type="hidden" name="csrf" value="{{.CSRF}}"><button>Log out</button></form></span>{{end}}</nav><main>{{.Body}}</main></body></html>`))
	return &Server{auth: authStore, db: database, pythonPath: pythonPath, recognitionThresholds: thresholds, templates: page}, nil
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
	mux.HandleFunc("GET /static/people.js", s.peopleScript)
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
	collections, err := s.reviewPhotoCollections(r.Context(), allowed)
	if err != nil {
		http.Error(w, "collection lookup failed", http.StatusInternalServerError)
		return
	}
	collectionID := query.Get("collection_id")
	if !containsCollection(collections, collectionID) {
		collectionID = ""
	}
	faceMode := query.Get("faces")
	if faceMode == "" {
		faceMode = "with"
	} else if faceMode != "all" && faceMode != "with" && faceMode != "without" {
		faceMode = "all"
	}
	assignmentStates := reviewPhotoAssignmentStates(query)
	statusFilters := query["status"]
	validStatus := map[string]bool{"dont-track": true, "unknown": true, "possibles": true}
	filteredStatuses := statusFilters[:0]
	for _, value := range statusFilters {
		if validStatus[value] {
			filteredStatuses = append(filteredStatuses, value)
		}
	}
	sortKey := query.Get("sort")
	if sortKey != "faces" && sortKey != "person" && sortKey != "assigned" && sortKey != "to-assign" {
		sortKey = "path"
	}
	direction := query.Get("direction")
	desc := direction == "desc"
	perPage := 10
	if value, parseErr := strconv.Atoi(query.Get("per_page")); parseErr == nil && (value == 10 || value == 20 || value == 25 || value == 50 || value == 100) {
		perPage = value
	}
	page := 1
	if value, parseErr := strconv.Atoi(query.Get("page")); parseErr == nil && value > 0 {
		page = value
	}
	result, err := search.NewStore(s.db).ReviewPhotosPage(r.Context(), search.ReviewPhotoOptions{
		AllowedCollectionIDs: allowed,
		CollectionID:         collectionID,
		PathQuery:            query.Get("path"),
		FaceMode:             faceMode,
		AssignmentStates:     assignmentStates,
		StatusFilters:        filteredStatuses,
		ExcludeDontTrack:     !containsString(filteredStatuses, "dont-track"),
		PersonQuery:          query.Get("person"),
		Sort:                 sortKey,
		Desc:                 desc,
		Limit:                perPage,
		Offset:               (page - 1) * perPage,
	})
	if err != nil {
		http.Error(w, "search failed", http.StatusInternalServerError)
		return
	}
	total := result.Total
	pageCount := (total + perPage - 1) / perPage
	if pageCount == 0 {
		pageCount = 1
	}
	if page > pageCount {
		page = pageCount
	}
	pagePhotos := result.Photos
	pageSize := photoSize(query.Get("photo_size"))
	pageURL := func(target int) string {
		values := copyQuery(query)
		values.Set("page", strconv.Itoa(target))
		values.Set("per_page", strconv.Itoa(perPage))
		return "/review/photos?" + values.Encode()
	}
	var body strings.Builder
	body.WriteString(`<h1>Photos (` + fmt.Sprint(len(pagePhotos)) + `/` + fmt.Sprint(total) + `)</h1><p>Review faces and use the face API to assign people.</p>`)
	body.WriteString(`<form id="photos-controls" method="get"><strong>Filter</strong> <input name="path" value="` + template.HTMLEscapeString(query.Get("path")) + `" placeholder="Filter path or file name"><label><strong>Collection</strong> <select name="collection_id"><option value=""` + selectedAttr("", collectionID) + `>All accessible</option>`)
	for _, collection := range collections {
		body.WriteString(`<option value="` + template.HTMLEscapeString(collection.ID) + `"` + selectedAttr(collection.ID, collectionID) + `>` + template.HTMLEscapeString(collection.Name) + `</option>`)
	}
	body.WriteString(`</select></label><label><strong>Faces</strong> <select name="faces"><option value="all"` + selectedAttr("all", faceMode) + `>All</option><option value="with"` + selectedAttr("with", faceMode) + `>With faces</option><option value="without"` + selectedAttr("without", faceMode) + `>Without faces</option></select></label><label><strong>Person</strong> <input name="person" value="` + template.HTMLEscapeString(query.Get("person")) + `" placeholder="Name or ID"></label><strong>Include:</strong>`)
	for _, item := range []struct{ Value, Label string }{{"dont-track", "Don't Track"}, {"unknown", "Unknown"}, {"possibles", "Possibles"}} {
		body.WriteString(`<label><input type="checkbox" name="status" value="` + item.Value + `"` + checkedAttr(containsString(filteredStatuses, item.Value)) + `> ` + item.Label + `</label>`)
	}
	body.WriteString(`<strong>Assignment:</strong>`)
	for _, item := range []struct{ Value, Label string }{{"assigned-user", "User"}, {"assigned-system", "System"}, {"provisional", "Provisional"}, {"other", "Other"}} {
		body.WriteString(`<label><input type="checkbox" name="assignment" value="` + item.Value + `"` + checkedAttr(reviewPhotoAssignmentSelected(query, item.Value)) + `> ` + item.Label + `</label>`)
	}
	body.WriteString(`<span class="photos-controls-break"></span><label><strong>Sort</strong> <select name="sort">`)
	for _, item := range []struct{ Value, Label string }{{"path", "Path"}, {"faces", "Faces"}, {"person", "Person"}, {"assigned", "Assigned"}, {"to-assign", "To Be Assigned"}} {
		body.WriteString(`<option value="` + item.Value + `"` + selectedAttr(item.Value, sortKey) + `>` + item.Label + `</option>`)
	}
	body.WriteString(`</select></label><label><strong>Direction</strong> <select name="direction"><option value="asc"` + selectedAttr("asc", direction) + `>Ascending</option><option value="desc"` + selectedAttr("desc", direction) + `>Descending</option></select></label><label><strong>Per Page</strong> <select name="per_page" onchange="this.form.submit()">`)
	for _, size := range []int{10, 20, 25, 50, 100} {
		body.WriteString(`<option value="` + fmt.Sprint(size) + `"` + selectedAttr(fmt.Sprint(size), fmt.Sprint(perPage)) + `>` + fmt.Sprint(size) + `</option>`)
	}
	body.WriteString(`</select></label><fieldset class="photo-size"><legend>Photo size</legend>`)
	for _, size := range []string{"0.25", "0.5", "1", "1.5", "2"} {
		body.WriteString(`<label><input type="radio" name="photo_size" value="` + size + `"` + checkedAttr(size == pageSize) + `> ` + size + `</label>`)
	}
	body.WriteString(`</fieldset><button>Apply</button></form>`)
	renderPager := func() {
		body.WriteString(`<nav class="pager photos-page-pager"><a href="` + template.HTMLEscapeString(pageURL(1)) + `">First</a> <a href="` + template.HTMLEscapeString(pageURL(maxInt(1, page-1))) + `">Previous</a> <span>Page ` + fmt.Sprint(page) + `/` + fmt.Sprint(pageCount) + `</span> <a href="` + template.HTMLEscapeString(pageURL(minInt(pageCount, page+1))) + `">Next</a> <a href="` + template.HTMLEscapeString(pageURL(pageCount)) + `">Last</a> <form class="page-jump" method="get" action="/review/photos"><label>Jump to Page <input type="number" name="page" min="1" value="` + fmt.Sprint(page) + `" onkeydown="if(event.key==='Enter')this.form.submit()"></label>`)
		values := copyQuery(query)
		values.Del("page")
		for key, list := range values {
			for _, value := range list {
				body.WriteString(`<input type="hidden" name="` + template.HTMLEscapeString(key) + `" value="` + template.HTMLEscapeString(value) + `">`)
			}
		}
		body.WriteString(`</form></nav>`)
	}
	renderPager()
	body.WriteString(`<div class="photo-list photo-size-` + strings.ReplaceAll(pageSize, ".", "-") + `">`)
	for _, photo := range pagePhotos {
		cardClass := photoCardClass(photo.Faces)
		body.WriteString(`<article class="card photo-card ` + cardClass + `"><a class="photo-preview" href="/review/photos?path_id=` + template.HTMLEscapeString(photo.PathID) + `"><img loading="lazy" src="/api/photos/` + template.HTMLEscapeString(photo.PathID) + `" alt="` + template.HTMLEscapeString(photo.RelativePath) + `"></a><div class="path"><a href="/review/photos?path_id=` + template.HTMLEscapeString(photo.PathID) + `">` + template.HTMLEscapeString(photo.RelativePath) + `</a></div><div class="photo-meta">Faces: ` + fmt.Sprint(photo.NeedsReviewCount) + ` / ` + fmt.Sprint(photo.FaceCount) + `</div>` + photoPeopleTable(photo.Faces, pageSize) + `</article>`)
	}
	if len(pagePhotos) == 0 {
		body.WriteString(`<p class="photo-empty">No photos match the active filters.</p>`)
	}
	body.WriteString(`</div>`)
	renderPager()
	style := template.CSS(`#photos-controls{display:flex;align-items:center;flex-wrap:wrap;gap:.4rem;margin:.5rem 0;width:max-content;max-width:100%;overflow-x:auto}#photos-controls .photos-controls-break{flex-basis:100%;height:0}#photos-controls>button{margin-left:4rem}.photos-page-pager{width:max-content;display:flex;align-items:center;gap:.4rem;margin:.5rem 0}.photos-page-pager .page-jump{display:flex;align-items:center;gap:.4rem}.photo-size{border:0;padding:0;margin:0;display:flex;gap:.35rem;align-items:center}.photo-list{display:flex;flex-wrap:wrap;gap:18px}.photo-card{width:min(calc(420px * var(--photo-scale)),calc(45vw * var(--photo-scale)));border:2px solid #D0021B;padding:10px}.photo-card.assigned{border-color:#7ED321}.photo-card.provisional{border-color:#F8E71C}.photo-card .photo-preview{display:block}.photo-card img{display:block;width:100%;height:auto;max-height:55vh;object-fit:contain}.photo-card .path{margin-top:8px;overflow-wrap:anywhere}.photo-meta{margin-top:6px;line-height:1.35}.photo-people-table{width:100%;margin-top:6px;border-collapse:collapse;table-layout:fixed}.photo-people-table td{padding:2px 4px;border:1px solid #ddd;vertical-align:top;overflow-wrap:anywhere}.photo-size-0-25{--photo-scale:.25}.photo-size-0-5{--photo-scale:.5}.photo-size-1{--photo-scale:1}.photo-size-1-5{--photo-scale:1.5}.photo-size-2{--photo-scale:2}.photo-size-0-25 .photo-people-table td,.photo-size-0-5 .photo-people-table td{width:100%}.photo-size-1 .photo-people-table td{width:50%}.photo-size-1-5 .photo-people-table td{width:33.333%}.photo-size-2 .photo-people-table td{width:25%}.photo-empty{width:100%;padding:.75rem;border:1px solid #D0021B;background:#fff1f1}@media(max-width:600px){.photo-card{width:100%}}`)
	s.render(w, map[string]any{"Title": "Photos", "Account": account, "Body": template.HTML(body.String()), "PageStyle": style})
}

type reviewPhotoCollection struct{ ID, Name string }

func (s *Server) reviewPhotoCollections(ctx context.Context, allowed []string) ([]reviewPhotoCollection, error) {
	query := `SELECT id, name FROM collections`
	args := []any{}
	if allowed != nil {
		if len(allowed) == 0 {
			return nil, nil
		}
		query += ` WHERE id IN (` + placeholders(len(allowed)) + `)`
		for _, id := range allowed {
			args = append(args, id)
		}
	}
	query += ` ORDER BY lower(name), id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []reviewPhotoCollection
	for rows.Next() {
		var value reviewPhotoCollection
		if err := rows.Scan(&value.ID, &value.Name); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func containsCollection(collections []reviewPhotoCollection, id string) bool {
	if id == "" {
		return true
	}
	for _, collection := range collections {
		if collection.ID == id {
			return true
		}
	}
	return false
}

func reviewPhotoAssignmentStates(query url.Values) []string {
	requested := false
	for _, value := range query["assignment"] {
		if value != "" {
			requested = true
			break
		}
	}
	if !requested {
		return nil
	}
	var states []string
	for _, value := range query["assignment"] {
		switch value {
		case "assigned-user", "assigned-system", "provisional":
			states = append(states, value)
		case "other":
			states = append(states, "unassigned", "unknown", "ambiguous")
		}
	}
	return states
}

func reviewPhotoAssignmentSelected(query url.Values, expected string) bool {
	values, specified := query["assignment"]
	if !specified {
		return true
	}
	return containsString(values, expected)
}

func photoAssignedSortKey(photo search.ReviewPhoto) string {
	if photo.FaceCount == 0 {
		return "000000000000000000"
	}
	return fmt.Sprintf("%09d", photo.AssignedCount*100000/photo.FaceCount)
}

func photoSize(value string) string {
	switch value {
	case "0.25", "0.5", "1", "1.5", "2":
		return value
	default:
		return "1"
	}
}

func photoCardClass(faces []search.ReviewPhotoFace) string {
	if len(faces) == 0 {
		return "needs-attention"
	}
	allAssigned := true
	allProvisionallySafe := true
	for _, face := range faces {
		switch face.State {
		case "assigned-user", "assigned-system":
		case "provisional":
			allAssigned = false
		default:
			allAssigned = false
			allProvisionallySafe = false
		}
	}
	if allAssigned {
		return "assigned"
	}
	if allProvisionallySafe {
		return "provisional"
	}
	return "needs-attention"
}

func faceStatusColorClass(state string, reference bool) string {
	if reference {
		return "reference"
	}
	switch state {
	case "assigned-user", "assigned-system":
		return "assigned"
	case "provisional":
		return "provisional"
	default:
		return "needs-attention"
	}
}

func photoPeopleTable(faces []search.ReviewPhotoFace, size string) string {
	columns := 1
	switch size {
	case "1":
		columns = 2
	case "1.5":
		columns = 3
	case "2":
		columns = 4
	}
	if len(faces) == 0 {
		return `<table class="photo-people-table"><tbody><tr><td>Unassigned</td></tr></tbody></table>`
	}
	var body strings.Builder
	body.WriteString(`<table class="photo-people-table"><tbody>`)
	for start := 0; start < len(faces); start += columns {
		body.WriteString(`<tr>`)
		for offset := 0; offset < columns; offset++ {
			index := start + offset
			if index >= len(faces) {
				body.WriteString(`<td></td>`)
				continue
			}
			face := faces[index]
			name := "Unassigned"
			if face.PersonName != "" {
				name = template.HTMLEscapeString(face.PersonName)
			}
			if face.PersonID != "" {
				name = `<a href="/review/people/` + template.HTMLEscapeString(face.PersonID) + `">` + name + `</a>`
			}
			body.WriteString(`<td>` + fmt.Sprintf("%d. %s (%s)", face.Index+1, name, template.HTMLEscapeString(shortAssignment(face.State))) + `</td>`)
		}
		body.WriteString(`</tr>`)
	}
	body.WriteString(`</tbody></table>`)
	return body.String()
}

func shortAssignment(state string) string {
	switch state {
	case "assigned-user":
		return "User"
	case "assigned-system":
		return "System"
	case "provisional":
		return "Prov"
	default:
		return assignmentLabel(state)
	}
}

func copyQuery(values url.Values) url.Values {
	copyValues := url.Values{}
	for key, list := range values {
		for _, value := range list {
			copyValues.Add(key, value)
		}
	}
	return copyValues
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

type photoDetailFace struct {
	ID                       string
	Index                    int
	Left, Top, Right, Bottom float64
	State, PersonID, Person  string
	Reference                bool
	Similarity, Margin       float64
	Quality, Detection       float64
	Disposition              string
	ClusterID, ClusterKey    string
	ClusterPersonID          string
	ClusterPerson            string
	ClusterConsistency       sql.NullFloat64
	ClusterConnectivity      sql.NullFloat64
	PossibleCandidates       []possibleCandidateView
	RecognitionCandidates    []recognitionCandidateView
}

type photoDetailCollection struct{ ID, Name string }

func (s *Server) photoDetailCollections(ctx context.Context, assetID string, allowed []string) ([]photoDetailCollection, error) {
	query := `SELECT DISTINCT c.id, c.name FROM photo_paths pp JOIN collections c ON c.id = pp.collection_id WHERE pp.asset_id = ? AND pp.missing_at IS NULL`
	args := []any{assetID}
	if allowed != nil {
		if len(allowed) == 0 {
			return nil, nil
		}
		query += ` AND pp.collection_id IN (` + placeholders(len(allowed)) + `)`
		for _, collectionID := range allowed {
			args = append(args, collectionID)
		}
	}
	query += ` ORDER BY lower(c.name), c.id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []photoDetailCollection
	for rows.Next() {
		var value photoDetailCollection
		if err := rows.Scan(&value.ID, &value.Name); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func photoAssignedFaceCount(faces []photoDetailFace) int {
	count := 0
	for _, face := range faces {
		if face.State == string(review.AssignedUser) || face.State == string(review.AssignedSystem) {
			count++
		}
	}
	return count
}

func photoReviewFaceCount(faces []photoDetailFace) int {
	return len(faces) - photoAssignedFaceCount(faces)
}

func photoFaceAssigned(face photoDetailFace) bool {
	return face.State == string(review.AssignedUser) || face.State == string(review.AssignedSystem)
}

func photoDetailPeopleSummary(faces []photoDetailFace) string {
	seen := map[string]bool{}
	var parts []string
	for _, face := range faces {
		if face.PersonID == "" || seen[face.PersonID] {
			continue
		}
		seen[face.PersonID] = true
		name := face.Person
		if name == "" {
			name = idSuffix(face.PersonID)
		}
		parts = append(parts, `<a href="/review/people/`+template.HTMLEscapeString(face.PersonID)+`">`+template.HTMLEscapeString(name)+`</a>`)
	}
	if len(parts) == 0 {
		return "Unassigned"
	}
	return strings.Join(parts, ", ")
}

func photoCollectionLinks(collections []photoDetailCollection) string {
	if len(collections) == 0 {
		return "None"
	}
	parts := make([]string, 0, len(collections))
	for _, collection := range collections {
		parts = append(parts, `<a href="/review/photos?collection_id=`+template.HTMLEscapeString(collection.ID)+`">`+template.HTMLEscapeString(collection.Name)+`</a>`)
	}
	return strings.Join(parts, ", ")
}

func (s *Server) reviewPhotoDetail(w http.ResponseWriter, r *http.Request, pathID string) {
	account, ok := s.requireAccount(w, r)
	if !ok {
		return
	}
	var collectionID, relative, assetID string
	var width, height int
	if err := s.db.QueryRowContext(r.Context(), `SELECT pp.collection_id, pp.relative_path, pp.asset_id,
		COALESCE((SELECT a.source_width FROM analyses a WHERE a.asset_id = pp.asset_id AND a.status = 'completed' ORDER BY a.completed_at DESC, a.created_at DESC LIMIT 1), pa.width, 0),
		COALESCE((SELECT a.source_height FROM analyses a WHERE a.asset_id = pp.asset_id AND a.status = 'completed' ORDER BY a.completed_at DESC, a.created_at DESC LIMIT 1), pa.height, 0)
		FROM photo_paths pp JOIN photo_assets pa ON pa.id = pp.asset_id WHERE pp.id = ? AND pp.missing_at IS NULL`, pathID).Scan(&collectionID, &relative, &assetID, &width, &height); err != nil {
		http.NotFound(w, r)
		return
	}
	allowed, err := s.auth.CanAccessCollection(r.Context(), account.ID, collectionID, false, false)
	if err != nil || !allowed {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT f.id, f.face_index, f.box_left, f.box_top, f.box_right, f.box_bottom,
		       COALESCE(ca.state, 'unassigned'), COALESCE(ca.person_id, ''),
		       COALESCE(NULLIF(p.name, ''), substr(ca.person_id, -8), ''),
		       COALESCE(rc.similarity, 0), COALESCE(rc.margin, 0),
		       COALESCE(f.quality, 0), COALESCE(f.detection_confidence, 0),
		       COALESCE((SELECT MAX(cfr.reference) FROM current_face_references cfr WHERE cfr.face_id = f.id), 0),
		       COALESCE(cd.disposition, ''),
		       COALESCE((SELECT c.id FROM cluster_memberships cm JOIN clusters c ON c.id = cm.cluster_id JOIN clustering_runs cr ON cr.id = c.clustering_run_id WHERE cm.face_id = f.id AND cr.status = 'active' ORDER BY c.id LIMIT 1), ''),
		       COALESCE((SELECT c.cluster_key FROM cluster_memberships cm JOIN clusters c ON c.id = cm.cluster_id JOIN clustering_runs cr ON cr.id = c.clustering_run_id WHERE cm.face_id = f.id AND cr.status = 'active' ORDER BY c.id LIMIT 1), ''),
		       (SELECT c.consistency FROM cluster_memberships cm JOIN clusters c ON c.id = cm.cluster_id JOIN clustering_runs cr ON cr.id = c.clustering_run_id WHERE cm.face_id = f.id AND cr.status = 'active' ORDER BY c.id LIMIT 1),
		       (SELECT c.connectivity FROM cluster_memberships cm JOIN clusters c ON c.id = cm.cluster_id JOIN clustering_runs cr ON cr.id = c.clustering_run_id WHERE cm.face_id = f.id AND cr.status = 'active' ORDER BY c.id LIMIT 1),
		       COALESCE((SELECT c.proposed_person_id FROM cluster_memberships cm JOIN clusters c ON c.id = cm.cluster_id JOIN clustering_runs cr ON cr.id = c.clustering_run_id WHERE cm.face_id = f.id AND cr.status = 'active' ORDER BY c.id LIMIT 1), ''),
		       COALESCE((SELECT COALESCE(NULLIF(p2.name, ''), substr(c2.proposed_person_id, -8), '') FROM cluster_memberships cm2 JOIN clusters c2 ON c2.id = cm2.cluster_id JOIN clustering_runs cr2 ON cr2.id = c2.clustering_run_id LEFT JOIN people p2 ON p2.id = c2.proposed_person_id WHERE cm2.face_id = f.id AND cr2.status = 'active' ORDER BY c2.id LIMIT 1), '')
		FROM faces f JOIN analyses a ON a.id = f.analysis_id
		LEFT JOIN current_face_assignments ca ON ca.face_id = f.id
		LEFT JOIN people p ON p.id = ca.person_id
		LEFT JOIN current_face_dispositions cd ON cd.face_id = f.id
		LEFT JOIN recognition_candidates rc ON rc.id = (SELECT id FROM recognition_candidates WHERE face_id = f.id ORDER BY created_at DESC, rank LIMIT 1)
		WHERE a.asset_id = ? AND a.status IN ('completed', 'completed_with_errors')
		ORDER BY f.face_index`, assetID)
	if err != nil {
		http.Error(w, "face lookup failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var faces []photoDetailFace
	for rows.Next() {
		var value photoDetailFace
		if err := rows.Scan(&value.ID, &value.Index, &value.Left, &value.Top, &value.Right, &value.Bottom, &value.State, &value.PersonID, &value.Person, &value.Similarity, &value.Margin, &value.Quality, &value.Detection, &value.Reference, &value.Disposition, &value.ClusterID, &value.ClusterKey, &value.ClusterConsistency, &value.ClusterConnectivity, &value.ClusterPersonID, &value.ClusterPerson); err != nil {
			return
		}
		value.PossibleCandidates, err = s.currentPossibleCandidates(r.Context(), value.ID)
		if err != nil {
			http.Error(w, "possible candidate lookup failed", http.StatusInternalServerError)
			return
		}
		value.RecognitionCandidates, err = s.latestRecognitionCandidates(r.Context(), value.ID)
		if err != nil {
			http.Error(w, "candidate lookup failed", http.StatusInternalServerError)
			return
		}
		faces = append(faces, value)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "face lookup failed", http.StatusInternalServerError)
		return
	}
	sort.SliceStable(faces, func(i, j int) bool {
		if photoFaceAssigned(faces[i]) != photoFaceAssigned(faces[j]) {
			return !photoFaceAssigned(faces[i])
		}
		return faces[i].Index < faces[j].Index
	})
	accessibleCollections, err := s.auth.AccessibleCollections(r.Context(), account.ID)
	if err != nil {
		http.Error(w, "permission lookup failed", http.StatusInternalServerError)
		return
	}
	collections, err := s.photoDetailCollections(r.Context(), assetID, accessibleCollections)
	if err != nil {
		http.Error(w, "collection lookup failed", http.StatusInternalServerError)
		return
	}
	body := strings.Builder{}
	body.WriteString(`<div id="photo-page" data-csrf="` + template.HTMLEscapeString(csrfCookie(r)) + `" data-photo-id="` + template.HTMLEscapeString(pathID) + `"><header class="photo-header"><h1>` + template.HTMLEscapeString(relative) + `</h1><dl class="photo-summary-table"><div><dt>Faces</dt><dd>` + fmt.Sprint(photoAssignedFaceCount(faces)) + ` / ` + fmt.Sprint(len(faces)) + `</dd></div><div><dt>People</dt><dd>` + photoDetailPeopleSummary(faces) + `</dd></div><div><dt>Collections</dt><dd>` + photoCollectionLinks(collections) + `</dd></div></dl></header><form id="photo-size-controls" method="get"><input type="hidden" name="path_id" value="` + template.HTMLEscapeString(pathID) + `"><div class="photo-size-controls"><fieldset class="photo-size"><legend>Photo size</legend>`)
	pageSize := photoSize(r.URL.Query().Get("photo_size"))
	for _, size := range []string{"0.25", "0.5", "1", "1.5", "2"} {
		body.WriteString(`<label><input type="radio" name="photo_size" value="` + size + `"` + checkedAttr(size == pageSize) + `> ` + size + `</label>`)
	}
	body.WriteString(`</fieldset><button>Apply</button></div></form><p class="photo-path-summary">Faces needing review: ` + fmt.Sprint(photoReviewFaceCount(faces)) + ` / ` + fmt.Sprint(len(faces)) + `</p><div class="photo-detail photo-size-` + strings.ReplaceAll(pageSize, ".", "-") + `"><img src="/api/photos/` + template.HTMLEscapeString(pathID) + `" alt="` + template.HTMLEscapeString(relative) + `"><div class="photo-boxes">`)
	for _, value := range faces {
		body.WriteString(fmt.Sprintf(`<span class="photo-box %s" style="left:%s%%;top:%s%%;width:%s%%;height:%s%%"></span><span class="photo-face-number" style="left:%s%%;top:%s%%">%d</span>`, faceStatusColorClass(value.State, value.Reference), percent(value.Left, width), percent(value.Top, height), percent(value.Right-value.Left, width), percent(value.Bottom-value.Top, height), percent(value.Right, width), percent(value.Top, height), value.Index+1))
	}
	body.WriteString(`</div></div><div class="photo-face-grid">`)
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
		if photoFaceAssigned(value) {
			continue
		}
		cardClass := "card photo-face"
		if value.State == string(review.Provisional) {
			cardClass += " provisional"
		} else {
			cardClass += " needs-attention"
		}
		personName := value.Person
		if personName == "" {
			personName = "Unassigned"
		}
		body.WriteString(`<article class="` + cardClass + `" data-face-id="` + template.HTMLEscapeString(value.ID) + `"><div class="photo-face-heading"><strong>Face ` + fmt.Sprint(value.Index+1) + `:</strong> `)
		if value.PersonID != "" && value.Person != "" {
			body.WriteString(`<img loading="lazy" class="assigned-face-person-image" alt="" src="/api/people/` + template.HTMLEscapeString(value.PersonID) + `/representative"> `)
			body.WriteString(`<a href="/review/people/` + template.HTMLEscapeString(value.PersonID) + `">` + template.HTMLEscapeString(personName) + `</a>`)
		} else {
			body.WriteString(template.HTMLEscapeString(personName))
		}
		body.WriteString(` [` + template.HTMLEscapeString(shortAssignment(value.State)) + `]</div>`)
		if len(value.PossibleCandidates) > 0 {
			body.WriteString(renderPossibleMatches(value.PossibleCandidates, value.ID, value.PersonID, account.Role != auth.Viewer))
		}
		if len(value.RecognitionCandidates) > 0 {
			body.WriteString(renderRecognitionCandidates(value.RecognitionCandidates, value.ID, value.PersonID, account.Role != auth.Viewer))
		}
		if value.ClusterID != "" {
			clusterPerson := ""
			if value.ClusterPersonID != "" {
				clusterPerson = `<a href="/review/people/` + template.HTMLEscapeString(value.ClusterPersonID) + `">` + template.HTMLEscapeString(value.ClusterPerson) + `</a>:`
			}
			body.WriteString(`<p class="photo-cluster"><strong>Cluster:</strong> <a href="/review/clusters/` + template.HTMLEscapeString(value.ClusterID) + `">` + template.HTMLEscapeString(idSuffix(value.ClusterID)) + `</a>: ` + clusterPerson + ` <strong>Cs:</strong> ` + formatScoreUnavailable(value.ClusterConsistency.Float64, value.ClusterConsistency.Valid) + ` <strong>Cn:</strong> ` + formatScoreUnavailable(value.ClusterConnectivity.Float64, value.ClusterConnectivity.Valid) + ` <strong>Q:</strong> ` + formatScore(value.Quality) + `% <strong>D:</strong> ` + formatScore(value.Detection) + `%</p>`)
		} else {
			body.WriteString(`<p class="photo-cluster"><strong>Cluster:</strong> No cluster</p>`)
		}
		if account.Role != auth.Viewer {
			body.WriteString(`<div class="photo-actions"><button type="button" data-action="unknown" data-face-id="` + template.HTMLEscapeString(value.ID) + `">Unknown</button><button type="button" data-action="ambiguous" data-face-id="` + template.HTMLEscapeString(value.ID) + `">Ambiguous</button><button type="button" data-action="disposition" data-disposition="incorrect-assignment" data-face-id="` + template.HTMLEscapeString(value.ID) + `">Incorrect assignment</button><button type="button" data-action="disposition" data-disposition="false-detection" data-face-id="` + template.HTMLEscapeString(value.ID) + `">False detection</button><input class="person-select-filter" data-person-filter="photo-assignment-options-` + template.HTMLEscapeString(value.ID) + `" value="` + template.HTMLEscapeString(value.Person) + `" placeholder="Type to filter people"><select class="photo-person-select" id="photo-assignment-options-` + template.HTMLEscapeString(value.ID) + `" data-assignment-person="` + template.HTMLEscapeString(value.ID) + `"><option value="" data-person-name="unknown">Unknown</option><option value="__ambiguous__" data-person-name="ambiguous">Ambiguous</option>`)
			for _, person := range people {
				body.WriteString(`<option value="` + template.HTMLEscapeString(person.ID) + `" data-person-name="` + template.HTMLEscapeString(person.Name) + `"` + selectedAttr(person.ID, value.PersonID) + `>` + template.HTMLEscapeString(person.Name) + `</option>`)
			}
			body.WriteString(`</select><button type="button" data-action="assign" data-face-id="` + template.HTMLEscapeString(value.ID) + `">Save</button>`)
			body.WriteString(`</div>`)
		}
		body.WriteString(`</article>`)
	}
	assignedFaces := make([]photoDetailFace, 0, photoAssignedFaceCount(faces))
	for _, value := range faces {
		if photoFaceAssigned(value) {
			assignedFaces = append(assignedFaces, value)
		}
	}
	if len(assignedFaces) > 0 {
		body.WriteString(`<article class="card photo-face assigned-group"><div class="photo-face-heading"><strong>Assigned faces</strong></div>`)
		for _, value := range assignedFaces {
			body.WriteString(`<div class="assigned-face-line"><strong>Face ` + fmt.Sprint(value.Index+1) + `:</strong> `)
			if value.PersonID != "" && value.Person != "" {
				body.WriteString(`<img loading="lazy" class="assigned-face-person-image" alt="" src="/api/people/` + template.HTMLEscapeString(value.PersonID) + `/representative"> `)
				body.WriteString(`<a href="/review/people/` + template.HTMLEscapeString(value.PersonID) + `">` + template.HTMLEscapeString(value.Person) + `</a>`)
			} else {
				body.WriteString(template.HTMLEscapeString(value.Person))
			}
			body.WriteString(` [` + template.HTMLEscapeString(shortAssignment(value.State)) + `]</div>`)
		}
		body.WriteString(`</article>`)
	}
	body.WriteString(`</div></div>`)
	if account.Role != auth.Viewer {
		body.WriteString(`<script src="/static/photo.js" defer></script>`)
	}
	pageStyle := template.CSS(`#photo-page{max-width:100%}.photo-header{display:block;position:sticky;top:0;z-index:10;background:#fff;padding:4px 0}.photo-header h1{margin:.15rem 0;overflow-wrap:anywhere}.photo-summary-table{display:flex;flex-wrap:wrap;gap:12px;margin:10px 0 0}.photo-summary-table>div{min-width:7rem}.photo-summary-table dt{font-size:.8em;color:#555}.photo-summary-table dd{margin:2px 0 0;font-weight:600}.photo-summary-table a{font-weight:600}.photo-path-summary{margin:.5rem 0}.photo-size-controls{display:flex;align-items:center;gap:.75rem;max-width:840px}.photo-size-controls button{margin-left:auto}.photo-detail{position:relative;display:inline-block;max-width:100%}.photo-detail>img{display:block;width:auto;max-width:min(420px,90vw);max-height:70vh}.photo-detail.photo-size-0-25>img{max-width:min(105px,90vw);max-height:17.5vh}.photo-detail.photo-size-0-5>img{max-width:min(210px,90vw);max-height:35vh}.photo-detail.photo-size-1>img{max-width:min(420px,90vw);max-height:70vh}.photo-detail.photo-size-1-5>img{max-width:min(630px,90vw);max-height:90vh}.photo-detail.photo-size-2>img{max-width:min(840px,100vw);max-height:100vh}.photo-boxes{position:absolute;inset:0}.photo-box{position:absolute;border:2px solid #f00;background:rgba(204,0,0,.15);pointer-events:none}.photo-face-number{position:absolute;transform:translateX:.15rem);color:#900;font-size:.8rem;line-height:1;pointer-events:none}.photo-face-grid{display:flex;flex-wrap:wrap;gap:18px;margin-top:18px}.photo-face{width:min(420px,45vw);border:2px solid #c62828;padding:10px}.photo-face.provisional{border-color:#d6a800}.photo-face.needs-attention{border-color:#c62828}.photo-face-heading{font-size:1rem;line-height:1.35}.photo-face .candidate-list,.photo-face .person-candidates{font-size:.9em;line-height:1.35;margin-top:6px}.photo-cluster{margin:.5rem 0}.photo-actions{display:flex;flex-wrap:wrap;gap:8px;align-items:center;margin-top:8px}.photo-actions select{max-width:100%}.photo-actions .person-select-filter{min-width:10rem}.photo-size{border:0;padding:0;margin:.5rem 0;display:flex;gap:.35rem;align-items:center}.assigned-group{border-color:#0a2}.assigned-face-line{padding:.35rem 0;border-top:1px solid #ddd}.assigned-face-line:first-of-type{border-top:0}@media(max-width:600px){.photo-face{width:100%}}`)
	pageStyle = template.CSS(string(pageStyle) + `.photo-size-controls{gap:.35rem}.photo-size-controls button{margin-left:.25rem}.photo-face-number{transform:translateX(.35rem);color:#f00;font-weight:700}.assigned-face-line{display:flex;align-items:center;gap:.35rem}.assigned-face-person-image{max-height:32px;width:auto;border-radius:4px;vertical-align:middle}`)
	pageStyle = template.CSS(string(pageStyle) + `.photo-box{border-color:#D0021B;background:rgba(208,2,27,.15)}.photo-box.reference{border-color:#50E3C2;background:rgba(80,227,194,.15)}.photo-box.assigned{border-color:#7ED321;background:rgba(126,211,33,.15)}.photo-box.provisional{border-color:#F8E71C;background:rgba(248,231,28,.15)}.photo-face{border-color:#D0021B}.photo-face.provisional{border-color:#F8E71C}.photo-face.needs-attention{border-color:#D0021B}.assigned-group{border-color:#7ED321}`)
	s.render(w, map[string]any{"Title": relative, "Account": account, "Breadcrumb": template.HTML(`<a href="/review/photos">Photos</a>`), "Body": template.HTML(body.String()), "PageStyle": pageStyle})
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
	query := r.URL.Query()
	type collectionOption struct{ ID, Name string }
	collectionQuery := `SELECT id, name FROM collections`
	collectionArgs := []any{}
	if allowed != nil {
		if len(allowed) == 0 {
			s.render(w, map[string]any{"Title": "People", "Account": account, "Body": template.HTML(`<h1>People</h1><p>No collections are assigned to this account.</p>`)})
			return
		}
		collectionQuery += ` WHERE id IN (` + placeholders(len(allowed)) + `)`
		for _, collectionID := range allowed {
			collectionArgs = append(collectionArgs, collectionID)
		}
	}
	collectionQuery += ` ORDER BY LOWER(name), id`
	collectionRows, err := s.db.QueryContext(r.Context(), collectionQuery, collectionArgs...)
	if err != nil {
		http.Error(w, "collection lookup failed", http.StatusInternalServerError)
		return
	}
	var collections []collectionOption
	for collectionRows.Next() {
		var collection collectionOption
		if err := collectionRows.Scan(&collection.ID, &collection.Name); err != nil {
			collectionRows.Close()
			http.Error(w, "collection lookup failed", http.StatusInternalServerError)
			return
		}
		collections = append(collections, collection)
	}
	if err := collectionRows.Err(); err != nil {
		collectionRows.Close()
		http.Error(w, "collection lookup failed", http.StatusInternalServerError)
		return
	}
	collectionRows.Close()
	collectionID := query.Get("collection_id")
	if collectionID != "" {
		found := false
		for _, collection := range collections {
			if collection.ID == collectionID {
				found = true
				break
			}
		}
		if !found {
			collectionID = ""
		}
	}
	visibleCollectionIDs := allowed
	if collectionID != "" {
		visibleCollectionIDs = []string{collectionID}
	}
	view := r.URL.Query().Get("view")
	if view != "possibles" && view != "dont-track" && view != "no-faces" {
		view = "all"
	}
	possibleCount := `(SELECT COUNT(DISTINCT pc.face_id) FROM possible_face_candidates pc WHERE pc.person_id = p.id AND pc.is_current = 1)`
	clusterCount := `(SELECT COUNT(DISTINCT c.id) FROM clusters c JOIN clustering_runs cr ON cr.id = c.clustering_run_id WHERE cr.status = 'active' AND (c.proposed_person_id = p.id OR EXISTS (SELECT 1 FROM cluster_memberships cm JOIN current_face_assignments cca ON cca.face_id = cm.face_id WHERE cm.cluster_id = c.id AND cca.person_id = p.id)))`
	var possibleArgs []any
	if visibleCollectionIDs != nil {
		placeholders := make([]string, len(visibleCollectionIDs))
		for i := range visibleCollectionIDs {
			placeholders[i] = "?"
			possibleArgs = append(possibleArgs, visibleCollectionIDs[i])
		}
		possibleCount = `(SELECT COUNT(DISTINCT pc.face_id) FROM possible_face_candidates pc JOIN faces pcf ON pcf.id = pc.face_id JOIN analyses pca ON pca.id = pcf.analysis_id JOIN photo_paths pcp ON pcp.asset_id = pca.asset_id WHERE pc.person_id = p.id AND pc.is_current = 1 AND pcp.collection_id IN (` + strings.Join(placeholders, ",") + `))`
	}
	peopleQuery := `SELECT p.id, COALESCE(NULLIF(p.name, ''), substr(p.id, -8)),
		CASE WHEN p.name IS NOT NULL AND trim(p.name) <> '' THEN 1 ELSE 0 END, p.dont_track,
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
	if visibleCollectionIDs != nil && (view != "no-faces" || collectionID != "") {
		placeholders := make([]string, len(visibleCollectionIDs))
		for i := range visibleCollectionIDs {
			placeholders[i] = "?"
		}
		peopleQuery += ` (EXISTS (SELECT 1 FROM faces f2 JOIN analyses a2 ON a2.id = f2.analysis_id JOIN photo_assets pa2 ON pa2.id = a2.asset_id JOIN photo_paths pp2 ON pp2.asset_id = pa2.id JOIN current_face_assignments ca2 ON ca2.face_id = f2.id WHERE ca2.person_id = p.id AND pp2.collection_id IN (` + strings.Join(placeholders, ",") + `))`
		peopleQuery += `)`
		for _, id := range visibleCollectionIDs {
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
		Named                                                                                     int
		DontTrack, Faces, User, System, Provisional, References, Recommended, Clusters, Possibles int
	}
	var people []personRow
	for rows.Next() {
		var id, name string
		var row personRow
		if err := rows.Scan(&id, &name, &row.Named, &row.DontTrack, &row.Faces, &row.User, &row.System, &row.Provisional, &row.References, &row.Recommended, &row.Clusters, &row.Possibles); err != nil {
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
	q := strings.ToLower(strings.TrimSpace(query.Get("q")))
	nameFilter := query.Get("name")
	if nameFilter != "named" && nameFilter != "unnamed" {
		nameFilter = "all"
	}
	includeDontTrack := query.Get("include_dont_track") == "true"
	includePossibles := query.Get("include_possibles") != "false"
	includeNoFaces := query.Get("include_no_faces") == "true"
	minFaces, maxFaces := parseIntFilter(query.Get("min_faces")), parseIntFilter(query.Get("max_faces"))
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
		if nameFilter == "named" && person.Named == 0 {
			continue
		}
		if nameFilter == "unnamed" && person.Named != 0 {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(person.Name), q) {
			continue
		}
		if minFaces > 0 && person.Faces < minFaces || maxFaces > 0 && person.Faces > maxFaces {
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
		case "references":
			left, right = fmt.Sprintf("%09d", people[i].References), fmt.Sprintf("%09d", people[j].References)
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
	body.WriteString(`<h1>People (` + fmt.Sprint(len(pagePeople)) + `/` + fmt.Sprint(total) + `)</h1><form id="people-controls" method="get"><strong>Filter</strong> <input name="q" value="` + template.HTMLEscapeString(query.Get("q")) + `" placeholder="Filter name"><label><strong>Name</strong> <select name="name"><option value="all"` + selectedAttr("all", nameFilter) + `>All</option><option value="named"` + selectedAttr("named", nameFilter) + `>With name</option><option value="unnamed"` + selectedAttr("unnamed", nameFilter) + `>Without name</option></select></label><label><strong>Collection</strong> <select name="collection_id"><option value=""` + selectedAttr("", collectionID) + `>All accessible</option>`)
	for _, collection := range collections {
		body.WriteString(`<option value="` + template.HTMLEscapeString(collection.ID) + `"` + selectedAttr(collection.ID, collectionID) + `>` + template.HTMLEscapeString(collection.Name) + `</option>`)
	}
	body.WriteString(`</select></label><label><strong>View</strong> <select name="view"><option value="all"` + selectedAttr("all", view) + `>All</option><option value="possibles"` + selectedAttr("possibles", view) + `>Possibles</option><option value="dont-track"` + selectedAttr("dont-track", view) + `>Don't Track</option><option value="no-faces"` + selectedAttr("no-faces", view) + `>No Faces</option></select></label><strong>Include:</strong> <label><input type="checkbox" name="include_possibles" value="true"` + checkedAttr(includePossibles) + `> Possibles</label><label><input type="checkbox" name="include_no_faces" value="true"` + checkedAttr(includeNoFaces) + `> No Faces</label><label><input type="checkbox" name="include_dont_track" value="true"` + checkedAttr(includeDontTrack) + `> Don't Track</label><strong>Assignment:</strong> <label><input type="checkbox" name="assignment_user" value="true"` + checkedAttr(includeAssignmentUser) + `> User</label><label><input type="checkbox" name="assignment_system" value="true"` + checkedAttr(includeAssignmentSystem) + `> System</label><label><input type="checkbox" name="assignment_provisional" value="true"` + checkedAttr(includeAssignmentProvisional) + `> Provisional</label><label><input type="checkbox" name="assignment_other" value="true"` + checkedAttr(includeAssignmentOther) + `> Other</label><label>Min faces <input name="min_faces" type="number" min="1" value="` + template.HTMLEscapeString(query.Get("min_faces")) + `"></label><label>Max faces <input name="max_faces" type="number" min="1" value="` + template.HTMLEscapeString(query.Get("max_faces")) + `"></label><span class="people-controls-break"></span><label><strong>Sort</strong> <select name="sort">`)
	for _, option := range []struct{ Value, Label string }{{"name", "Name"}, {"faces", "Faces"}, {"user", "User"}, {"system", "System"}, {"provisional", "Provisional"}, {"references", "References"}, {"clusters", "Clusters"}} {
		body.WriteString(`<option value="` + option.Value + `"` + selectedAttr(option.Value, sortKey) + `>` + option.Label + `</option>`)
	}
	body.WriteString(`</select></label><label><strong>Direction</strong> <select name="direction"><option value="asc"` + selectedAttr("asc", r.URL.Query().Get("direction")) + `>Ascending</option><option value="desc"` + selectedAttr("desc", r.URL.Query().Get("direction")) + `>Descending</option></select></label><label><strong>Per Page</strong> <select name="per_page" onchange="this.form.submit()">`)
	for _, size := range []int{10, 20, 25, 50, 100} {
		body.WriteString(`<option value="` + fmt.Sprint(size) + `"` + selectedAttr(fmt.Sprint(size), fmt.Sprint(perPage)) + `>` + fmt.Sprint(size) + `</option>`)
	}
	body.WriteString(`</select></label><button>Apply</button></form>`)
	renderPager := func() {
		body.WriteString(`<nav class="pager people-page-pager"><a href="` + template.HTMLEscapeString(pageURL(1)) + `">First</a> <a href="` + template.HTMLEscapeString(pageURL(maxInt(1, page-1))) + `">Previous</a> <span>Page ` + fmt.Sprint(page) + `/` + fmt.Sprint(pageCount) + `</span> <a href="` + template.HTMLEscapeString(pageURL(minInt(pageCount, page+1))) + `">Next</a> <a href="` + template.HTMLEscapeString(pageURL(pageCount)) + `">Last</a> <form class="page-jump" method="get"><label>Jump to Page <input type="number" name="page" min="1" value="` + fmt.Sprint(page) + `" onkeydown="if(event.key==='Enter')this.form.submit()"></label><input type="hidden" name="per_page" value="` + fmt.Sprint(perPage) + `"><input type="hidden" name="q" value="` + template.HTMLEscapeString(query.Get("q")) + `"><input type="hidden" name="name" value="` + template.HTMLEscapeString(nameFilter) + `"><input type="hidden" name="collection_id" value="` + template.HTMLEscapeString(collectionID) + `"><input type="hidden" name="view" value="` + template.HTMLEscapeString(view) + `"><input type="hidden" name="include_possibles" value="` + template.HTMLEscapeString(query.Get("include_possibles")) + `"><input type="hidden" name="include_no_faces" value="` + template.HTMLEscapeString(query.Get("include_no_faces")) + `"><input type="hidden" name="include_dont_track" value="` + template.HTMLEscapeString(query.Get("include_dont_track")) + `"><input type="hidden" name="assignment_user" value="` + template.HTMLEscapeString(query.Get("assignment_user")) + `"><input type="hidden" name="assignment_system" value="` + template.HTMLEscapeString(query.Get("assignment_system")) + `"><input type="hidden" name="assignment_provisional" value="` + template.HTMLEscapeString(query.Get("assignment_provisional")) + `"><input type="hidden" name="assignment_other" value="` + template.HTMLEscapeString(query.Get("assignment_other")) + `"><input type="hidden" name="min_faces" value="` + template.HTMLEscapeString(query.Get("min_faces")) + `"><input type="hidden" name="max_faces" value="` + template.HTMLEscapeString(query.Get("max_faces")) + `"><input type="hidden" name="sort" value="` + template.HTMLEscapeString(sortKey) + `"><input type="hidden" name="direction" value="` + template.HTMLEscapeString(query.Get("direction")) + `"></form></nav>`)
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
	body.WriteString(`<hr><form id="create-person" data-csrf="` + template.HTMLEscapeString(csrfCookie(r)) + `"><input name="name" placeholder="New person name" required><button>Create person</button></form><script src="/static/people.js" defer></script>`)
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
		s.render(w, map[string]any{"Title": personName, "Account": account, "Breadcrumb": template.HTML(`<a href="/review/people">People</a>`), "Body": template.HTML(`<h1>` + template.HTMLEscapeString(personName) + `</h1><p>No collections are assigned to this account.</p>`)})
		return
	}
	query := `SELECT f.id, pp.id, pp.relative_path, COALESCE(col.name, ''), COALESCE(a.source_width, pa.width, 0), COALESCE(a.source_height, pa.height, 0),
		f.box_left, f.box_top, f.box_right, f.box_bottom, COALESCE(f.quality, 0), COALESCE(f.detection_confidence, 0),
		CASE WHEN ae.reason = 'cluster-provisional' AND COALESCE(ae.support_count, 0) = 0 AND COALESCE(ae.quality, 0) = 0 AND COALESCE(ae.detection_confidence, 0) = 0 AND ae.agreement IS NULL THEN NULL ELSE ae.similarity END, COALESCE(ca.state, 'unassigned'), COALESCE(ca.person_id, ''), COALESCE(NULLIF(ca_person.name, ''), substr(ca.person_id, -8), ''), COALESCE(cfr.reference, 0)
		FROM faces f JOIN analyses a ON a.id = f.analysis_id
		JOIN photo_assets pa ON pa.id = a.asset_id JOIN photo_paths pp ON pp.asset_id = pa.id
		JOIN collections col ON col.id = pp.collection_id
		JOIN current_face_assignments ca ON ca.face_id = f.id
		LEFT JOIN people ca_person ON ca_person.id = ca.person_id
		LEFT JOIN assignment_events ae ON ae.id = ca.event_id
		LEFT JOIN current_face_references cfr ON cfr.face_id = f.id AND cfr.person_id = ?
		WHERE (ca.person_id = ? OR (ca.state IN ('unknown', 'provisional') AND EXISTS (
			SELECT 1 FROM possible_face_candidates pc_scope
			WHERE pc_scope.face_id = f.id AND pc_scope.person_id = ? AND pc_scope.is_current = 1
		))) AND pp.missing_at IS NULL`
	args := []any{personID, personID, personID}
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
		AssignedPersonName                       string
		Reference                                bool
		Recommended                              bool
		RecommendationPriority                   int
		Representative                           bool
		Cluster                                  *personCluster
		Candidates                               []recognitionCandidateView
		PossibleCandidates                       []possibleCandidateView
		PossibleForPerson                        bool
		PossibleSimilarity                       float64
	}
	var faces []personFace
	seenFaceIDs := make(map[string]struct{})
	referenceCount := 0
	for rows.Next() {
		var face personFace
		var reference, representativeFace int
		var confidence sql.NullFloat64
		if err := rows.Scan(&face.FaceID, &face.PathID, &face.RelativePath, &face.Collection, &face.Width, &face.Height, &face.Left, &face.Top, &face.Right, &face.Bottom, &face.Quality, &face.Detection, &confidence, &face.State, &face.PersonID, &face.AssignedPersonName, &reference); err != nil {
			http.Error(w, "person faces lookup failed", http.StatusInternalServerError)
			return
		}
		// An asset may have multiple path rows, including duplicate paths in
		// one collection. A face belongs to the analysis/asset, not to each
		// path row, so render it only once and never submit its ID twice in a
		// bulk assignment.
		if _, exists := seenFaceIDs[face.FaceID]; exists {
			continue
		}
		seenFaceIDs[face.FaceID] = struct{}{}
		if confidence.Valid {
			face.Confidence, face.ConfidenceAvailable = confidence.Float64, true
		}
		face.Reference = reference == 1
		_ = s.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM person_representatives WHERE person_id = ? AND face_id = ?)`, personID, face.FaceID).Scan(&representativeFace)
		face.Representative = representativeFace == 1
		face.Candidates, err = s.latestRecognitionCandidates(r.Context(), face.FaceID)
		if err != nil {
			http.Error(w, "person candidate lookup failed", http.StatusInternalServerError)
			return
		}
		foundCurrent := false
		for _, candidate := range face.Candidates {
			if candidate.PersonID == face.PersonID {
				foundCurrent = true
				break
			}
		}
		if !foundCurrent && face.PersonID != "" {
			var candidate recognitionCandidateView
			if err := s.db.QueryRowContext(r.Context(), `
				SELECT rc.person_id, COALESCE(NULLIF(p.name, ''), substr(rc.person_id, -8), rc.person_id),
				       rc.similarity, rc.margin, rc.support_count, rc.quality, rc.detection_confidence, rc.evidence
				FROM recognition_candidates rc LEFT JOIN people p ON p.id = rc.person_id
				WHERE rc.face_id = ? AND rc.person_id = ?
				ORDER BY rc.created_at DESC, rc.rank LIMIT 1`, face.FaceID, face.PersonID).
				Scan(&candidate.PersonID, &candidate.Name, &candidate.Similarity, &candidate.Margin, &candidate.Support, &candidate.Quality, &candidate.Detection, &candidate.Evidence); err == nil {
				face.Candidates = append([]recognitionCandidateView{candidate}, face.Candidates[:min(4, len(face.Candidates))]...)
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
		face.PossibleForPerson = possibleFaceForPerson(face.State, face.PersonID, personID, face.PossibleCandidates)
		face.PossibleSimilarity, _ = possibleSimilarityForPerson(face.State, face.PersonID, personID, face.PossibleCandidates)
		faces = append(faces, face)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "person faces lookup failed", http.StatusInternalServerError)
		return
	}
	headerFaces := make([]personFace, 0, len(faces))
	totalPossibleFaceCount := 0
	for _, face := range faces {
		if face.PossibleForPerson {
			totalPossibleFaceCount++
		} else {
			headerFaces = append(headerFaces, face)
		}
	}
	totalFaceCount := len(headerFaces)
	referenceCount = 0
	for _, face := range headerFaces {
		if face.Reference {
			referenceCount++
		}
	}
	hasTrustedFaces := false
	trustedFaceCount := 0
	for _, face := range headerFaces {
		if face.State == string(review.AssignedUser) || face.State == string(review.AssignedSystem) {
			hasTrustedFaces = true
			trustedFaceCount++
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
	if possibleFilter == "possibles" || possibleFilter == "only" || possibleFilter == "without" || possibleFilter == "high" || possibleFilter == "medium" || possibleFilter == "low" || possibleFilter == "very-low" || candidatePersonFilter != "" || evidenceFilter != "" {
		filtered := faces[:0]
		for _, face := range faces {
			matched := true
			if possibleFilter == "possibles" || possibleFilter == "only" || possibleFilter == "true" {
				matched = face.PossibleForPerson
			} else if possibleFilter == "without" || possibleFilter == "false" {
				matched = !face.PossibleForPerson
			} else if face.PossibleForPerson {
				similarity := face.PossibleSimilarity
				switch possibleFilter {
				case "high":
					matched = similarity >= float64(s.recognitionThresholds.HighSimilarity)
				case "medium":
					matched = similarity >= float64(s.recognitionThresholds.MediumSimilarity) && similarity < float64(s.recognitionThresholds.HighSimilarity)
				case "low":
					matched = similarity >= float64(s.recognitionThresholds.LowSimilarity) && similarity < float64(s.recognitionThresholds.MediumSimilarity)
				case "very-low":
					matched = similarity < float64(s.recognitionThresholds.LowSimilarity)
				}
			} else if possibleFilter == "high" || possibleFilter == "medium" || possibleFilter == "low" || possibleFilter == "very-low" {
				matched = false
			}
			if matched && candidatePersonFilter != "" {
				matched = false
				if face.PossibleForPerson {
					for _, candidate := range face.PossibleCandidates {
						if candidate.PersonID == candidatePersonFilter || strings.EqualFold(candidate.PersonName, candidatePersonFilter) {
							matched = true
							break
						}
					}
				}
			}
			if matched && evidenceFilter != "" {
				matched = false
				if face.PossibleForPerson {
					for _, candidate := range face.PossibleCandidates {
						if candidate.Evidence == evidenceFilter {
							matched = true
							break
						}
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
		case "possibles":
			less = faces[i].PossibleSimilarity < faces[j].PossibleSimilarity
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
	clusterSummaryClusters := make(map[string][]string)
	clusterPersonIDs := make(map[string]string)
	clusterSummaryOrder := make([]string, 0)
	for _, face := range headerFaces {
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
			if _, exists := clusterSummaryClusters[face.Cluster.PersonName]; !exists {
				clusterSummaryOrder = append(clusterSummaryOrder, face.Cluster.PersonName)
			}
			seen := false
			for _, clusterID := range clusterSummaryClusters[face.Cluster.PersonName] {
				if clusterID == face.Cluster.ID {
					seen = true
					break
				}
			}
			if !seen {
				clusterSummaryClusters[face.Cluster.PersonName] = append(clusterSummaryClusters[face.Cluster.PersonName], face.Cluster.ID)
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
	clusterSummary := strings.Builder{}
	if len(clusterSummaryOrder) > 0 {
		clusterSummary.WriteString(`<span class="person-clusters">`)
		for index, clusterPerson := range clusterSummaryOrder {
			if index > 0 {
				clusterSummary.WriteString(`, `)
			}
			if clusterPersonIDs[clusterPerson] != "" && clusterPersonIDs[clusterPerson] != personID {
				clusterSummary.WriteString(`<a href="/review/people/` + template.HTMLEscapeString(clusterPersonIDs[clusterPerson]) + `">` + template.HTMLEscapeString(clusterPerson) + `</a>`)
			} else {
				clusterSummary.WriteString(template.HTMLEscapeString(clusterPerson))
			}
			clusterSummary.WriteString(` (`)
			for clusterIndex, clusterID := range clusterSummaryClusters[clusterPerson] {
				if clusterIndex > 0 {
					clusterSummary.WriteString(`, `)
				}
				clusterSummary.WriteString(`<a href="/review/clusters/` + template.HTMLEscapeString(clusterID) + `">` + template.HTMLEscapeString(idSuffix(clusterID)) + `</a>`)
			}
			clusterSummary.WriteString(`)`)
		}
		clusterSummary.WriteString(`</span>`)
	}
	shownFaceCount := 0
	shownPossibleFaceCount := 0
	for _, face := range faces {
		if face.PossibleForPerson {
			shownPossibleFaceCount++
		} else {
			shownFaceCount++
		}
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
.card.provisional{border:2px solid #F8E71C}
.card.assigned{border:2px solid #7ED321}
.card.needs-attention{border:2px solid #D0021B}
.viewer{
  position:relative;
  display:inline-block;
  max-width:100%}
.viewer img{
  max-width:100%;
  max-height:55vh}
.box{
  position:absolute;
  border:2px solid #D0021B;
  background:rgba(208,2,27,.15);
  pointer-events:none}
.box.reference{
  border-color:#50E3C2;
  background:rgba(80,227,194,.15)}
.box.assigned{border-color:#7ED321;background:rgba(126,211,33,.15)}
.box.provisional{border-color:#F8E71C;background:rgba(248,231,28,.15)}
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
.person-identity{display:flex;gap:12px;align-items:stretch}
.person-identity img{flex:0 0 auto;border-radius:8px}
.person-identity-info{display:flex;flex-direction:column;min-width:0}
.person-identity-info h1{margin:0}
.person-heading{display:grid;grid-template-columns:96px minmax(0,1fr);gap:12px;align-items:start}
.person-heading img{flex:0 0 auto;border-radius:8px}
.person-heading h1{margin:0}
.person-name-form{display:flex;align-items:center;flex-wrap:wrap;gap:8px;margin-top:8px}
.person-name-form .dont-track-button{margin-right:4px}
.person-name-form .recommend-button{margin-left:8px}
.person-clusters{margin-top:6px;font-size:.9em}
.person-summary-table{display:flex;flex-wrap:wrap;gap:12px;margin:10px 0 0}
.person-summary-table>div{min-width:7rem}
.person-summary-table dt{font-size:.8em;color:#555}
.person-summary-table dd{margin:2px 0 0;font-weight:600}
.person-summary-table .person-clusters{margin:0;font-size:inherit}
.ref-rec{float:right}
.person-footer-note{margin-top:1rem;font-size:.9em;color:#555}
.person-reference-warning{display:inline-block;margin-left:.5rem;padding:.35rem .6rem;border:1px solid #c62828;background:#fff1f1;color:#b71c1c;font-weight:600}
.person-select{min-width:10rem}
.person-select-filter{min-width:10rem}
.person-page-controls{display:flex;align-items:center;flex-wrap:wrap;gap:6px;margin:8px 0}
.person-controls-break{flex-basis:100%;height:0}
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
		peopleQuery += ` WHERE (TRIM(COALESCE(people.name, '')) <> '' OR EXISTS (SELECT 1 FROM current_face_assignments ca2 JOIN faces f2 ON f2.id = ca2.face_id JOIN analyses a2 ON a2.id = f2.analysis_id JOIN photo_assets pa2 ON pa2.id = a2.asset_id JOIN photo_paths pp2 ON pp2.asset_id = pa2.id WHERE ca2.person_id = people.id AND pp2.collection_id IN (` + strings.Join(placeholders, ",") + `)))`
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
	if account.Role == auth.SystemAdmin {
		if hasTrustedFaces {
			recommendButton = `<button type="button" class="recommend-button" data-action="recommend-references">Recommend References</button>`
		}
	}
	reprocessButton := ""
	if account.Role == auth.SystemAdmin && referenceCount > s.recognitionThresholds.MediumSupport {
		reprocessButton = `<button type="button" class="reprocess-button" data-action="reprocess-references">Reprocess</button>`
	}
	referenceWarning := ""
	if trustedFaceCount > 5 && referenceCount < s.recognitionThresholds.HighSupport {
		referenceWarning = `<span class="person-reference-warning">Need ` + strconv.Itoa(s.recognitionThresholds.HighSupport-referenceCount) + ` more references</span>`
	}
	dontTrackLabel := "Don't Track"
	if personDontTrack != 0 {
		dontTrackLabel = "Track"
	}
	dontTrackButton := ""
	if canModify {
		dontTrackButton = `<button type="button" class="dont-track-button" data-action="person-dont-track" data-dont-track="` + fmt.Sprint(personDontTrack == 0) + `">` + dontTrackLabel + `</button>`
	}
	clusterSummaryText := clusterSummary.String()
	if clusterSummaryText == "" {
		clusterSummaryText = "—"
	}
	body.WriteString(`<div id="person-page" data-person-id="` + template.HTMLEscapeString(personID) + `" data-person-name="` + template.HTMLEscapeString(personName) + `" data-csrf="` + template.HTMLEscapeString(csrfCookie(r)) + `" class="person-header"><div class="person-heading"><img loading="lazy" width="96" height="96" alt="Representative image" src="/api/people/` + template.HTMLEscapeString(personID) + `/representative"><div><h1>` + template.HTMLEscapeString(personName) + `</h1><dl class="person-summary-table"><div><dt>Faces</dt><dd>` + fmt.Sprint(shownFaceCount) + ` / ` + fmt.Sprint(totalFaceCount) + `</dd></div><div><dt>Possibles</dt><dd>` + fmt.Sprint(shownPossibleFaceCount) + ` / ` + fmt.Sprint(totalPossibleFaceCount) + `</dd></div><div><dt>References</dt><dd>` + fmt.Sprint(referenceCount) + `</dd></div><div><dt>Clusters</dt><dd>` + clusterSummaryText + `</dd></div><div><dt>Similarity</dt><dd>` + minConfidenceText + ` - ` + maxConfidenceText + `</dd></div><div><dt>Cluster Min/Max</dt><dd>` + minConsistencyText + ` - ` + maxConsistencyText + `</dd></div></dl></div></div><form id="rename-person" class="person-name-form">` + dontTrackButton + `<input name="name" value="` + template.HTMLEscapeString(personName) + `" required><button>Rename</button>` + recommendButton + reprocessButton + referenceWarning + `</form></div>`)
	body.WriteString(`<form method="get" class="person-page-controls"><label><strong>Filter:</strong> <select name="state"><option value="">All faces</option>`)
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
	body.WriteString(`</select></label> <label>Possible <select name="possible"><option value="without"` + selectedAttr("without", possibleFilter) + `>With Out</option><option value="high"` + selectedAttr("high", possibleFilter) + `>High</option><option value="medium"` + selectedAttr("medium", possibleFilter) + `>Medium</option><option value="low"` + selectedAttr("low", possibleFilter) + `>Low</option><option value="very-low"` + selectedAttr("very-low", possibleFilter) + `>Very Low</option><option value="only"` + selectedAttr("only", possibleFilter) + `>Only</option><option value="all"` + selectedAttr("all", possibleFilter) + `>All</option></select></label> <label>Candidate <input name="candidate_person" value="` + template.HTMLEscapeString(candidatePersonFilter) + `" placeholder="name or ID"></label> <label>Evidence <select name="evidence"><option value="">All</option>`)
	for _, evidence := range []string{"high", "medium", "low", "manual-review"} {
		body.WriteString(`<option value="` + evidence + `"` + selectedAttr(evidence, evidenceFilter) + `>` + evidence + `</option>`)
	}
	body.WriteString(`</select></label><span class="person-controls-break"></span><label><strong>Sort:</strong> <select name="sort">`)
	for _, option := range []struct{ Value, Label string }{
		{Value: "path", Label: "Path"},
		{Value: "collection", Label: "Collection"},
		{Value: "cluster", Label: "Cluster"},
		{Value: "consistency", Label: "Consistency"},
		{Value: "confidence", Label: "Confidence"},
		{Value: "quality", Label: "Quality"},
		{Value: "detection", Label: "Detection"},
		{Value: "possibles", Label: "Possibles"},
		{Value: "ref_recs", Label: "Ref Recs"},
	} {
		body.WriteString(`<option value="` + option.Value + `"` + selectedAttr(option.Value, faceSort) + `>` + option.Label + `</option>`)
	}
	body.WriteString(`</select></label> <select name="direction"><option value="asc"` + selectedAttr("asc", r.URL.Query().Get("direction")) + `>Ascending</option><option value="desc"` + selectedAttr("desc", r.URL.Query().Get("direction")) + `>Descending</option></select> <button>Apply</button></form>`)
	body.WriteString(`</div>`)
	body.WriteString(`<div id="photos" class="grid">`)
	for _, face := range faces {
		boxClass := "box " + faceStatusColorClass(face.State, face.Reference)
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
		assignment := template.HTMLEscapeString(assignmentLabel(face.State))
		if face.PossibleForPerson && face.PersonID != "" && face.PersonID != personID {
			assignment += ` (<a href="/review/people/` + template.HTMLEscapeString(face.PersonID) + `">` + template.HTMLEscapeString(face.AssignedPersonName) + `</a>)`
		}
		body.WriteString(`<div class="person-metadata"><strong>` + assignment + `:</strong> <strong>C:</strong> ` + confidence + ` Q: ` + formatScore(face.Quality) + `% D: ` + formatScore(face.Detection) + `%`)
		if face.Recommended {
			body.WriteString(`<span class="ref-rec"><strong>Ref Rec #` + fmt.Sprint(face.RecommendationPriority) + `</strong></span>`)
		}
		body.WriteString(`</div>`)
		body.WriteString(renderRecognitionCandidates(face.Candidates, face.FaceID, face.PersonID, canModify))
		if face.PossibleForPerson {
			body.WriteString(renderPossibleMatches(face.PossibleCandidates, face.FaceID, face.PersonID, canModify))
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
	s.render(w, map[string]any{"Title": personName, "Account": account, "Breadcrumb": template.HTML(`<a href="/review/people">People</a>`), "PageStyle": pageStyle, "Body": template.HTML(body.String())})
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
	referenceQuery := `SELECT rr.face_id, rr.is_current, COALESCE(rr.recommendation_score, 0), pp.id, pp.relative_path, COALESCE(a.source_width, pa.width, 0), COALESCE(a.source_height, pa.height, 0), f.box_left, f.box_top, f.box_right, f.box_bottom, COALESCE(f.quality, 0), COALESCE(f.detection_confidence, 0), COALESCE(cfr.reference, 0) FROM reference_recommendations rr JOIN faces f ON f.id = rr.face_id JOIN analyses a ON a.id = f.analysis_id JOIN photo_assets pa ON pa.id = a.asset_id JOIN photo_paths pp ON pp.asset_id = pa.id LEFT JOIN current_face_references cfr ON cfr.face_id = rr.face_id AND cfr.person_id = rr.person_id WHERE rr.person_id = ?`
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
    alert(result.queued
      ? 'Reference recommendation job queued for ' + page.dataset.personName + ': ' + (result.job_id || result.status)
      : 'Reference recommendations were not queued for ' + page.dataset.personName + ': ' + (result.status || 'unknown reason'));
  };
  const reprocessButton = document.querySelector('[data-action="reprocess-references"]');
  if (reprocessButton) reprocessButton.onclick = async () => {
    const response = await fetch('/api/people/' + encodeURIComponent(page.dataset.personId) + '/reference-reprocess', {
      method: 'POST', headers: {'X-CSRF-Token': token}
    });
    if (!response.ok) { alert(await response.text()); return; }
    const result = await response.json();
    alert(result.queued
      ? 'Reference reprocess job queued for ' + page.dataset.personName + ': ' + (result.job_id || result.status)
      : 'Reference reprocess was not queued for ' + page.dataset.personName + ': ' + (result.status || 'unknown reason'));
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

func (s *Server) peopleScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	_, _ = w.Write([]byte(`(function () {
  const form = document.getElementById('create-person');
  if (!form) return;
  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    const response = await fetch('/api/people', {
      method: 'POST',
      headers: {'Content-Type': 'application/json', 'X-CSRF-Token': form.dataset.csrf || ''},
      body: JSON.stringify({name: form.name.value})
    });
    if (!response.ok) { alert(await response.text()); return; }
    location.reload();
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
  document.querySelectorAll('[data-person-filter]').forEach((input) => {
    const select = document.getElementById(input.dataset.personFilter); if (!select) return;
    const options = Array.from(select.options).map((option) => option.cloneNode(true));
    const filter = () => {
      const query = input.value.trim().toLowerCase();
      const selected = select.value;
      const matches = options.filter((option) => {
        const name = (option.dataset.personName || option.textContent || '').toLowerCase();
        return !query || name.includes(query);
      });
      select.replaceChildren(...matches.map((option) => option.cloneNode(true)));
      if (matches.some((option) => option.value === selected)) select.value = selected;
    };
    input.addEventListener('input', filter); filter();
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
	clusterQuery := `SELECT c.id, c.cluster_key, c.purity, c.connectivity, c.is_mixed, c.review_status, COUNT(DISTINCT cm.face_id), COUNT(DISTINCT rc.person_id), COALESCE(c.proposed_person_id, ''), COALESCE(NULLIF(p.name, ''), substr(c.proposed_person_id, -8), '') FROM clusters c JOIN clustering_runs cr ON cr.id = c.clustering_run_id LEFT JOIN cluster_memberships cm ON cm.cluster_id = c.id AND NOT EXISTS (SELECT 1 FROM current_face_dispositions fd0 WHERE fd0.face_id = cm.face_id AND fd0.disposition = 'false-detection') LEFT JOIN recognition_candidates rc ON rc.face_id = cm.face_id AND rc.created_at = (SELECT MAX(rc2.created_at) FROM recognition_candidates rc2 WHERE rc2.face_id = rc.face_id) LEFT JOIN people p ON p.id = c.proposed_person_id WHERE cr.status = 'active'`
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
	var runStatus, supersededBy, reviewStatus, collectionID, proposedPersonID, proposedPersonName string
	var reviewVersion, mixed, membershipCount int
	var purityValue, consistencyValue, connectivityValue sql.NullFloat64
	err := s.db.QueryRowContext(r.Context(), `
		SELECT cr.status, COALESCE(cr.superseded_by, ''), c.review_status, c.review_version, c.is_mixed,
		       c.purity, c.consistency, c.connectivity,
		       COALESCE(c.proposed_person_id, ''), COALESCE(NULLIF(p.name, ''), substr(c.proposed_person_id, -8), ''),
		       (SELECT COUNT(*) FROM cluster_memberships cm2 WHERE cm2.cluster_id = c.id),
		       (SELECT pp.collection_id FROM cluster_memberships cm
				JOIN faces f ON f.id = cm.face_id JOIN analyses a ON a.id = f.analysis_id
				JOIN photo_assets pa ON pa.id = a.asset_id JOIN photo_paths pp ON pp.asset_id = pa.id
		WHERE cm.cluster_id = c.id LIMIT 1)
		FROM clusters c JOIN clustering_runs cr ON cr.id = c.clustering_run_id LEFT JOIN people p ON p.id = c.proposed_person_id WHERE c.id = ?`, clusterID).
		Scan(&runStatus, &supersededBy, &reviewStatus, &reviewVersion, &mixed, &purityValue, &consistencyValue, &connectivityValue, &proposedPersonID, &proposedPersonName, &membershipCount, &collectionID)
	if err == sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		http.Error(w, "cluster lookup failed", http.StatusInternalServerError)
		return
	}
	accessibleCollections, err := s.auth.AccessibleCollections(r.Context(), account.ID)
	if err != nil {
		http.Error(w, "permission lookup failed", http.StatusInternalServerError)
		return
	}
	if accessibleCollections != nil && len(accessibleCollections) == 0 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if accessibleCollections == nil {
		allowed, permissionErr := s.auth.CanAccessCollection(r.Context(), account.ID, collectionID, false, false)
		if permissionErr != nil || !allowed {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}
	visibleFaceCount := membershipCount
	visibleCandidateCount := 0
	visibleToAssignCount := 0
	countQuery := `SELECT COUNT(DISTINCT cm.face_id), COUNT(DISTINCT CASE WHEN rc.similarity >= 0.015 THEN cm.face_id END), COUNT(DISTINCT CASE WHEN COALESCE(ca.state, 'unassigned') NOT IN ('assigned-user', 'assigned-system') THEN cm.face_id END) FROM cluster_memberships cm JOIN faces cf ON cf.id = cm.face_id JOIN analyses caa ON caa.id = cf.analysis_id JOIN photo_assets cpa ON cpa.id = caa.asset_id JOIN photo_paths cpp ON cpp.asset_id = cpa.id LEFT JOIN current_face_assignments ca ON ca.face_id = cm.face_id LEFT JOIN recognition_candidates rc ON rc.face_id = cm.face_id AND rc.created_at = (SELECT MAX(rc2.created_at) FROM recognition_candidates rc2 WHERE rc2.face_id = cm.face_id) WHERE cm.cluster_id = ? AND cpp.missing_at IS NULL`
	countArgs := []any{clusterID}
	if accessibleCollections != nil {
		countQuery += ` AND cpp.collection_id IN (` + placeholders(len(accessibleCollections)) + `)`
		for _, id := range accessibleCollections {
			countArgs = append(countArgs, id)
		}
	}
	if err := s.db.QueryRowContext(r.Context(), countQuery, countArgs...).Scan(&visibleFaceCount, &visibleCandidateCount, &visibleToAssignCount); err != nil {
		http.Error(w, "cluster face count failed", http.StatusInternalServerError)
		return
	}

	representedPeople := make(map[string]int)
	representedQuery := `SELECT ca.person_id, COUNT(*) FROM cluster_memberships cm JOIN current_face_assignments ca ON ca.face_id = cm.face_id JOIN faces f ON f.id = cm.face_id JOIN analyses a ON a.id = f.analysis_id JOIN photo_assets pa ON pa.id = a.asset_id JOIN photo_paths pp ON pp.asset_id = pa.id WHERE cm.cluster_id = ? AND ca.state IN ('assigned-user', 'assigned-system') AND pp.missing_at IS NULL`
	representedArgs := []any{clusterID}
	if accessibleCollections != nil {
		representedQuery += ` AND pp.collection_id IN (` + placeholders(len(accessibleCollections)) + `)`
		for _, id := range accessibleCollections {
			representedArgs = append(representedArgs, id)
		}
	}
	representedQuery += ` GROUP BY ca.person_id`
	representedRows, err := s.db.QueryContext(r.Context(), representedQuery, representedArgs...)
	if err != nil {
		http.Error(w, "cluster people lookup failed", http.StatusInternalServerError)
		return
	}
	for representedRows.Next() {
		var personID string
		var count int
		if err := representedRows.Scan(&personID, &count); err != nil {
			representedRows.Close()
			http.Error(w, "cluster people lookup failed", http.StatusInternalServerError)
			return
		}
		representedPeople[personID] = count
	}
	if err := representedRows.Err(); err != nil {
		representedRows.Close()
		http.Error(w, "cluster people lookup failed", http.StatusInternalServerError)
		return
	}
	representedRows.Close()
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
	sort.SliceStable(people, func(i, j int) bool {
		leftProposed := people[i].ID == proposedPersonID
		rightProposed := people[j].ID == proposedPersonID
		if leftProposed != rightProposed {
			return leftProposed
		}
		leftCount, leftShown := representedPeople[people[i].ID]
		rightCount, rightShown := representedPeople[people[j].ID]
		if leftShown != rightShown {
			return leftShown
		}
		if leftShown && leftCount != rightCount {
			return leftCount > rightCount
		}
		leftName, rightName := strings.ToLower(people[i].Name), strings.ToLower(people[j].Name)
		if leftName != rightName {
			return leftName < rightName
		}
		return people[i].ID < people[j].ID
	})
	defaultPersonID := proposedPersonID
	faceQuery := `
		SELECT cm.face_id, COALESCE(ca.state, 'unassigned'), COALESCE(ca.person_id, ''),
		       COALESCE((SELECT disposition FROM current_face_dispositions WHERE face_id = cm.face_id), ''),
		       COALESCE(pp.id, ''), COALESCE(pp.relative_path, ''), COALESCE(a.source_width, pa.width, 0), COALESCE(a.source_height, pa.height, 0),
		       f.box_left, f.box_top, f.box_right, f.box_bottom,
		       COALESCE(f.quality, 0), COALESCE(f.detection_confidence, 0)
		FROM cluster_memberships cm
		JOIN faces f ON f.id = cm.face_id JOIN analyses a ON a.id = f.analysis_id
		JOIN photo_assets pa ON pa.id = a.asset_id
		LEFT JOIN current_face_assignments ca ON ca.face_id = cm.face_id
		LEFT JOIN assignment_events ae ON ae.id = ca.event_id
		JOIN photo_paths pp ON pp.asset_id = pa.id
		WHERE cm.cluster_id = ? AND pp.missing_at IS NULL`
	faceArgs := []any{clusterID}
	if accessibleCollections != nil {
		faceQuery += ` AND pp.collection_id IN (` + placeholders(len(accessibleCollections)) + `)`
		for _, id := range accessibleCollections {
			faceArgs = append(faceArgs, id)
		}
	}
	detailQuery := r.URL.Query()
	if value := detailQuery.Get("assignment_state"); value != "" {
		if value == "to-assign" {
			faceQuery += ` AND COALESCE(ca.state, 'unassigned') NOT IN ('assigned-user', 'assigned-system')`
		} else {
			faceQuery += ` AND COALESCE(ca.state, 'unassigned') = ?`
			faceArgs = append(faceArgs, value)
		}
	}
	if value := strings.TrimSpace(detailQuery.Get("assigned_person")); value != "" {
		faceQuery += ` AND (LOWER(COALESCE(ca.person_id, '')) LIKE ? OR EXISTS (SELECT 1 FROM people fp WHERE fp.id = ca.person_id AND LOWER(COALESCE(fp.name, '')) LIKE ?))`
		pattern := "%" + strings.ToLower(value) + "%"
		faceArgs = append(faceArgs, pattern, pattern)
	}
	if value := detailQuery.Get("disposition"); value != "" {
		if value == "none" {
			faceQuery += ` AND NOT EXISTS (SELECT 1 FROM current_face_dispositions fdx WHERE fdx.face_id = cm.face_id)`
		} else {
			faceQuery += ` AND EXISTS (SELECT 1 FROM current_face_dispositions fdx WHERE fdx.face_id = cm.face_id AND fdx.disposition = ?)`
			faceArgs = append(faceArgs, value)
		}
	}
	if value := detailQuery.Get("min_quality"); value != "" {
		if score, parseErr := strconv.ParseFloat(value, 64); parseErr == nil {
			faceQuery += ` AND f.quality >= ?`
			faceArgs = append(faceArgs, score/100)
		}
	}
	if value := detailQuery.Get("max_quality"); value != "" {
		if score, parseErr := strconv.ParseFloat(value, 64); parseErr == nil {
			faceQuery += ` AND f.quality <= ?`
			faceArgs = append(faceArgs, score/100)
		}
	}
	if value := detailQuery.Get("min_detection"); value != "" {
		if score, parseErr := strconv.ParseFloat(value, 64); parseErr == nil {
			faceQuery += ` AND f.detection_confidence >= ?`
			faceArgs = append(faceArgs, score/100)
		}
	}
	if value := detailQuery.Get("max_detection"); value != "" {
		if score, parseErr := strconv.ParseFloat(value, 64); parseErr == nil {
			faceQuery += ` AND f.detection_confidence <= ?`
			faceArgs = append(faceArgs, score/100)
		}
	}
	if value := detailQuery.Get("candidate_presence"); value == "with" || value == "without" {
		candidateExists := `EXISTS (SELECT 1 FROM recognition_candidates rcf WHERE rcf.face_id = cm.face_id AND rcf.created_at = (SELECT MAX(rcf2.created_at) FROM recognition_candidates rcf2 WHERE rcf2.face_id = cm.face_id))`
		if value == "without" {
			candidateExists = "NOT " + candidateExists
		}
		faceQuery += ` AND ` + candidateExists
	}
	if value := detailQuery.Get("possible_presence"); value == "with" || value == "without" {
		possibleExists := `EXISTS (SELECT 1 FROM possible_face_candidates pcf WHERE pcf.face_id = cm.face_id AND pcf.is_current = 1)`
		if value == "without" {
			possibleExists = "NOT " + possibleExists
		}
		faceQuery += ` AND ` + possibleExists
	}
	orderBy := map[string]string{"path": "pp.relative_path", "person": "LOWER(COALESCE(ca.person_id, ''))", "assignment_state": "COALESCE(ca.state, 'unassigned')", "disposition": "COALESCE((SELECT disposition FROM current_face_dispositions WHERE face_id = cm.face_id), '')", "consistency": "(SELECT consistency FROM clusters WHERE id = cm.cluster_id)", "quality": "f.quality", "detection": "f.detection_confidence", "confidence": "COALESCE(ae.similarity, 0)"}[detailQuery.Get("sort")]
	if orderBy == "" {
		orderBy = "pp.relative_path"
	}
	direction := "ASC"
	if strings.EqualFold(detailQuery.Get("direction"), "desc") {
		direction = "DESC"
	}
	faceQuery += ` ORDER BY ` + orderBy + ` ` + direction + `, cm.face_id`
	rows, err := s.db.QueryContext(r.Context(), faceQuery, faceArgs...)
	if err != nil {
		http.Error(w, "cluster lookup failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var body strings.Builder
	canModify := account.Role != auth.Viewer && runStatus == "active"
	disabledAttr := ""
	if !canModify {
		disabledAttr = ` disabled`
	}
	pageStyle := template.CSS(`#cluster-page{max-width:100%}.cluster-header{display:grid;grid-template-columns:96px minmax(0,1fr);column-gap:.5rem;position:sticky;top:0;z-index:10;background:#fff;padding:4px 0}.cluster-heading{display:contents}.cluster-heading h1{grid-column:2;grid-row:1;margin:.15rem 0}.cluster-heading img{grid-column:1;grid-row:1 / span 2;width:96px;height:96px;object-fit:cover;border-radius:6px}.grid{display:flex;flex-wrap:wrap;gap:18px}.card{width:min(420px,45vw);border:1px solid #ccc;padding:10px}.card.provisional{border:2px solid #d6a800}.card.assigned{border:2px solid #0a2}.card.needs-attention{border:2px solid #c62828}.viewer{position:relative;display:inline-block;max-width:100%}.viewer img{max-width:100%;max-height:55vh}.box{position:absolute;border:2px solid #f00;pointer-events:none}.path{margin-top:8px;overflow-wrap:anywhere}.card select{max-width:100%}.person-candidates{font-size:.9em;line-height:1.35;margin-top:6px}.person-page-actions{display:flex;justify-content:flex-start;align-items:center;flex-wrap:wrap;gap:.75rem;width:max-content;max-width:100%;margin-top:18px;padding:10px 0}.cluster-card-actions{margin-top:8px}.cluster-dispositions{display:flex;flex-wrap:wrap;gap:6px}.person-assignment{display:flex;justify-content:flex-end;gap:6px;margin-top:8px}.person-assignment select{min-width:10rem}.person-select-filter{min-width:10rem}.cluster-face{border-width:4px}.cluster-alert{border:1px solid #c62828;background:#fff1f1;padding:.75rem;margin:.75rem 0}.cluster-summary{grid-column:2;grid-row:2;display:flex;flex-wrap:wrap;gap:.35rem}.cluster-summary div{min-width:6rem}.cluster-summary dt{font-size:.8rem;color:#666}.cluster-summary dd{margin:.15rem 0 0;font-weight:600}@media(max-width:600px){.card{width:100%}}`)
	candidateSummary := strconv.Itoa(visibleCandidateCount)
	if visibleToAssignCount == 0 {
		candidateSummary = "N/A"
	}
	body.WriteString(`<div id="cluster-page" data-cluster-id="` + template.HTMLEscapeString(clusterID) + `" data-csrf="` + template.HTMLEscapeString(csrfCookie(r)) + `" data-version="` + fmt.Sprint(reviewVersion) + `"><header class="cluster-header"><div class="cluster-heading">`)
	if proposedPersonID != "" {
		body.WriteString(`<img alt="" src="/api/people/` + template.HTMLEscapeString(proposedPersonID) + `/representative">`)
	}
	body.WriteString(`<h1>Cluster ` + template.HTMLEscapeString(idSuffix(clusterID)) + ` <small>(` + template.HTMLEscapeString(clusterID) + `)</small></h1></div><dl class="cluster-summary"><div><dt>Proposed person</dt><dd>`)
	if proposedPersonID == "" {
		body.WriteString(`Unassigned`)
	} else {
		body.WriteString(`<a href="/review/people/` + template.HTMLEscapeString(proposedPersonID) + `">` + template.HTMLEscapeString(proposedPersonName) + `</a>`)
	}
	body.WriteString(`</dd></div><div><dt>Faces</dt><dd>` + fmt.Sprint(visibleFaceCount) + `</dd></div><div><dt>Candidates</dt><dd>` + candidateSummary + `</dd></div><div><dt>Review version</dt><dd>` + fmt.Sprint(reviewVersion) + `</dd></div><div><dt>Review</dt><dd>` + template.HTMLEscapeString(reviewStatus) + `</dd></div><div><dt>Cluster</dt><dd>` + template.HTMLEscapeString(humanRunStatus(runStatus)) + `</dd></div><div><dt>Purity</dt><dd>` + formatScoreUnavailable(purityValue.Float64, purityValue.Valid) + `</dd></div><div><dt>Consistency</dt><dd>` + formatScoreUnavailable(consistencyValue.Float64, consistencyValue.Valid) + `</dd></div><div><dt>Connectivity</dt><dd>` + formatScoreUnavailable(connectivityValue.Float64, connectivityValue.Valid) + `</dd></div><div><dt>Mixed</dt><dd>`)
	if mixed != 0 {
		body.WriteString(`X`)
	}
	body.WriteString(`</dd></div></dl></header>`)
	if mixed != 0 {
		body.WriteString(`<div class="cluster-alert">This cluster is mixed: review each face explicitly. A default person cannot bulk-confirm it.</div>`)
	}
	if runStatus != "active" {
		body.WriteString(`<div class="cluster-alert">This is a historical clustering run. It is read-only`)
		if supersededBy != "" {
			body.WriteString(` (` + template.HTMLEscapeString(idSuffix(supersededBy)) + `)`)
		}
		body.WriteString(`.</div>`)
	}
	body.WriteString(`<div class="person-page-actions cluster-controls"><label>Default person <select id="default-person"` + disabledAttr)
	if mixed != 0 {
		body.WriteString(` disabled`)
	}
	if proposedPersonID != "" {
		body.WriteString(`><option value="` + template.HTMLEscapeString(proposedPersonID) + `" selected>` + template.HTMLEscapeString(proposedPersonName) + `</option>`)
	} else {
		body.WriteString(`>`)
	}
	for _, person := range people {
		if person.ID == proposedPersonID {
			continue
		}
		body.WriteString(`<option value="` + template.HTMLEscapeString(person.ID) + `"` + selectedAttr(person.ID, defaultPersonID) + `>` + template.HTMLEscapeString(person.Name) + `</option>`)
	}
	body.WriteString(`<option value="">No default</option></select></label> <label>Status <select id="cluster-status"` + disabledAttr + `>` + clusterOption("unreviewed", "Unreviewed", reviewStatus) + clusterOption("in-progress", "In progress", reviewStatus) + clusterOption("partially-reviewed", "Partially reviewed", reviewStatus) + clusterOption("reviewed", "Reviewed", reviewStatus) + clusterOption("needs-review", "Needs review", reviewStatus) + `</select></label> <label>Note <input id="cluster-note" size="45"` + disabledAttr + `></label> <button id="save-cluster" type="button"` + disabledAttr + `>Save review</button></div>`)
	body.WriteString(`<form id="cluster-detail-controls" method="get"><div><strong>Filter:</strong> <label>Assignment <select name="assignment_state"><option value="">Any</option>`)
	body.WriteString(clusterOption("to-assign", "To Assign", detailQuery.Get("assignment_state")))
	for _, value := range []string{"unassigned", "unknown", "ambiguous", "provisional", "assigned-system", "assigned-user"} {
		body.WriteString(clusterOption(value, assignmentLabel(value), detailQuery.Get("assignment_state")))
	}
	body.WriteString(`</select></label><label>Person <input name="assigned_person" value="` + template.HTMLEscapeString(detailQuery.Get("assigned_person")) + `"></label><label>Disposition <select name="disposition">` + clusterOption("", "Any", detailQuery.Get("disposition")) + clusterOption("none", "None", detailQuery.Get("disposition")) + clusterOption("dont-track", "Don’t track", detailQuery.Get("disposition")) + clusterOption("false-detection", "False detection", detailQuery.Get("disposition")) + clusterOption("incorrect-assignment", "Incorrect assignment", detailQuery.Get("disposition")) + `</select></label><label>Candidates <select name="candidate_presence">` + clusterOption("", "Any", detailQuery.Get("candidate_presence")) + clusterOption("with", "With candidates", detailQuery.Get("candidate_presence")) + clusterOption("without", "Without candidates", detailQuery.Get("candidate_presence")) + `</select></label><label>Possible <select name="possible_presence">` + clusterOption("", "Any", detailQuery.Get("possible_presence")) + clusterOption("with", "With possibles", detailQuery.Get("possible_presence")) + clusterOption("without", "Without possibles", detailQuery.Get("possible_presence")) + `</select></label>`)
	for _, field := range []string{"min_quality", "max_quality", "min_detection", "max_detection"} {
		body.WriteString(`<label>` + strings.Title(strings.ReplaceAll(field, "_", " ")) + ` <input type="number" name="` + field + `" min="0" max="100" step="0.1" value="` + template.HTMLEscapeString(detailQuery.Get(field)) + `"></label>`)
	}
	body.WriteString(`</div><div><label><strong>Sort:</strong> <select name="sort">`)
	for _, option := range []struct{ value, label string }{{"path", "Face/path"}, {"person", "Person"}, {"assignment_state", "Assignment state"}, {"disposition", "Disposition"}, {"consistency", "Consistency"}, {"quality", "Quality"}, {"detection", "Detection"}, {"confidence", "Candidate confidence"}} {
		body.WriteString(clusterOption(option.value, option.label, detailQuery.Get("sort")))
	}
	detailDirection := "asc"
	if strings.EqualFold(detailQuery.Get("direction"), "desc") {
		detailDirection = "desc"
	}
	body.WriteString(`</select></label><label>Direction <select name="direction">` + clusterOption("asc", "Ascending", detailDirection) + clusterOption("desc", "Descending", detailDirection) + `</select></label><button>Apply</button></div></form>`)
	body.WriteString(`<p id="cluster-message"></p><div class="grid cluster-grid">`)
	renderedFaces := 0
	for rows.Next() {
		var faceID, state, personID, disposition, pathID, relativePath string
		var width, height int
		var left, top, right, bottom, quality, detection float64
		if err := rows.Scan(&faceID, &state, &personID, &disposition, &pathID, &relativePath, &width, &height, &left, &top, &right, &bottom, &quality, &detection); err != nil {
			http.Error(w, "cluster lookup failed", http.StatusInternalServerError)
			return
		}
		renderedFaces++
		assignmentType := template.HTMLEscapeString(assignmentLabel(state))
		if personID != "" && personID != proposedPersonID {
			assignedPersonName := personID
			for _, person := range people {
				if person.ID == personID {
					assignedPersonName = person.Name
					break
				}
			}
			assignmentType += ` (<a href="/review/people/` + template.HTMLEscapeString(personID) + `">` + template.HTMLEscapeString(assignedPersonName) + `</a>)`
		}
		dispositionActive := disposition != ""
		body.WriteString(`<article class="` + template.HTMLEscapeString("card cluster-face "+func() string {
			if state == string(review.AssignedUser) || state == string(review.AssignedSystem) {
				if state == string(review.AssignedUser) {
					return "assigned assigned-user"
				}
				return "assigned"
			}
			if state == string(review.Provisional) {
				return "provisional"
			}
			return "needs-attention"
		}()) + `" data-face-id="` + template.HTMLEscapeString(faceID) + `" data-original-state="` + template.HTMLEscapeString(state) + `" data-original-person="` + template.HTMLEscapeString(personID) + `" data-original-disposition="` + template.HTMLEscapeString(disposition) + `" data-disposition-active="` + strconv.FormatBool(dispositionActive) + `" data-pending-state="` + template.HTMLEscapeString(state) + `"><div class="viewer photo-frame"><img src="/api/photos/` + template.HTMLEscapeString(pathID) + `" alt="face ` + template.HTMLEscapeString(idSuffix(faceID)) + `"><span class="box face-box" style="left:` + percent(left, width) + `%;top:` + percent(top, height) + `%;width:` + percent(right-left, width) + `%;height:` + percent(bottom-top, height) + `%"></span></div><div class="path"><a href="/review/photos?path_id=` + template.HTMLEscapeString(pathID) + `">` + template.HTMLEscapeString(relativePath) + `</a></div><div class="person-metadata"><strong>` + assignmentType + `:</strong> Cs: ` + formatScoreUnavailable(consistencyValue.Float64, consistencyValue.Valid) + ` Cn: ` + formatScoreUnavailable(connectivityValue.Float64, connectivityValue.Valid) + ` Q: ` + formatScore(quality) + `% D: ` + formatScore(detection) + `%</div><div class="cluster-card-actions"><div class="cluster-dispositions"><button type="button" data-action="unknown"` + disabledAttr + `>Unknown</button><button type="button" data-action="ambiguous"` + disabledAttr + `>Ambiguous</button><button type="button" data-action="disposition" data-disposition="incorrect-assignment"` + disabledAttr + `>` + dispositionButtonLabel(disposition, "incorrect-assignment", "Incorrect", "Correct") + `</button><button type="button" data-action="disposition" data-disposition="false-detection"` + disabledAttr + `>` + dispositionButtonLabel(disposition, "false-detection", "No Face", "Has Face") + `</button><button type="button" data-action="disposition" data-disposition="dont-track"` + disabledAttr + `>` + dispositionButtonLabel(disposition, "dont-track", "Don't Track", "Track") + `</button></div>`)
		selectedName := ""
		for _, person := range people {
			if person.ID == personID {
				selectedName = person.Name
				break
			}
		}
		body.WriteString(`<div class="person-assignment"><input class="person-select-filter" data-person-filter="assignment-options-` + template.HTMLEscapeString(faceID) + `" value="` + template.HTMLEscapeString(selectedName) + `" placeholder="Type to filter people"><select class="person-select face-person" id="assignment-options-` + template.HTMLEscapeString(faceID) + `" data-assignment-person="` + template.HTMLEscapeString(faceID) + `"` + disabledAttr + `><option value="">Unknown</option><option value="__ambiguous__">Ambiguous</option>`)
		for _, person := range people {
			selected := selectedAttr(person.ID, personID)
			body.WriteString(`<option value="` + template.HTMLEscapeString(person.ID) + `" data-person-name="` + template.HTMLEscapeString(person.Name) + `"` + selected + `>` + template.HTMLEscapeString(person.Name) + `</option>`)
		}
		body.WriteString(`</select><button type="button" data-action="assign"` + disabledAttr + `>Save</button></div></div><input type="hidden" class="face-disposition" value="` + template.HTMLEscapeString(disposition) + `">`)
		body.WriteString(`<div class="person-candidates candidate">`)
		recognitionCandidates, candidateErr := s.latestRecognitionCandidates(r.Context(), faceID)
		if candidateErr == nil {
			body.WriteString(renderRecognitionCandidates(recognitionCandidates, faceID, personID, canModify))
		}
		possibleCandidates, possibleErr := s.currentPossibleCandidates(r.Context(), faceID)
		if possibleErr == nil {
			body.WriteString(renderPossibleMatches(possibleCandidates, faceID, personID, canModify))
		}
		body.WriteString(`</div></article>`)
	}
	if renderedFaces == 0 && membershipCount > 0 {
		body.WriteString(`<p class="cluster-alert">No faces match the active filters.</p>`)
	}
	if renderedFaces == 0 && membershipCount == 0 {
		body.WriteString(`<p class="cluster-alert">This cluster has no faces.</p>`)
	}
	body.WriteString(`</div><p>Green means user-assigned, yellow means the selected/default person is provisional or system-assigned, and red means the face needs attention. Save may be used before every face is assigned.</p></div><script src="/static/cluster.js" defer></script>`)
	pageStyle = template.CSS(string(pageStyle) + `.card.provisional{border:2px solid #F8E71C}.card.assigned{border:2px solid #7ED321}.card.needs-attention{border:2px solid #D0021B}.box{border:2px solid #D0021B;background:rgba(208,2,27,.15)}.cluster-face.assigned .box{border-color:#7ED321;background:rgba(126,211,33,.15)}.cluster-face.provisional .box{border-color:#F8E71C;background:rgba(248,231,28,.15)}.cluster-face.reference .box{border-color:#50E3C2;background:rgba(80,227,194,.15)}`)
	s.render(w, map[string]any{"Title": "Cluster review", "Account": account, "Breadcrumb": template.HTML(`<a href="/review/clusters">Clusters</a>`), "Body": template.HTML(body.String()), "PageStyle": pageStyle})
}

func (s *Server) clusterScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	_, _ = w.Write([]byte(`(function () {
  const page = document.getElementById('cluster-page'); if (!page) return;
  const message = document.getElementById('cluster-message');
  const dispositionLabels = {'incorrect-assignment':['Incorrect','Correct'], 'false-detection':['No Face','Has Face'], 'dont-track':["Don't Track",'Track']};
  async function send(faceID, url, payload) {
    const response = await fetch(url, {method:'POST', headers:{'Content-Type':'application/json','X-CSRF-Token':page.dataset.csrf}, body:JSON.stringify(payload)});
    if (!response.ok) { message.textContent = await response.text(); message.className = 'error'; return; }
    location.reload();
  }
  function stateFor(card) { const person = card.querySelector('.face-person').value; const pendingState = card.dataset.pendingState; if (person === '__ambiguous__') return 'ambiguous'; if (person) return person === card.dataset.originalPerson && (pendingState === 'assigned-system' || pendingState === 'provisional') ? pendingState : 'assigned-user'; return 'unknown'; }
  function refresh(card) { const state = stateFor(card); card.dataset.pendingState = state; card.style.borderColor = state === 'assigned-user' ? '#2a9d50' : (state === 'unknown' || state === 'ambiguous' ? '#c62828' : '#d6a800'); }
  function refreshDispositionButtons(card) { const current = card.dataset.dispositionActive === 'true' ? card.querySelector('.face-disposition').value : ''; card.querySelectorAll('[data-action="disposition"]').forEach((button) => { const labels = dispositionLabels[button.dataset.disposition]; button.textContent = current === button.dataset.disposition ? labels[1] : labels[0]; }); }
  document.querySelectorAll('[data-person-filter]').forEach((input) => { const select = document.getElementById(input.dataset.personFilter); if (!select) return; const filter = () => { const text = input.value.toLowerCase(); Array.from(select.options).forEach((option) => { const label = (option.dataset.personName || option.textContent || '').toLowerCase(); option.hidden = text !== '' && !label.includes(text); }); const selected = select.options[select.selectedIndex]; if (selected && selected.hidden) { const first = Array.from(select.options).find((option) => !option.hidden); if (first) select.value = first.value; } }; input.addEventListener('input', filter); filter(); });
  document.querySelectorAll('.cluster-face').forEach((card) => { const select = card.querySelector('.face-person'); select.addEventListener('change', () => { card.dataset.pendingState = select.value ? (select.value === '__ambiguous__' ? 'ambiguous' : 'assigned-user') : 'unknown'; refresh(card); }); refreshDispositionButtons(card); refresh(card); });
  document.querySelectorAll('.cluster-face [data-action="unknown"], .cluster-face [data-action="ambiguous"]').forEach((button) => { button.onclick = () => { const card = button.closest('.cluster-face'); const select = card.querySelector('.face-person'); select.value = button.dataset.action === 'ambiguous' ? '__ambiguous__' : ''; card.dataset.pendingState = button.dataset.action; refresh(card); saveReview(); }; });
  document.querySelectorAll('.cluster-face [data-action="disposition"]').forEach((button) => { button.onclick = () => { const card = button.closest('.cluster-face'); const disposition = card.querySelector('.face-disposition'); const active = card.dataset.dispositionActive === 'true' && disposition.value === button.dataset.disposition; disposition.value = button.dataset.disposition; card.dataset.dispositionActive = active ? 'false' : 'true'; refreshDispositionButtons(card); saveReview(); }; });
  document.querySelectorAll('[data-action="assign-candidate"]').forEach((button) => {
    button.onclick = () => send(button.dataset.faceId, '/api/faces/' + encodeURIComponent(button.dataset.faceId) + '/assignment', {person_id:button.dataset.personId, state:'assigned-user', candidate_id:button.dataset.candidateId});
  });
  async function saveReview() {
    const decisions = Array.from(document.querySelectorAll('.cluster-face')).map((card) => { const person = card.querySelector('.face-person').value; const disposition = card.querySelector('.face-disposition').value; const dispositionActive = card.dataset.dispositionActive === 'true'; const state = stateFor(card); const dispositionChanged = dispositionActive !== Boolean(card.dataset.originalDisposition) || (dispositionActive && disposition !== card.dataset.originalDisposition); return {face_id: card.dataset.faceId, person_id: person === '__ambiguous__' ? '' : person, state: state, disposition: dispositionChanged ? disposition : '', disposition_active: dispositionActive, original_state: card.dataset.originalState, original_person: card.dataset.originalPerson}; }).filter((decision) => decision.disposition || decision.state !== decision.original_state || decision.person_id !== decision.original_person);
    const selectedStatus = document.getElementById('cluster-status').value;
    const defaultPerson = document.getElementById('default-person');
    const payload = {default_person_id: defaultPerson.disabled ? '' : defaultPerson.value, status: selectedStatus === 'unreviewed' ? 'in-progress' : selectedStatus, note: document.getElementById('cluster-note').value, expected_version: Number(page.dataset.version), decisions: decisions};
    const response = await fetch('/api/clusters/' + encodeURIComponent(page.dataset.clusterId) + '/review', {method:'POST', headers:{'Content-Type':'application/json','X-CSRF-Token':page.dataset.csrf}, body:JSON.stringify(payload)});
    if (!response.ok) { message.textContent = await response.text(); message.className = 'error'; return; }
    message.textContent = 'Review saved.'; location.reload();
  }
  document.querySelectorAll('.cluster-face [data-action="assign"]').forEach((button) => { button.onclick = () => { const card = button.closest('.cluster-face'); const select = card.querySelector('.face-person'); card.dataset.pendingState = select.value === '__ambiguous__' ? 'ambiguous' : (select.value ? 'assigned-user' : 'unknown'); refresh(card); saveReview(); }; });
  document.getElementById('save-cluster').onclick = saveReview;
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

func dispositionButtonLabel(current, disposition, inactiveLabel, activeLabel string) string {
	if current == disposition {
		return activeLabel
	}
	return inactiveLabel
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
	if runStatus != "active" {
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
	memberRows, err := s.db.QueryContext(r.Context(), `SELECT face_id FROM cluster_memberships WHERE cluster_id = ?`, clusterID)
	if err != nil {
		http.Error(w, "cluster membership lookup failed", http.StatusInternalServerError)
		return
	}
	for memberRows.Next() {
		var faceID string
		if err := memberRows.Scan(&faceID); err != nil {
			memberRows.Close()
			http.Error(w, "cluster membership lookup failed", http.StatusInternalServerError)
			return
		}
		faceAllowed, permissionErr := s.facePermission(r, account, faceID, true)
		if permissionErr != nil || !faceAllowed {
			memberRows.Close()
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}
	if err := memberRows.Err(); err != nil {
		memberRows.Close()
		http.Error(w, "cluster membership lookup failed", http.StatusInternalServerError)
		return
	}
	memberRows.Close()
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
	allowed, err := s.auth.AccessibleCollections(r.Context(), account.ID)
	if err != nil {
		http.Error(w, "representative lookup failed", http.StatusInternalServerError)
		return
	}
	if allowed != nil && len(allowed) == 0 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	visibleClause := ""
	args := []any{}
	if allowed != nil {
		visibleClause = ` AND pp.collection_id IN (` + placeholders(len(allowed)) + `)`
		for _, collectionID := range allowed {
			args = append(args, collectionID)
		}
	}
	args = append(args, personID)
	var related int
	err = s.db.QueryRowContext(r.Context(), `WITH visible_faces AS (
		SELECT DISTINCT f.id FROM faces f JOIN analyses a ON a.id = f.analysis_id JOIN photo_assets pa ON pa.id = a.asset_id JOIN photo_paths pp ON pp.asset_id = pa.id WHERE pp.missing_at IS NULL`+visibleClause+`
	) SELECT EXISTS(SELECT 1 FROM people p WHERE p.id = ? AND (
		EXISTS (SELECT 1 FROM current_face_assignments ca JOIN visible_faces vf ON vf.id = ca.face_id WHERE ca.person_id = p.id) OR
		EXISTS (SELECT 1 FROM clusters c JOIN clustering_runs cr ON cr.id = c.clustering_run_id JOIN cluster_memberships cm ON cm.cluster_id = c.id JOIN visible_faces vf ON vf.id = cm.face_id WHERE cr.status = 'active' AND c.proposed_person_id = p.id) OR
		EXISTS (SELECT 1 FROM recognition_candidates rc JOIN visible_faces vf ON vf.id = rc.face_id WHERE rc.person_id = p.id) OR
		EXISTS (SELECT 1 FROM possible_face_candidates pc JOIN visible_faces vf ON vf.id = pc.face_id WHERE pc.person_id = p.id AND pc.is_current = 1)
	))`, args...).Scan(&related)
	if err != nil {
		http.Error(w, "representative lookup failed", http.StatusInternalServerError)
		return
	}
	if related == 0 {
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
	rows, err := s.db.QueryContext(r.Context(), "SELECT id, kind, status, priority, created_at, COALESCE(started_at,''), COALESCE(completed_at,''), (COALESCE(error_code,'') <> '' OR COALESCE(error_message,'') <> '' OR EXISTS (SELECT 1 FROM job_items ji WHERE ji.job_id = jobs.id AND (COALESCE(ji.error_code,'') <> '' OR COALESCE(ji.error_message,'') <> ''))) FROM jobs "+where+" ORDER BY "+order+", id LIMIT ? OFFSET ?", rowsArgs...)
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
	body := `<h1>Jobs</h1>` + adminNav() + `<form><input name="q" value="` + template.HTMLEscapeString(search) + `" placeholder="Filter"><label>Kind <select name="kind_filter"><option value="">All</option>` + adminSelectOptions(kinds, kindFilter) + `</select></label><label>Status <select name="status_filter"><option value="">All</option>` + adminSelectOptions(statuses, statusFilter) + `</select></label><label>Priority <select name="priority_filter"><option value="">All</option>` + adminSelectOptions(priorities, priorityFilter) + `</select></label><select name="sort"><option value="created">Created</option><option value="kind">Kind</option><option value="status">Status</option></select><select name="order">` + adminOrderOptions(r, "created") + `</select><select name="limit"><option>20</option><option>50</option><option>100</option></select><button>Filter</button></form>` + adminPager(r, search, order, limit, page, total) + `<table><tr><th>ID</th><th>Kind</th><th>Status</th><th>Priority</th><th>Created</th><th>Started</th><th>Completed</th><th>Errors</th></tr>`
	for rows.Next() {
		var id, kind, status, created, started, completed string
		var priority, hasErrors int
		if err := rows.Scan(&id, &kind, &status, &priority, &created, &started, &completed, &hasErrors); err != nil {
			continue
		}
		errorsCell := ""
		if hasErrors != 0 {
			errorsCell = `<a href="/admin/jobs?show_errors=` + template.HTMLEscapeString(url.QueryEscape(id)) + `">Show Errors</a>`
		}
		body += `<tr><td>` + adminIDLink("/admin/jobs/", id) + `</td><td>` + template.HTMLEscapeString(kind) + `</td><td>` + template.HTMLEscapeString(status) + `</td><td>` + fmt.Sprint(priority) + `</td><td>` + template.HTMLEscapeString(created) + `</td><td>` + template.HTMLEscapeString(started) + `</td><td>` + template.HTMLEscapeString(completed) + `</td><td>` + errorsCell + `</td></tr>`
	}
	body += `</table>`
	if jobID := r.URL.Query().Get("show_errors"); jobID != "" {
		body += s.adminJobErrors(r.Context(), jobID)
	}
	s.render(w, map[string]any{"Title": "Jobs", "Account": account, "Body": template.HTML(body)})
}

func (s *Server) adminJobErrors(ctx context.Context, id string) string {
	var body strings.Builder
	body.WriteString(`<h2>Errors for job ` + template.HTMLEscapeString(id) + `</h2><table><tr><th>Item</th><th>Status</th><th>Attempts</th><th>Code</th><th>Message</th></tr>`)
	var errorCode, errorMessage string
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(error_code,''), COALESCE(error_message,'') FROM jobs WHERE id = ?`, id).Scan(&errorCode, &errorMessage); err == nil && (errorCode != "" || errorMessage != "") {
		body.WriteString(`<tr><td>(job)</td><td></td><td></td><td>` + template.HTMLEscapeString(errorCode) + `</td><td>` + template.HTMLEscapeString(errorMessage) + `</td></tr>`)
	}
	errorRows, err := s.db.QueryContext(ctx, `SELECT item_key, status, attempts, COALESCE(error_code,''), COALESCE(error_message,'') FROM job_items WHERE job_id = ? AND (COALESCE(error_code,'') <> '' OR COALESCE(error_message,'') <> '') ORDER BY item_key`, id)
	if err == nil {
		defer errorRows.Close()
		for errorRows.Next() {
			var itemKey, itemStatus, itemCode, itemMessage string
			var attempts int
			if err := errorRows.Scan(&itemKey, &itemStatus, &attempts, &itemCode, &itemMessage); err != nil {
				continue
			}
			body.WriteString(`<tr><td>` + template.HTMLEscapeString(itemKey) + `</td><td>` + template.HTMLEscapeString(itemStatus) + `</td><td>` + fmt.Sprint(attempts) + `</td><td>` + template.HTMLEscapeString(itemCode) + `</td><td>` + template.HTMLEscapeString(itemMessage) + `</td></tr>`)
		}
	}
	body.WriteString(`</table>`)
	return body.String()
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
	body += `</table>`
	if table == "jobs" && r.URL.Query().Get("show_errors") == "true" {
		body += s.adminJobErrors(r.Context(), id)
	}
	body += `<p><a href="` + template.HTMLEscapeString(prefix) + `">Back to list</a></p>`
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

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
	"path/filepath"
	"strings"

	"github.com/pjg/face_me_bro/internal/audit"
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
	mux.HandleFunc("GET /static/person.js", s.personScript)
	mux.HandleFunc("POST /api/faces/{faceID}/assignment", s.assignFace)
	mux.HandleFunc("POST /api/faces/{faceID}/reference", s.referenceFace)
	mux.HandleFunc("GET /", s.dashboard)
	mux.HandleFunc("GET /review/photos", s.reviewPhotos)
	mux.HandleFunc("GET /review/people", s.reviewPeople)
	mux.HandleFunc("GET /review/people/{personID}", s.reviewPerson)
	mux.HandleFunc("GET /review/clusters", s.reviewClusters)
	mux.HandleFunc("GET /review/clusters/{clusterID}", s.reviewCluster)
	mux.HandleFunc("POST /api/clusters/{clusterID}/review", s.reviewClusterAPI)
	mux.HandleFunc("POST /api/audit/{eventID}/undo", s.undoAudit)
	mux.HandleFunc("GET /admin/accounts", s.adminAccounts)
	mux.HandleFunc("GET /admin/collections", s.adminCollections)
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
	allowed, err := s.auth.AccessibleCollections(r.Context(), account.ID)
	if err != nil {
		http.Error(w, "permission lookup failed", http.StatusInternalServerError)
		return
	}
	result, err := search.NewStore(s.db).Photos(r.Context(), search.Options{AllowedCollectionIDs: allowed, CollectionID: query.Get("collection_id"), RelativePrefix: query.Get("directory"), IncludeProvisional: query.Get("include_provisional") == "true", IncludeDontTrack: query.Get("include_dont_track") == "true"})
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
	allowed, err := s.auth.AccessibleCollections(r.Context(), account.ID)
	if err != nil {
		http.Error(w, "permission lookup failed", http.StatusInternalServerError)
		return
	}
	peopleQuery := `SELECT p.id, COALESCE(NULLIF(p.name, ''), substr(p.id, -8)), p.is_provisional, p.dont_track,
		(SELECT COUNT(DISTINCT ca.face_id) FROM current_face_assignments ca
		 JOIN faces f ON f.id = ca.face_id JOIN analyses a ON a.id = f.analysis_id
		 JOIN photo_assets pa ON pa.id = a.asset_id JOIN photo_paths ppc ON ppc.asset_id = pa.id
		 WHERE ca.person_id = p.id AND ppc.missing_at IS NULL) FROM people p`
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
		peopleQuery = strings.Replace(peopleQuery, `WHERE ca.person_id = p.id AND ppc.missing_at IS NULL)`, `WHERE ca.person_id = p.id AND ppc.missing_at IS NULL AND ppc.collection_id IN (`+strings.Join(placeholders, ",")+`))`, 1)
		for _, id := range allowed {
			args = append(args, id)
		}
		peopleQuery += ` WHERE EXISTS (SELECT 1 FROM faces f JOIN analyses a ON a.id = f.analysis_id JOIN current_face_assignments ca ON ca.face_id = f.id JOIN photo_assets pa ON pa.id = a.asset_id JOIN photo_paths pp ON pp.asset_id = pa.id WHERE ca.person_id = p.id AND pp.collection_id IN (` + strings.Join(placeholders, ",") + `))`
	}
	peopleQuery += ` ORDER BY COALESCE(NULLIF(p.name, ''), substr(p.id, -8)), p.id`
	rows, err := s.db.QueryContext(r.Context(), peopleQuery, args...)
	if err != nil {
		http.Error(w, "people lookup failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var body strings.Builder
	body.WriteString(`<h1>People</h1><table><tr><th>Name</th><th>Faces</th><th>Provisional</th><th>Don't track</th></tr>`)
	for rows.Next() {
		var id, name string
		var provisional, dontTrack int
		var faceCount int
		if err := rows.Scan(&id, &name, &provisional, &dontTrack, &faceCount); err != nil {
			http.Error(w, "people lookup failed", http.StatusInternalServerError)
			return
		}
		body.WriteString(`<tr><td><a href="/review/people/` + template.HTMLEscapeString(id) + `">` + template.HTMLEscapeString(name) + `</a></td><td>` + fmt.Sprint(faceCount) + `</td><td>` + fmt.Sprint(provisional == 1) + `</td><td>` + fmt.Sprint(dontTrack == 1) + `</td></tr>`)
	}
	body.WriteString(`</table>`)
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
		COALESCE(ae.similarity, 0), COALESCE(ca.state, 'unassigned'), COALESCE(ca.person_id, ''), COALESCE(cfr.reference, 0)
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
		ID, PersonName     string
		Quality, Detection float64
	}
	type personFace struct {
		FaceID, PathID, RelativePath string
		Width, Height                int
		Left, Top, Right, Bottom     float64
		Quality, Detection           float64
		Confidence                   float64
		State, PersonID              string
		Reference                    bool
		Recommended                  bool
		Cluster                      *personCluster
		Candidates                   []personCandidate
	}
	var faces []personFace
	referenceCount := 0
	for rows.Next() {
		var face personFace
		var reference int
		if err := rows.Scan(&face.FaceID, &face.PathID, &face.RelativePath, &face.Width, &face.Height, &face.Left, &face.Top, &face.Right, &face.Bottom, &face.Quality, &face.Detection, &face.Confidence, &face.State, &face.PersonID, &reference); err != nil {
			http.Error(w, "person faces lookup failed", http.StatusInternalServerError)
			return
		}
		face.Reference = reference == 1
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
					face.Confidence = candidate.Similarity
					break
				}
			}
		}
		var recommendation int
		if err := s.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM reference_recommendations WHERE face_id = ? AND person_id = ? AND is_current = 1)`, face.FaceID, personID).Scan(&recommendation); err == nil {
			face.Recommended = recommendation == 1
		}
		var cluster personCluster
		if err := s.db.QueryRowContext(r.Context(), `
			SELECT c.id, COALESCE(NULLIF(p.name, ''), CASE WHEN c.proposed_person_id IS NULL THEN 'Unknown' ELSE substr(c.proposed_person_id, -8) END),
			       COALESCE(AVG(f2.quality), 0), COALESCE(AVG(f2.detection_confidence), 0)
			FROM cluster_memberships cm
			JOIN clusters c ON c.id = cm.cluster_id
			JOIN clustering_runs cr ON cr.id = c.clustering_run_id AND cr.status = 'active'
			LEFT JOIN people p ON p.id = c.proposed_person_id
			JOIN cluster_memberships cm2 ON cm2.cluster_id = c.id
			JOIN faces f2 ON f2.id = cm2.face_id
			WHERE cm.face_id = ?
			GROUP BY c.id, p.name, c.proposed_person_id
			ORDER BY c.id LIMIT 1`, face.FaceID).Scan(&cluster.ID, &cluster.PersonName, &cluster.Quality, &cluster.Detection); err == nil {
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
	maxConfidence, minConfidence := 0.0, 1.0
	hasConfidence := false
	clusterSummary := make(map[string][]string)
	clusterSummaryOrder := make([]string, 0)
	for _, face := range faces {
		if face.Confidence > 0 {
			if !hasConfidence || face.Confidence > maxConfidence {
				maxConfidence = face.Confidence
			}
			if !hasConfidence || face.Confidence < minConfidence {
				minConfidence = face.Confidence
			}
			hasConfidence = true
		}
		if face.Cluster != nil {
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

	body := strings.Builder{}
	pageStyle := template.CSS(`.person-header{
  position:sticky;
  top:0;z-index:10;
  background:#fff;
  padding:10px 0;
  border-bottom:1px solid #ccc}
.grid{
  display:flex;
  flex-wrap:wrap;
  gap:18px}
.card{
  width:min(420px,45vw);
  border:1px solid #ccc;
  padding:10px}
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
.person-metadata{font-size:13.3333px;line-height:1.35}
.person-candidates{font-size:13.3333px;line-height:1.35}
.person-actions{display:flex;justify-content:space-between;align-items:center;margin-top:8px}
.person-dispositions{display:flex;gap:20px}
.person-assignment{display:flex;justify-content:flex-end;gap:6px;margin-top:8px}
.person-assignment select{min-width:12rem}`)
	peopleRows, peopleErr := s.db.QueryContext(r.Context(), `SELECT id, COALESCE(NULLIF(name, ''), substr(id, -8), id) FROM people ORDER BY LOWER(COALESCE(NULLIF(name, ''), substr(id, -8), id)), id`)
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

	body.WriteString(`<div id="person-page" data-person-id="` + template.HTMLEscapeString(personID) + `" data-csrf="` + template.HTMLEscapeString(csrfCookie(r)) + `" class="person-header"><h1>` + template.HTMLEscapeString(personName) + ` <span class="person-summary">(` + fmt.Sprint(len(faces)) + ` faces, ` + fmt.Sprint(referenceCount) + ` references, Max: ` + formatScore(maxConfidence) + `%, Min: ` + formatScore(minConfidence) + `%)</span></h1>`)
	if len(clusterSummaryOrder) > 0 {
		body.WriteString(`<div>Clusters: `)
		for index, clusterPerson := range clusterSummaryOrder {
			if index > 0 {
				body.WriteString(`, `)
			}
			body.WriteString(template.HTMLEscapeString(clusterPerson) + ` (`)
			for clusterIndex, clusterID := range clusterSummary[clusterPerson] {
				if clusterIndex > 0 {
					body.WriteString(`, `)
				}
				body.WriteString(template.HTMLEscapeString(idSuffix(clusterID)))
			}
			body.WriteString(`)`)
		}
		body.WriteString(`</div>`)
	}
	body.WriteString(`</div>`)
	body.WriteString(`<div id="photos" class="grid">`)
	canModify := account.Role != auth.Viewer
	for _, face := range faces {
		boxClass := "box"
		if face.Reference {
			boxClass += " reference"
		}
		body.WriteString(`<article class="card" id="face-` + template.HTMLEscapeString(idSuffix(face.FaceID)) + `"><div class="viewer"><img loading="lazy" src="/api/photos/` + template.HTMLEscapeString(face.PathID) + `" alt="` + template.HTMLEscapeString(idSuffix(face.FaceID)) + `"><span class="` + boxClass + `" style="left:` + percent(face.Left, face.Width) + `%;top:` + percent(face.Top, face.Height) + `%;width:` + percent(face.Right-face.Left, face.Width) + `%;height:` + percent(face.Bottom-face.Top, face.Height) + `%"></span></div><div class="path">` + template.HTMLEscapeString(face.RelativePath) + `</div>`)
		body.WriteString(`<div class="person-metadata"><strong>Assignment:</strong> ` + template.HTMLEscapeString(face.State) + `; <strong>Confidence:</strong> ` + formatScore(face.Confidence) + `%; <strong>Face quality:</strong> ` + formatScore(face.Quality) + `%; <strong>Detection:</strong> ` + formatScore(face.Detection) + `%`)
		if face.Recommended {
			body.WriteString(`; <strong>Ref Rec</strong>`)
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
				body.WriteString(template.HTMLEscapeString(candidate.Name) + ` (` + formatScore(candidate.Similarity) + `%)`)
			}
			body.WriteString(`</div>`)
		}
		if face.Cluster != nil {
			body.WriteString(`<div class="person-metadata">Cluster: ` + template.HTMLEscapeString(idSuffix(face.Cluster.ID)) + `: ` + template.HTMLEscapeString(face.Cluster.PersonName) + ` (Quality: ` + formatScore(face.Cluster.Quality) + `%, Detection: ` + formatScore(face.Cluster.Detection) + `%)</div>`)
		}
		if canModify {
			body.WriteString(`<div class="actions"><div class="person-dispositions"><button type="button" data-action="unknown" data-face-id="` + template.HTMLEscapeString(face.FaceID) + `">Unknown</button><button type="button" data-action="ambiguous" data-face-id="` + template.HTMLEscapeString(face.FaceID) + `">Ambiguous</button></div>`)
			if face.Reference || (face.Recommended && (face.State == string(review.AssignedSystem) || face.State == string(review.AssignedUser))) {
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
	body.WriteString(`</div>`)
	if canModify {
		body.WriteString(`<script src="/static/person.js" defer></script>`)
	}
	s.render(w, map[string]any{"Title": personName, "Account": account, "PageStyle": pageStyle, "Body": template.HTML(body.String())})
}

func (s *Server) personScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	_, _ = w.Write([]byte(`(function () {
  const page = document.getElementById('person-page');
  if (!page) return;
  const token = page.dataset.csrf || '';
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
	clusterQuery := `SELECT c.id, c.cluster_key, c.purity, c.connectivity, c.is_mixed, c.review_status FROM clusters c JOIN clustering_runs cr ON cr.id = c.clustering_run_id WHERE cr.status = 'active'`
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
	clusterQuery += ` ORDER BY c.is_mixed DESC, c.cluster_key`
	rows, err := s.db.QueryContext(r.Context(), clusterQuery, clusterArgs...)
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
	var runStatus, supersededBy, reviewStatus, collectionID string
	var reviewVersion int
	err := s.db.QueryRowContext(r.Context(), `
		SELECT cr.status, COALESCE(cr.superseded_by, ''), c.review_status, c.review_version,
		       (SELECT pp.collection_id FROM cluster_memberships cm
				JOIN faces f ON f.id = cm.face_id JOIN analyses a ON a.id = f.analysis_id
				JOIN photo_assets pa ON pa.id = a.asset_id JOIN photo_paths pp ON pp.asset_id = pa.id
				WHERE cm.cluster_id = c.id LIMIT 1)
		FROM clusters c JOIN clustering_runs cr ON cr.id = c.clustering_run_id WHERE c.id = ?`, clusterID).
		Scan(&runStatus, &supersededBy, &reviewStatus, &reviewVersion, &collectionID)
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
	rows, err := s.db.QueryContext(r.Context(), `
		SELECT cm.face_id, COALESCE(ca.state, 'unassigned'), COALESCE(ca.person_id, ''),
		       COALESCE(pp.id, ''), COALESCE(pa.width, 0), COALESCE(pa.height, 0),
		       f.box_left, f.box_top, f.box_right, f.box_bottom
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
	body.WriteString(`<style>.cluster-face{display:inline-block;vertical-align:top;width:24rem;margin:1rem;padding:1rem;border:4px solid}.photo-frame{position:relative;display:inline-block;max-width:22rem}.photo-frame img{display:block;max-width:22rem;max-height:18rem}.face-box{position:absolute;border:3px solid #d00;box-sizing:border-box}.candidate{font-size:.9rem}</style>`)
	body.WriteString(`<h1>Cluster ` + template.HTMLEscapeString(clusterID) + `</h1>`)
	body.WriteString(`<p>Review status: ` + template.HTMLEscapeString(reviewStatus) + `; version ` + fmt.Sprint(reviewVersion) + `; run: ` + template.HTMLEscapeString(runStatus) + `</p>`)
	if runStatus == "superseded" {
		body.WriteString(`<p class="error">This is a historical clustering run. A newer run is active`)
		if supersededBy != "" {
			body.WriteString(` (` + template.HTMLEscapeString(supersededBy) + `)`)
		}
		body.WriteString(`. Review changes are still recorded against this historical cluster.</p>`)
	}
	body.WriteString(`<form method="post" action="/api/clusters/` + template.HTMLEscapeString(clusterID) + `/review"><input type="hidden" name="csrf" value="` + template.HTMLEscapeString(csrfCookie(r)) + `"><input type="hidden" name="expected_version" value="` + fmt.Sprint(reviewVersion) + `"><table><tr><th>Face</th><th>State</th><th>Person</th><th>Candidate evidence</th></tr>`)
	for rows.Next() {
		var faceID, state, personID, pathID string
		var width, height int
		var left, top, right, bottom float64
		if err := rows.Scan(&faceID, &state, &personID, &pathID, &width, &height, &left, &top, &right, &bottom); err != nil {
			http.Error(w, "cluster lookup failed", http.StatusInternalServerError)
			return
		}
		color := faceStateColor(state)
		body.WriteString(`<tr><td><div class="cluster-face" style="border-color:` + color + `"><div class="photo-frame"><img src="/api/photos/` + template.HTMLEscapeString(pathID) + `" alt="` + template.HTMLEscapeString(faceID) + `"><span class="face-box" style="left:` + percent(left, width) + `%;top:` + percent(top, height) + `%;width:` + percent(right-left, width) + `%;height:` + percent(bottom-top, height) + `%"></span></div></div><br>` + template.HTMLEscapeString(faceID) + `</td><td>` + template.HTMLEscapeString(state) + `</td><td>` + template.HTMLEscapeString(personID) + `</td><td class="candidate">`)
		candidateRows, candidateErr := s.db.QueryContext(r.Context(), `SELECT COALESCE(NULLIF(p.name, ''), substr(rc.person_id, -8), rc.person_id), rc.similarity, rc.margin, rc.support_count FROM recognition_candidates rc LEFT JOIN people p ON p.id = rc.person_id WHERE rc.face_id = ? ORDER BY rc.created_at DESC, rc.rank LIMIT 5`, faceID)
		if candidateErr == nil {
			for candidateRows.Next() {
				var name string
				var similarity, margin float64
				var support int
				if candidateRows.Scan(&name, &similarity, &margin, &support) == nil {
					body.WriteString(template.HTMLEscapeString(name) + template.HTMLEscapeString(fmt.Sprintf(" %.3f (margin %.3f, support %d)", similarity, margin, support)) + `<br>`)
				}
			}
			candidateRows.Close()
		}
		body.WriteString(`</td></tr>`)
	}
	body.WriteString(`</table><p>Use the authenticated cluster review API to submit assignments, unknown/ambiguous states, false-detection or don't-track dispositions. Saving must include the displayed review version.</p></form>`)
	s.render(w, map[string]any{"Title": "Cluster review", "Account": account, "Body": template.HTML(body.String())})
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
	var collectionID string
	if err := s.db.QueryRowContext(r.Context(), `SELECT pp.collection_id FROM cluster_memberships cm JOIN faces f ON f.id = cm.face_id JOIN analyses a ON a.id = f.analysis_id JOIN photo_assets pa ON pa.id = a.asset_id JOIN photo_paths pp ON pp.asset_id = pa.id WHERE cm.cluster_id = ? LIMIT 1`, clusterID).Scan(&collectionID); err != nil {
		http.Error(w, "cluster not found", http.StatusNotFound)
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

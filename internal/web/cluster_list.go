package web

import (
	"database/sql"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

type clusterListRow struct {
	ID, Key, RunID, RunStatus, ReviewStatus string
	ProposedPersonID, ProposedPersonName    string
	People                                  []clusterListPerson
	Purity, Consistency, Connectivity       sql.NullFloat64
	Mixed                                   bool
	FaceCount, CandidateCount, ToAssign     int
	AssignedMatch                           int
}

type clusterListPerson struct {
	ID, Name string
	Count    int
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
	if allowed != nil && len(allowed) == 0 {
		s.render(w, map[string]any{"Title": "Clusters", "Account": account, "Body": template.HTML(`<h1>Clusters (0/0)</h1><p>No collections are assigned to this account.</p>`)})
		return
	}

	query := r.URL.Query()
	status := query.Get("status")
	runStatus := query.Get("run_status")
	if runStatus == "" {
		runStatus = "active"
	}
	if !validRunStatus(runStatus) {
		runStatus = "active"
	}
	clusterQuery := `WITH visible_members AS (
		SELECT DISTINCT cm.cluster_id, cm.face_id
		FROM cluster_memberships cm
		JOIN faces f ON f.id = cm.face_id
		JOIN analyses a ON a.id = f.analysis_id
		JOIN photo_assets pa ON pa.id = a.asset_id
		JOIN photo_paths pp ON pp.asset_id = pa.id
		WHERE pp.missing_at IS NULL`
	var args []any
	if allowed != nil {
		clusterQuery += ` AND pp.collection_id IN (` + placeholders(len(allowed)) + `)`
		for _, collectionID := range allowed {
			args = append(args, collectionID)
		}
	}
	clusterQuery += `), latest_candidates AS (
		SELECT rc.face_id, rc.person_id, rc.similarity
		FROM recognition_candidates rc
		WHERE rc.created_at = (SELECT MAX(rc2.created_at) FROM recognition_candidates rc2 WHERE rc2.face_id = rc.face_id)
	)
	SELECT c.id, c.cluster_key, c.clustering_run_id, cr.status,
		COALESCE(c.proposed_person_id, ''), COALESCE(NULLIF(p.name, ''), substr(c.proposed_person_id, -8), ''),
		c.purity, c.consistency, c.connectivity, c.is_mixed, c.review_status,
		COUNT(DISTINCT vm.face_id),
		COUNT(DISTINCT CASE WHEN lc.similarity >= 0.015 THEN vm.face_id END),
		COUNT(DISTINCT CASE WHEN COALESCE(ca.state, 'unassigned') NOT IN ('assigned-user', 'assigned-system') THEN vm.face_id END),
		COUNT(DISTINCT CASE WHEN COALESCE(ca.state, 'unassigned') IN ('assigned-user', 'assigned-system') AND ca.person_id = c.proposed_person_id THEN vm.face_id END)
	FROM clusters c
	JOIN clustering_runs cr ON cr.id = c.clustering_run_id
	JOIN visible_members vm ON vm.cluster_id = c.id
	LEFT JOIN people p ON p.id = c.proposed_person_id
	LEFT JOIN current_face_assignments ca ON ca.face_id = vm.face_id
	LEFT JOIN latest_candidates lc ON lc.face_id = vm.face_id
	WHERE cr.status = ?`
	args = append(args, runStatus)
	if status != "" {
		clusterQuery += ` AND c.review_status = ?`
		args = append(args, status)
	}
	if mixed := query.Get("mixed"); mixed == "true" || mixed == "false" {
		clusterQuery += ` AND c.is_mixed = ?`
		if mixed == "true" {
			args = append(args, 1)
		} else {
			args = append(args, 0)
		}
	}
	if query.Get("include_dont_track") != "true" {
		clusterQuery += ` AND NOT EXISTS (SELECT 1 FROM visible_members vm2 JOIN current_face_dispositions fd ON fd.face_id = vm2.face_id WHERE vm2.cluster_id = c.id AND fd.disposition = 'dont-track')`
	}
	clusterQuery += ` GROUP BY c.id, c.cluster_key, c.clustering_run_id, cr.status, c.proposed_person_id, p.name, c.purity, c.consistency, c.connectivity, c.is_mixed, c.review_status`
	rows, err := s.db.QueryContext(r.Context(), clusterQuery, args...)
	if err != nil {
		http.Error(w, "cluster lookup failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var clusters []clusterListRow
	for rows.Next() {
		var row clusterListRow
		var mixed int
		if err := rows.Scan(&row.ID, &row.Key, &row.RunID, &row.RunStatus, &row.ProposedPersonID, &row.ProposedPersonName, &row.Purity, &row.Consistency, &row.Connectivity, &mixed, &row.ReviewStatus, &row.FaceCount, &row.CandidateCount, &row.ToAssign, &row.AssignedMatch); err != nil {
			http.Error(w, "cluster lookup failed", http.StatusInternalServerError)
			return
		}
		row.Mixed = mixed != 0
		row.People, err = s.clusterListPeople(r, row.ID, allowed)
		if err != nil {
			http.Error(w, "cluster people lookup failed", http.StatusInternalServerError)
			return
		}
		clusters = append(clusters, row)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "cluster lookup failed", http.StatusInternalServerError)
		return
	}

	q := strings.ToLower(strings.TrimSpace(query.Get("q")))
	minFaces, maxFaces := parseIntFilter(query.Get("min_faces")), parseIntFilter(query.Get("max_faces"))
	minConsistency, maxConsistency := parseScoreFilter(query.Get("min_consistency")), parseScoreFilter(query.Get("max_consistency"))
	rating := query.Get("rating")
	filtered := clusters[:0]
	for _, row := range clusters {
		personText := strings.ToLower(row.ProposedPersonID + " " + row.ProposedPersonName)
		for _, person := range row.People {
			personText += " " + strings.ToLower(person.ID+" "+person.Name)
		}
		if q != "" && !strings.Contains(strings.ToLower(row.ID+" "+row.Key+" "+personText), q) {
			continue
		}
		if minFaces > 0 && row.FaceCount < minFaces || maxFaces > 0 && row.FaceCount > maxFaces {
			continue
		}
		if minConsistency != nil && (!row.Consistency.Valid || row.Consistency.Float64 < *minConsistency) || maxConsistency != nil && (!row.Consistency.Valid || row.Consistency.Float64 > *maxConsistency) {
			continue
		}
		if !matchesClusterRating(row, rating) {
			continue
		}
		filtered = append(filtered, row)
	}
	clusters = filtered

	sortKey := query.Get("sort")
	if sortKey == "" {
		sortKey = "mixed"
	}
	direction := query.Get("direction")
	if direction != "desc" {
		direction = "asc"
	}
	desc := direction == "desc"
	sort.SliceStable(clusters, func(i, j int) bool {
		cmp := compareClusterRows(clusters[i], clusters[j], sortKey)
		if cmp == 0 {
			cmp = strings.Compare(clusters[i].ID, clusters[j].ID)
		}
		if desc {
			return cmp > 0
		}
		return cmp < 0
	})

	total := len(clusters)
	perPage := validPerPage(query.Get("per_page"))
	page := validPage(query.Get("page"))
	pageCount := (total + perPage - 1) / perPage
	if pageCount == 0 {
		pageCount = 1
	}
	if page > pageCount {
		page = pageCount
	}
	start := (page - 1) * perPage
	end := minInt(start+perPage, total)
	pageClusters := clusters
	if start >= total {
		pageClusters = nil
	} else {
		pageClusters = clusters[start:end]
	}

	var body strings.Builder
	body.WriteString(`<h1>Clusters (` + strconv.Itoa(len(pageClusters)) + `/` + strconv.Itoa(total) + `)</h1>`)
	body.WriteString(`<form id="clusters-controls" method="get">`)
	body.WriteString(`<div class="clusters-primary-controls"><label><strong>Search</strong> <input name="q" value="` + template.HTMLEscapeString(query.Get("q")) + `" placeholder="Cluster or person"></label>`)
	body.WriteString(`<label><strong>Mixed</strong> <select name="mixed">` + clusterOption("", "Any", query.Get("mixed")) + clusterOption("true", "Mixed", query.Get("mixed")) + clusterOption("false", "Pure", query.Get("mixed")) + `</select></label>`)
	body.WriteString(`<label><strong>Review</strong> <select name="status">` + clusterOption("", "Any status", status) + clusterOption("unreviewed", "Unreviewed", status) + clusterOption("in-progress", "In progress", status) + clusterOption("partially-reviewed", "Partially reviewed", status) + clusterOption("reviewed", "Reviewed", status) + clusterOption("needs-review", "Needs review", status) + clusterOption("superseded", "Superseded", status) + `</select></label>`)
	body.WriteString(`<label><strong>Run</strong> <select name="run_status">`)
	for _, value := range []string{"active", "queued", "running", "completed", "superseded", "failed", "cancelled"} {
		body.WriteString(clusterOption(value, humanRunStatus(value), runStatus))
	}
	body.WriteString(`</select></label>`)
	body.WriteString(`<label><strong>Rating</strong> <select name="rating">` + clusterOption("", "Any rating", rating) + clusterOption("high", "High", rating) + clusterOption("medium", "Medium", rating) + clusterOption("low", "Low", rating) + `</select></label>`)
	body.WriteString(`<label><input type="checkbox" name="include_dont_track" value="true"` + checkedAttr(query.Get("include_dont_track") == "true") + `> Include Don’t Track</label>`)
	body.WriteString(`<label>Min faces <input name="min_faces" type="number" min="1" value="` + template.HTMLEscapeString(query.Get("min_faces")) + `"></label><label>Max faces <input name="max_faces" type="number" min="1" value="` + template.HTMLEscapeString(query.Get("max_faces")) + `"></label>`)
	body.WriteString(`<label>Min consistency <input name="min_consistency" type="number" min="0" max="100" step="0.1" value="` + template.HTMLEscapeString(query.Get("min_consistency")) + `"></label><label>Max consistency <input name="max_consistency" type="number" min="0" max="100" step="0.1" value="` + template.HTMLEscapeString(query.Get("max_consistency")) + `"></label></div>`)
	body.WriteString(`<div class="clusters-sort-controls"><label><strong>Sort</strong> <select name="sort">`)
	for _, option := range []struct{ value, label string }{{"mixed", "Mixed"}, {"face_count", "Face count"}, {"candidate_count", "Candidate count"}, {"person", "Person"}, {"rating", "Rating"}, {"review_status", "Review status"}, {"to_assign", "To Be Assigned"}, {"cluster_key", "Cluster key"}} {
		body.WriteString(clusterOption(option.value, option.label, sortKey))
	}
	body.WriteString(`</select></label><label><strong>Direction</strong> <select name="direction">` + clusterOption("asc", "Ascending", direction) + clusterOption("desc", "Descending", direction) + `</select></label><label><strong>Per Page</strong> <select name="per_page">`)
	for _, size := range []int{10, 20, 25, 50, 100} {
		body.WriteString(clusterOption(strconv.Itoa(size), strconv.Itoa(size), strconv.Itoa(perPage)))
	}
	body.WriteString(`</select></label><button>Apply</button></div></form>`)
	renderClusterPager(&body, r, page, pageCount, perPage)
	body.WriteString(`<table class="clusters-table"><thead><tr><th>Cluster</th><th>Person</th><th>Faces</th><th>Candidates</th><th>To Assign</th><th>Assigned Match</th><th>Consistency</th><th>Purity</th><th>Connectivity</th><th>Mixed</th><th>Review</th><th>Type</th></tr></thead><tbody>`)
	for _, row := range pageClusters {
		personCell := clusterPeopleHTML(row)
		candidateCount := strconv.Itoa(row.CandidateCount)
		if row.ToAssign == 0 {
			candidateCount = "N/A"
		}
		assignedMatch := ""
		if row.Mixed {
			assignedMatch = "-"
		} else if row.FaceCount-row.ToAssign > 0 {
			assignedMatch = strconv.Itoa(row.AssignedMatch)
		}
		mixedCell := ""
		if row.Mixed {
			mixedCell = "X"
		}
		body.WriteString(`<tr><td><a href="/review/clusters/` + template.HTMLEscapeString(row.ID) + `">` + template.HTMLEscapeString(row.Key) + `</a></td><td>` + personCell + `</td><td>` + strconv.Itoa(row.FaceCount) + `</td><td>` + candidateCount + `</td><td>` + strconv.Itoa(row.ToAssign) + `</td><td>` + assignedMatch + `</td><td>` + formatScoreUnavailable(row.Consistency.Float64, row.Consistency.Valid) + `</td><td>` + formatScoreUnavailable(row.Purity.Float64, row.Purity.Valid) + `</td><td>` + formatScoreUnavailable(row.Connectivity.Float64, row.Connectivity.Valid) + `</td><td>` + mixedCell + `</td><td>` + template.HTMLEscapeString(humanReviewStatus(row.ReviewStatus)) + `</td><td>` + template.HTMLEscapeString(clusterType(row.RunStatus)) + `</td></tr>`)
	}
	body.WriteString(`</tbody></table>`)
	renderClusterPager(&body, r, page, pageCount, perPage)
	pageStyle := template.CSS(`#clusters-controls{display:flex;flex-wrap:wrap;gap:.4rem;margin:.75rem 0;width:max-content;max-width:100%;overflow-x:auto}#clusters-controls .clusters-primary-controls,#clusters-controls .clusters-sort-controls{display:flex;align-items:center;flex-wrap:wrap;gap:.4rem;flex-basis:100%}#clusters-controls .clusters-sort-controls{margin-top:.6rem}#clusters-controls button{margin-left:4rem}.clusters-page-pager{width:max-content;display:flex;align-items:center;gap:.4rem;margin:.5rem 0}.clusters-page-pager .page-jump{display:flex;align-items:center;gap:.4rem}.clusters-table{width:max-content}.clusters-table td,.clusters-table th{white-space:nowrap}.clusters-table img{max-height:32px;width:auto;border-radius:4px;vertical-align:middle}.cluster-people-list{display:block;font-size:.9em}`)
	s.render(w, map[string]any{"Title": "Clusters", "Account": account, "Body": template.HTML(body.String()), "PageStyle": pageStyle})
}

func (s *Server) clusterListPeople(r *http.Request, clusterID string, allowed []string) ([]clusterListPerson, error) {
	query := `SELECT ca.person_id, COALESCE(NULLIF(p.name, ''), substr(ca.person_id, -8), ca.person_id), COUNT(*)
		FROM cluster_memberships cm JOIN current_face_assignments ca ON ca.face_id = cm.face_id
		JOIN people p ON p.id = ca.person_id JOIN faces f ON f.id = cm.face_id JOIN analyses a ON a.id = f.analysis_id JOIN photo_assets pa ON pa.id = a.asset_id JOIN photo_paths pp ON pp.asset_id = pa.id
		WHERE cm.cluster_id = ? AND ca.state IN ('assigned-user', 'assigned-system') AND pp.missing_at IS NULL`
	args := []any{clusterID}
	if allowed != nil {
		query += ` AND pp.collection_id IN (` + placeholders(len(allowed)) + `)`
		for _, collectionID := range allowed {
			args = append(args, collectionID)
		}
	}
	query += ` GROUP BY ca.person_id, p.name ORDER BY COUNT(*) DESC, LOWER(COALESCE(NULLIF(p.name, ''), substr(ca.person_id, -8), ca.person_id)), ca.person_id`
	rows, err := s.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var people []clusterListPerson
	for rows.Next() {
		var person clusterListPerson
		if err := rows.Scan(&person.ID, &person.Name, &person.Count); err != nil {
			return nil, err
		}
		people = append(people, person)
	}
	return people, rows.Err()
}

func clusterPeopleHTML(row clusterListRow) string {
	if len(row.People) == 0 {
		if row.ProposedPersonID == "" {
			return "Unassigned"
		}
		return `<img loading="lazy" alt="" src="/api/people/` + template.HTMLEscapeString(row.ProposedPersonID) + `/representative"> <a href="/review/people/` + template.HTMLEscapeString(row.ProposedPersonID) + `">` + template.HTMLEscapeString(row.ProposedPersonName) + `</a>`
	}
	var body strings.Builder
	if len(row.People) > 1 {
		body.WriteString(`<strong>Multiple</strong> `)
	}
	body.WriteString(`<span class="cluster-people-list">`)
	for index, person := range row.People {
		if index > 0 {
			body.WriteString(`, `)
		}
		body.WriteString(`<img loading="lazy" alt="" src="/api/people/` + template.HTMLEscapeString(person.ID) + `/representative"> <a href="/review/people/` + template.HTMLEscapeString(person.ID) + `">` + template.HTMLEscapeString(person.Name) + `</a> (` + strconv.Itoa(person.Count) + `)`)
	}
	body.WriteString(`</span>`)
	return body.String()
}

func compareClusterRows(left, right clusterListRow, key string) int {
	compareInt := func(a, b int) int {
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
		return 0
	}
	compareFloat := func(a, b sql.NullFloat64) int {
		if !a.Valid && !b.Valid {
			return 0
		}
		if !a.Valid {
			return -1
		}
		if !b.Valid {
			return 1
		}
		if a.Float64 < b.Float64 {
			return -1
		}
		if a.Float64 > b.Float64 {
			return 1
		}
		return 0
	}
	switch key {
	case "face_count":
		return compareInt(left.FaceCount, right.FaceCount)
	case "candidate_count":
		return compareInt(left.CandidateCount, right.CandidateCount)
	case "person":
		return strings.Compare(strings.ToLower(left.ProposedPersonName), strings.ToLower(right.ProposedPersonName))
	case "rating":
		if cmp := compareFloat(left.Purity, right.Purity); cmp != 0 {
			return cmp
		}
		return compareFloat(left.Connectivity, right.Connectivity)
	case "review_status":
		if cmp := strings.Compare(left.ReviewStatus, right.ReviewStatus); cmp != 0 {
			return cmp
		}
		return compareInt(left.ToAssign, right.ToAssign)
	case "to_assign":
		return compareInt(left.ToAssign, right.ToAssign)
	case "cluster_key":
		return strings.Compare(left.Key, right.Key)
	default:
		return compareInt(boolInt(left.Mixed), boolInt(right.Mixed))
	}
}

func matchesClusterRating(row clusterListRow, rating string) bool {
	if rating == "" {
		return true
	}
	purity, connectivity := 0.0, 0.0
	if row.Purity.Valid {
		purity = row.Purity.Float64
	}
	if row.Connectivity.Valid {
		connectivity = row.Connectivity.Float64
	}
	switch rating {
	case "high":
		return purity >= .8 && connectivity >= .8
	case "medium":
		return purity >= .5 && purity < .8 && connectivity >= .5
	case "low":
		return purity < .5 || connectivity < .5
	default:
		return true
	}
}

func renderClusterPager(body *strings.Builder, r *http.Request, page, pageCount, perPage int) {
	pageURL := func(target int) string {
		values := url.Values{}
		for key, list := range r.URL.Query() {
			for _, value := range list {
				values.Add(key, value)
			}
		}
		values.Set("page", strconv.Itoa(target))
		values.Set("per_page", strconv.Itoa(perPage))
		return "/review/clusters?" + values.Encode()
	}
	values := r.URL.Query()
	body.WriteString(`<nav class="pager clusters-page-pager"><a href="` + template.HTMLEscapeString(pageURL(1)) + `">First</a> <a href="` + template.HTMLEscapeString(pageURL(maxInt(1, page-1))) + `">Previous</a> <span>Page ` + strconv.Itoa(page) + `/` + strconv.Itoa(pageCount) + `</span> <a href="` + template.HTMLEscapeString(pageURL(minInt(pageCount, page+1))) + `">Next</a> <a href="` + template.HTMLEscapeString(pageURL(pageCount)) + `">Last</a> <form class="page-jump" method="get"><label>Jump to Page <input type="number" name="page" min="1" value="` + strconv.Itoa(page) + `"></label><input type="hidden" name="per_page" value="` + strconv.Itoa(perPage) + `">`)
	for key, list := range values {
		if key == "page" || key == "per_page" {
			continue
		}
		for _, value := range list {
			body.WriteString(`<input type="hidden" name="` + template.HTMLEscapeString(key) + `" value="` + template.HTMLEscapeString(value) + `">`)
		}
	}
	body.WriteString(`</form></nav>`)
}

func placeholders(count int) string { return strings.TrimRight(strings.Repeat("?,", count), ",") }

func parseIntFilter(value string) int { result, _ := strconv.Atoi(value); return result }

func parseScoreFilter(value string) *float64 {
	if value == "" {
		return nil
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return nil
	}
	number /= 100
	return &number
}

func validPerPage(value string) int {
	parsed := parseIntFilter(value)
	for _, size := range []int{10, 20, 25, 50, 100} {
		if parsed == size {
			return size
		}
	}
	return 10
}

func validPage(value string) int {
	parsed := parseIntFilter(value)
	if parsed < 1 {
		return 1
	}
	return parsed
}

func clusterOption(value, label, selected string) string {
	return `<option value="` + template.HTMLEscapeString(value) + `"` + selectedAttr(value, selected) + `>` + template.HTMLEscapeString(label) + `</option>`
}

func validRunStatus(value string) bool {
	switch value {
	case "active", "queued", "running", "completed", "superseded", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func humanRunStatus(value string) string {
	switch value {
	case "active":
		return "Active"
	case "queued":
		return "Queued"
	case "running":
		return "Running"
	case "completed":
		return "Completed"
	case "superseded":
		return "Superseded"
	case "failed":
		return "Failed"
	case "cancelled":
		return "Cancelled"
	default:
		return value
	}
}

func humanReviewStatus(value string) string { return strings.ReplaceAll(value, "-", " ") }

func clusterType(runStatus string) string {
	if runStatus == "active" {
		return "Active"
	}
	if runStatus == "superseded" {
		return "Superseded"
	}
	return "Historical"
}

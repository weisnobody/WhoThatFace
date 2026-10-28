// Package search implements database-backed photo and face search filters.
package search

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type Options struct {
	AllowedCollectionIDs  []string
	CollectionID          string
	PersonID              string
	RelativePrefix        string
	CaptureDateFrom       string
	CaptureDateUntil      string
	IncludeProvisional    bool
	IncludeUnknown        bool
	IncludeAmbiguous      bool
	IncludeDontTrack      bool
	OnlyConfirmed         bool
	OnlyUnconfirmed       bool
	ReviewMode            bool
	IncludeAssignedSystem bool
	IncludeAssignedUser   bool
	AssignmentStates      []string
}

type Photo struct {
	PathID       string `json:"path_id"`
	CollectionID string `json:"collection_id"`
	RelativePath string `json:"relative_path"`
	AssetID      string `json:"asset_id"`
	CaptureDate  string `json:"capture_date,omitempty"`
	FaceCount    int    `json:"face_count"`
}

// ReviewPhotoOptions describes the filters used by the authenticated photo
// review page. It intentionally remains separate from Options, which is also
// used by the public JSON search endpoint.
type ReviewPhotoOptions struct {
	AllowedCollectionIDs []string
	CollectionID         string
	PathQuery            string
	FaceMode             string // all, with, without
	AssignmentStates     []string
	StatusFilters        []string // dont-track, unknown, possibles
	PersonQuery          string
}

type ReviewPhotoFace struct {
	ID             string
	Index          int
	State          string
	PersonID       string
	PersonName     string
	Disposition    string
	PossibleCount  int
	CandidateCount int
}

type ReviewPhoto struct {
	Photo
	Faces            []ReviewPhotoFace
	AssignedCount    int
	NeedsReviewCount int
	PersonSortName   string
}

type Store struct{ db *sql.DB }

func NewStore(database *sql.DB) Store { return Store{db: database} }

func placeholders(count int) string {
	values := make([]string, count)
	for index := range values {
		values[index] = "?"
	}
	return strings.Join(values, ",")
}

// ReviewPhotos returns permission-scoped photo rows and all faces belonging to
// each returned photo. The caller supplies the accessible collection IDs; a
// nil slice means unrestricted access, while an empty slice means no access.
func (s Store) ReviewPhotos(ctx context.Context, options ReviewPhotoOptions) ([]ReviewPhoto, error) {
	query := strings.Builder{}
	query.WriteString(`SELECT pp.id, pp.collection_id, pp.relative_path, pp.asset_id,
		COALESCE(pa.capture_date, ''), COUNT(DISTINCT f.id),
		COUNT(DISTINCT CASE WHEN ca.state IN ('assigned-user', 'assigned-system') THEN f.id END),
		COUNT(DISTINCT CASE WHEN COALESCE(ca.state, 'unassigned') NOT IN ('assigned-user', 'assigned-system') THEN f.id END),
		COALESCE(MIN(CASE WHEN p.name IS NOT NULL AND trim(p.name) <> '' THEN lower(p.name) ELSE lower(p.id) END), '')
		FROM photo_paths pp
		JOIN photo_assets pa ON pa.id = pp.asset_id
		LEFT JOIN analyses a ON a.asset_id = pa.id AND a.status IN ('completed', 'completed_with_errors')
		LEFT JOIN faces f ON f.analysis_id = a.id
		LEFT JOIN current_face_assignments ca ON ca.face_id = f.id
		LEFT JOIN people p ON p.id = ca.person_id
		WHERE pp.missing_at IS NULL`)
	args := []any{}
	add := func(condition string, values ...any) {
		query.WriteString(" AND ")
		query.WriteString(condition)
		args = append(args, values...)
	}
	if options.CollectionID != "" {
		add("pp.collection_id = ?", options.CollectionID)
	}
	if options.AllowedCollectionIDs != nil {
		if len(options.AllowedCollectionIDs) == 0 {
			query.WriteString(" AND 1 = 0")
		} else {
			query.WriteString(" AND pp.collection_id IN (" + placeholders(len(options.AllowedCollectionIDs)) + ")")
			for _, id := range options.AllowedCollectionIDs {
				args = append(args, id)
			}
		}
	}
	if value := strings.TrimSpace(options.PathQuery); value != "" {
		add("lower(pp.relative_path) LIKE lower(?)", "%"+value+"%")
	}
	if options.FaceMode == "with" {
		query.WriteString(` AND EXISTS (SELECT 1 FROM analyses fa JOIN faces ff ON ff.analysis_id = fa.id WHERE fa.asset_id = pp.asset_id AND fa.status IN ('completed', 'completed_with_errors'))`)
	} else if options.FaceMode == "without" {
		query.WriteString(` AND NOT EXISTS (SELECT 1 FROM analyses fa JOIN faces ff ON ff.analysis_id = fa.id WHERE fa.asset_id = pp.asset_id AND fa.status IN ('completed', 'completed_with_errors'))`)
	}
	if len(options.AssignmentStates) > 0 {
		query.WriteString(` AND EXISTS (SELECT 1 FROM analyses sa JOIN faces sf ON sf.analysis_id = sa.id LEFT JOIN current_face_assignments sfa ON sfa.face_id = sf.id WHERE sa.asset_id = pp.asset_id AND sa.status IN ('completed', 'completed_with_errors') AND COALESCE(sfa.state, 'unassigned') IN (` + placeholders(len(options.AssignmentStates)) + `))`)
		for _, state := range options.AssignmentStates {
			args = append(args, state)
		}
	}
	if len(options.StatusFilters) > 0 {
		conditions := make([]string, 0, len(options.StatusFilters))
		for _, filter := range options.StatusFilters {
			switch filter {
			case "dont-track":
				conditions = append(conditions, `EXISTS (SELECT 1 FROM analyses da JOIN faces df ON df.analysis_id = da.id JOIN current_face_dispositions dd ON dd.face_id = df.id WHERE da.asset_id = pp.asset_id AND da.status IN ('completed', 'completed_with_errors') AND dd.disposition = 'dont-track')`)
			case "unknown":
				conditions = append(conditions, `EXISTS (SELECT 1 FROM analyses ua JOIN faces uf ON uf.analysis_id = ua.id LEFT JOIN current_face_assignments ufa ON ufa.face_id = uf.id WHERE ua.asset_id = pp.asset_id AND ua.status IN ('completed', 'completed_with_errors') AND COALESCE(ufa.state, 'unassigned') = 'unknown')`)
			case "possibles":
				conditions = append(conditions, `EXISTS (SELECT 1 FROM analyses pca JOIN faces pcf ON pcf.analysis_id = pca.id JOIN possible_face_candidates pcc ON pcc.face_id = pcf.id AND pcc.is_current = 1 WHERE pca.asset_id = pp.asset_id AND pca.status IN ('completed', 'completed_with_errors'))`)
			}
		}
		if len(conditions) > 0 {
			query.WriteString(" AND (" + strings.Join(conditions, " OR ") + ")")
		}
	}
	if value := strings.TrimSpace(options.PersonQuery); value != "" {
		like := "%" + value + "%"
		query.WriteString(` AND EXISTS (SELECT 1 FROM analyses qa JOIN faces qf ON qf.analysis_id = qa.id
			LEFT JOIN current_face_assignments qca ON qca.face_id = qf.id
			LEFT JOIN people qp ON qp.id = qca.person_id
			WHERE qa.asset_id = pp.asset_id AND qa.status IN ('completed', 'completed_with_errors') AND
			(lower(COALESCE(qp.id, '')) LIKE lower(?) OR lower(COALESCE(qp.name, '')) LIKE lower(?) OR
			 EXISTS (SELECT 1 FROM recognition_candidates qrc JOIN people qrp ON qrp.id = qrc.person_id WHERE qrc.face_id = qf.id AND (lower(qrc.person_id) LIKE lower(?) OR lower(COALESCE(qrp.name, '')) LIKE lower(?))) OR
			 EXISTS (SELECT 1 FROM possible_face_candidates qpc JOIN people qpp ON qpp.id = qpc.person_id WHERE qpc.face_id = qf.id AND qpc.is_current = 1 AND (lower(qpc.person_id) LIKE lower(?) OR lower(COALESCE(qpp.name, '')) LIKE lower(?))))`)
		args = append(args, like, like, like, like, like, like)
	}
	query.WriteString(" GROUP BY pp.id, pp.collection_id, pp.relative_path, pp.asset_id, pa.capture_date ORDER BY pp.relative_path, pp.id")
	rows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("review photo search: %w", err)
	}
	defer rows.Close()
	var result []ReviewPhoto
	for rows.Next() {
		var photo ReviewPhoto
		if err := rows.Scan(&photo.PathID, &photo.CollectionID, &photo.RelativePath, &photo.AssetID, &photo.CaptureDate, &photo.FaceCount, &photo.AssignedCount, &photo.NeedsReviewCount, &photo.PersonSortName); err != nil {
			return nil, err
		}
		faces, err := s.reviewPhotoFaces(ctx, photo.AssetID)
		if err != nil {
			return nil, err
		}
		photo.Faces = faces
		result = append(result, photo)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s Store) reviewPhotoFaces(ctx context.Context, assetID string) ([]ReviewPhotoFace, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT f.id, f.face_index, COALESCE(ca.state, 'unassigned'), COALESCE(ca.person_id, ''), COALESCE(NULLIF(p.name, ''), substr(ca.person_id, -8), ''), COALESCE(cd.disposition, ''),
		(SELECT COUNT(*) FROM possible_face_candidates pc WHERE pc.face_id = f.id AND pc.is_current = 1),
		(SELECT COUNT(*) FROM recognition_candidates rc WHERE rc.face_id = f.id)
		FROM analyses a JOIN faces f ON f.analysis_id = a.id
		LEFT JOIN current_face_assignments ca ON ca.face_id = f.id
		LEFT JOIN people p ON p.id = ca.person_id
		LEFT JOIN current_face_dispositions cd ON cd.face_id = f.id
		WHERE a.asset_id = ? AND a.status IN ('completed', 'completed_with_errors') ORDER BY f.face_index`, assetID)
	if err != nil {
		return nil, fmt.Errorf("review photo faces: %w", err)
	}
	defer rows.Close()
	var result []ReviewPhotoFace
	for rows.Next() {
		var face ReviewPhotoFace
		if err := rows.Scan(&face.ID, &face.Index, &face.State, &face.PersonID, &face.PersonName, &face.Disposition, &face.PossibleCount, &face.CandidateCount); err != nil {
			return nil, err
		}
		result = append(result, face)
	}
	return result, rows.Err()
}

func (s Store) Photos(ctx context.Context, options Options) ([]Photo, error) {
	query := strings.Builder{}
	query.WriteString(`SELECT pp.id, pp.collection_id, pp.relative_path, pp.asset_id, COALESCE(pa.capture_date, ''), COUNT(DISTINCT f.id)
		FROM photo_paths pp JOIN photo_assets pa ON pa.id = pp.asset_id
		LEFT JOIN analyses an ON an.asset_id = pa.id
		LEFT JOIN faces f ON f.analysis_id = an.id
		WHERE pp.missing_at IS NULL`)
	args := []any{}
	add := func(condition string, value any) {
		query.WriteString(" AND ")
		query.WriteString(condition)
		args = append(args, value)
	}
	if options.CollectionID != "" {
		add("pp.collection_id = ?", options.CollectionID)
	}
	if options.AllowedCollectionIDs != nil {
		if len(options.AllowedCollectionIDs) == 0 {
			query.WriteString(" AND 1 = 0")
		} else {
			placeholders := make([]string, len(options.AllowedCollectionIDs))
			for index, collectionID := range options.AllowedCollectionIDs {
				placeholders[index] = "?"
				args = append(args, collectionID)
			}
			query.WriteString(" AND pp.collection_id IN (" + strings.Join(placeholders, ",") + ")")
		}
	}
	if options.RelativePrefix != "" {
		prefix := strings.TrimSuffix(strings.ReplaceAll(options.RelativePrefix, "\\", "/"), "/") + "/%"
		add("pp.relative_path LIKE ?", prefix)
	}
	if options.CaptureDateFrom != "" {
		add("pa.capture_date >= ?", options.CaptureDateFrom)
	}
	if options.CaptureDateUntil != "" {
		add("pa.capture_date < ?", options.CaptureDateUntil)
	}
	if options.PersonID != "" {
		add(`EXISTS (SELECT 1 FROM faces pf JOIN analyses pan ON pan.id = pf.analysis_id JOIN current_face_assignments pca ON pca.face_id = pf.id WHERE pan.asset_id = pp.asset_id AND pca.person_id = ?)`, options.PersonID)
	}
	if len(options.AssignmentStates) > 0 {
		placeholders := make([]string, len(options.AssignmentStates))
		for index, state := range options.AssignmentStates {
			placeholders[index] = "?"
			args = append(args, state)
		}
		query.WriteString(` AND EXISTS (SELECT 1 FROM faces sf JOIN analyses san ON san.id = sf.analysis_id LEFT JOIN current_face_assignments sa ON sa.face_id = sf.id WHERE san.asset_id = pp.asset_id AND COALESCE(sa.state, 'unassigned') IN (` + strings.Join(placeholders, ",") + `))`)
	}
	if !options.IncludeDontTrack {
		query.WriteString(` AND NOT EXISTS (SELECT 1 FROM faces df JOIN analyses dan ON dan.id = df.analysis_id JOIN current_face_dispositions d ON d.face_id = df.id WHERE dan.asset_id = pp.asset_id AND d.disposition = 'dont-track')`)
	}
	if options.OnlyConfirmed {
		query.WriteString(` AND NOT EXISTS (SELECT 1 FROM faces cf JOIN analyses can ON can.id = cf.analysis_id JOIN current_face_assignments ca ON ca.face_id = cf.id WHERE can.asset_id = pp.asset_id AND ca.state <> 'assigned-user')`)
	}
	if options.OnlyUnconfirmed {
		query.WriteString(` AND EXISTS (SELECT 1 FROM faces uf JOIN analyses uan ON uan.id = uf.analysis_id JOIN current_face_assignments ua ON ua.face_id = uf.id WHERE uan.asset_id = pp.asset_id AND ua.state NOT IN ('assigned-user', 'assigned-system'))`)
	}
	if options.ReviewMode {
		states := []string{"'unassigned'", "'provisional'", "'unknown'", "'ambiguous'"}
		if options.IncludeAssignedSystem {
			states = append(states, "'assigned-system'")
		}
		if options.IncludeAssignedUser {
			states = append(states, "'assigned-user'")
		}
		query.WriteString(` AND EXISTS (SELECT 1 FROM faces rf JOIN analyses ran ON ran.id = rf.analysis_id LEFT JOIN current_face_assignments ra ON ra.face_id = rf.id WHERE ran.asset_id = pp.asset_id AND COALESCE(ra.state, 'unassigned') IN (` + strings.Join(states, ",") + `))`)
	}
	if options.ReviewMode {
		// Review mode selects the faces needing review above; the ordinary
		// state-exclusion filters would incorrectly remove those same photos.
		query.WriteString("")
	} else {
		if !options.IncludeProvisional {
			query.WriteString(` AND NOT EXISTS (SELECT 1 FROM faces pf2 JOIN analyses pan2 ON pan2.id = pf2.analysis_id JOIN current_face_assignments pa2 ON pa2.face_id = pf2.id WHERE pan2.asset_id = pp.asset_id AND pa2.state = 'provisional')`)
		}
		if !options.IncludeUnknown {
			query.WriteString(` AND NOT EXISTS (SELECT 1 FROM faces uf2 JOIN analyses uan2 ON uan2.id = uf2.analysis_id JOIN current_face_assignments ua2 ON ua2.face_id = uf2.id WHERE uan2.asset_id = pp.asset_id AND ua2.state = 'unknown')`)
		}
		if !options.IncludeAmbiguous {
			query.WriteString(` AND NOT EXISTS (SELECT 1 FROM faces af2 JOIN analyses aan2 ON aan2.id = af2.analysis_id JOIN current_face_assignments aa2 ON aa2.face_id = af2.id WHERE aan2.asset_id = pp.asset_id AND aa2.state = 'ambiguous')`)
		}
	}
	query.WriteString(" GROUP BY pp.id, pp.collection_id, pp.relative_path, pp.asset_id, pa.capture_date ORDER BY pp.relative_path")
	rows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("search photos: %w", err)
	}
	defer rows.Close()
	var result []Photo
	for rows.Next() {
		var photo Photo
		if err := rows.Scan(&photo.PathID, &photo.CollectionID, &photo.RelativePath, &photo.AssetID, &photo.CaptureDate, &photo.FaceCount); err != nil {
			return nil, err
		}
		result = append(result, photo)
	}
	return result, rows.Err()
}

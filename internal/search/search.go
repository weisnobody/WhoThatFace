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

type Store struct{ db *sql.DB }

func NewStore(database *sql.DB) Store { return Store{db: database} }

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
		query.WriteString(` AND NOT EXISTS (SELECT 1 FROM faces cf JOIN analyses can ON can.id = cf.analysis_id JOIN current_face_assignments ca ON ca.face_id = cf.id WHERE can.asset_id = pp.asset_id AND ca.state NOT IN ('assigned-user', 'assigned-system'))`)
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

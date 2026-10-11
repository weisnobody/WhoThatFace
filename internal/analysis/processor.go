package analysis

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/pjg/face_me_bro/internal/review"
	"github.com/pjg/face_me_bro/internal/worker"
)

type Analyzer interface {
	Analyze(context.Context, string, string, int64) (worker.Message, error)
}

type Processor struct {
	db         *sql.DB
	store      Store
	review     review.Store
	analyzer   Analyzer
	pipelineID string
	maxPixels  int64
}

func NewProcessor(database *sql.DB, analyzer Analyzer, pipelineID string, maxPixels int64) Processor {
	return Processor{db: database, store: NewStore(database), review: review.NewStore(database), analyzer: analyzer, pipelineID: pipelineID, maxPixels: maxPixels}
}

// ProcessCollection analyzes each distinct asset in a collection once, even
// when multiple paths point to the same content hash.
func (p Processor) ProcessCollection(ctx context.Context, collectionID string) error {
	return p.processCollection(ctx, collectionID, nil)
}

// ProcessCollectionWithProgress preserves successful asset work in job-item
// storage so a later retry can skip it. The callback is called once after an
// asset has been persisted successfully.
func (p Processor) ProcessCollectionWithProgress(ctx context.Context, collectionID string, progress func(string) error) error {
	return p.processCollection(ctx, collectionID, progress)
}

func (p Processor) processCollection(ctx context.Context, collectionID string, progress func(string) error) error {
	rows, err := p.db.QueryContext(ctx, `
		SELECT c.root_path, pp.relative_path, pa.id
		FROM photo_paths pp JOIN collections c ON c.id = pp.collection_id
		JOIN photo_assets pa ON pa.id = pp.asset_id
		WHERE pp.collection_id = ? AND pp.missing_at IS NULL
		ORDER BY pp.relative_path`, collectionID)
	if err != nil {
		return err
	}
	type assetPath struct{ root, relative, assetID string }
	var assets []assetPath
	seen := map[string]struct{}{}
	for rows.Next() {
		var value assetPath
		if err := rows.Scan(&value.root, &value.relative, &value.assetID); err != nil {
			rows.Close()
			return err
		}
		if _, exists := seen[value.assetID]; exists {
			continue
		}
		seen[value.assetID] = struct{}{}
		assets = append(assets, value)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, asset := range assets {
		var analysisStatus string
		err := p.db.QueryRowContext(ctx, `SELECT status FROM analyses WHERE asset_id = ? AND pipeline_id = ? LIMIT 1`, asset.assetID, p.pipelineID).Scan(&analysisStatus)
		if err == nil && analysisStatus == "completed" {
			if progress != nil {
				if progressErr := progress(asset.assetID); progressErr != nil {
					return progressErr
				}
			}
			continue
		}
		if err != sql.ErrNoRows {
			if err != nil && analysisStatus == "" {
				return err
			}
		}
		if err := p.store.MarkRunning(ctx, asset.assetID, p.pipelineID); err != nil {
			return err
		}
		response, err := p.analyzer.Analyze(ctx, filepath.Join(asset.root, filepath.FromSlash(asset.relative)), p.pipelineID, p.maxPixels)
		if err != nil {
			_ = p.store.RecordFailure(ctx, asset.assetID, p.pipelineID, "analysis_failed", err.Error())
			return fmt.Errorf("analyze %s: %w", asset.relative, err)
		}
		analysisID, err := p.store.Persist(ctx, asset.assetID, p.pipelineID, response)
		if err != nil {
			return err
		}
		faceRows, err := p.db.QueryContext(ctx, "SELECT id FROM faces WHERE analysis_id = ?", analysisID)
		if err != nil {
			return err
		}
		var faceIDs []string
		for faceRows.Next() {
			var faceID string
			if err := faceRows.Scan(&faceID); err != nil {
				faceRows.Close()
				return err
			}
			faceIDs = append(faceIDs, faceID)
		}
		if err := faceRows.Close(); err != nil {
			return err
		}
		for _, faceID := range faceIDs {
			if err := p.review.InitializeFace(ctx, faceID, review.Actor{Type: "system"}); err != nil {
				return err
			}
		}
		if progress != nil {
			if err := progress(asset.assetID); err != nil {
				return err
			}
		}
	}
	return nil
}

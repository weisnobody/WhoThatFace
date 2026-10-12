// Package representative maintains the UI-only representative image for each
// person.
package representative

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/pjg/face_me_bro/internal/audit"
	"github.com/pjg/face_me_bro/internal/review"
)

const CropVersion = "square-face-v1"

//go:embed crop.py
var cropScript embed.FS

var ErrNoUsableFace = errors.New("person has no usable face")

type Image struct {
	PersonID            string
	FaceID              string
	SelectionMode       string
	FaceQuality         float64
	DetectionConfidence float64
	CropVersion         string
	MIMEType            string
	Width               int
	Height              int
	Data                []byte
	UpdatedAt           string
}

type Store struct {
	db         *sql.DB
	pythonPath string
}

func NewStore(database *sql.DB) Store { return Store{db: database} }

func NewStoreWithPython(database *sql.DB, pythonPath string) Store {
	return Store{db: database, pythonPath: pythonPath}
}

// Get returns the current cached image without changing automatic selection.
func (s Store) Get(ctx context.Context, personID string) (Image, error) {
	var result Image
	err := s.db.QueryRowContext(ctx, `
		SELECT person_id, face_id, selection_mode, COALESCE(face_quality, 0),
		       COALESCE(detection_confidence, 0), crop_version, mime_type,
		       width, height, image, updated_at
		FROM person_representatives WHERE person_id = ?`, personID).Scan(
		&result.PersonID, &result.FaceID, &result.SelectionMode,
		&result.FaceQuality, &result.DetectionConfidence, &result.CropVersion,
		&result.MIMEType, &result.Width, &result.Height, &result.Data,
		&result.UpdatedAt)
	return result, err
}

// Ensure chooses and caches an automatic representative when needed. An
// explicit selection remains in force while its source face is available.
func (s Store) Ensure(ctx context.Context, personID string) (Image, error) {
	if current, err := s.Get(ctx, personID); err == nil {
		if current.SelectionMode == "explicit" {
			if _, err := s.sourceForFace(ctx, personID, current.FaceID); err == nil {
				return current, nil
			}
		} else {
			candidate, err := s.bestCandidate(ctx, personID)
			if err == nil && candidate.FaceID == current.FaceID {
				return current, nil
			}
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Image{}, err
	}
	candidate, err := s.bestCandidate(ctx, personID)
	if err != nil {
		return Image{}, err
	}
	return s.cache(ctx, personID, candidate, "automatic", review.Actor{Type: "system"})
}

// Select explicitly selects a currently associated face for a person. The
// image is generated immediately so a missing source fails the operation.
func (s Store) Select(ctx context.Context, personID, faceID string, actor review.Actor) (Image, error) {
	candidate, err := s.candidateForFace(ctx, personID, faceID)
	if err != nil {
		return Image{}, err
	}
	return s.cache(ctx, personID, candidate, "explicit", actor)
}

// Clear removes an explicit selection. The next Ensure call chooses the best
// automatic representative.
func (s Store) Clear(ctx context.Context, personID string, actor review.Actor) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM person_representatives WHERE person_id = ?`, personID)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return nil
	}
	return audit.NewStore(s.db).Record(ctx, audit.Event{
		AccountID: actor.AccountID, Action: "representative-clear", EntityType: "person", EntityID: personID,
		New: map[string]any{"selection_mode": "automatic"},
	})
}

type candidate struct {
	FaceID              string
	Quality             float64
	DetectionConfidence float64
	Left, Top           float64
	Right, Bottom       float64
	Root, Relative      string
}

func (s Store) bestCandidate(ctx context.Context, personID string) (candidate, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT f.id, COALESCE(f.quality, 0), COALESCE(f.detection_confidence, 0),
		       f.box_left, f.box_top, f.box_right, f.box_bottom
		FROM faces f
		JOIN current_face_assignments ca ON ca.face_id = f.id AND ca.person_id = ?
		LEFT JOIN current_face_references cr ON cr.face_id = f.id AND cr.person_id = ? AND cr.reference = 1
		WHERE NOT EXISTS (
			SELECT 1 FROM current_face_dispositions cd
			WHERE cd.face_id = f.id AND cd.disposition IN ('false-detection', 'incorrect-assignment')
		)
		ORDER BY CASE
			WHEN cr.face_id IS NOT NULL THEN 1
			WHEN ca.state = 'assigned-user' THEN 2
			WHEN ca.state = 'assigned-system' THEN 3
			WHEN ca.state = 'provisional' THEN 4
			ELSE 5 END,
			COALESCE(f.quality, 0) DESC,
			COALESCE(f.detection_confidence, 0) DESC,
			f.id`, personID, personID)
	if err != nil {
		return candidate{}, err
	}
	var candidates []candidate
	for rows.Next() {
		var value candidate
		if err := rows.Scan(&value.FaceID, &value.Quality, &value.DetectionConfidence, &value.Left, &value.Top, &value.Right, &value.Bottom); err != nil {
			return candidate{}, err
		}
		candidates = append(candidates, value)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return candidate{}, err
	}
	if err := rows.Close(); err != nil {
		return candidate{}, err
	}
	for _, value := range candidates {
		if err := s.populateSource(ctx, &value); err != nil {
			continue
		}
		return value, nil
	}
	return candidate{}, fmt.Errorf("%w: %q", ErrNoUsableFace, personID)
}

func (s Store) candidateForFace(ctx context.Context, personID, faceID string) (candidate, error) {
	var value candidate
	err := s.db.QueryRowContext(ctx, `
		SELECT f.id, COALESCE(f.quality, 0), COALESCE(f.detection_confidence, 0),
		       f.box_left, f.box_top, f.box_right, f.box_bottom
		FROM faces f JOIN current_face_assignments ca ON ca.face_id = f.id
		WHERE f.id = ? AND ca.person_id = ?`, faceID, personID).Scan(
		&value.FaceID, &value.Quality, &value.DetectionConfidence,
		&value.Left, &value.Top, &value.Right, &value.Bottom)
	if err != nil {
		return candidate{}, err
	}
	var blocked int
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM current_face_dispositions WHERE face_id = ? AND disposition IN ('false-detection', 'incorrect-assignment'))`, faceID).Scan(&blocked); err != nil {
		return candidate{}, err
	}
	if blocked == 1 {
		return candidate{}, fmt.Errorf("face %q cannot be a representative", faceID)
	}
	if err := s.populateSource(ctx, &value); err != nil {
		return candidate{}, err
	}
	return value, nil
}

func (s Store) populateSource(ctx context.Context, value *candidate) error {
	return s.db.QueryRowContext(ctx, `
		SELECT c.root_path, pp.relative_path
		FROM faces f JOIN analyses a ON a.id = f.analysis_id
		JOIN photo_assets pa ON pa.id = a.asset_id
		JOIN photo_paths pp ON pp.asset_id = pa.id
		JOIN collections c ON c.id = pp.collection_id
		WHERE f.id = ? AND pp.missing_at IS NULL
		ORDER BY pp.last_seen_at DESC, pp.id LIMIT 1`, value.FaceID).Scan(&value.Root, &value.Relative)
}

func (s Store) sourceForFace(ctx context.Context, personID, faceID string) (candidate, error) {
	return s.candidateForFace(ctx, personID, faceID)
}

func (s Store) cache(ctx context.Context, personID string, value candidate, mode string, actor review.Actor) (Image, error) {
	path := filepath.Join(value.Root, filepath.FromSlash(value.Relative))
	data, width, height, mimeType, err := crop(ctx, s.pythonPath, path, value.Left, value.Top, value.Right, value.Bottom)
	if err != nil {
		return Image{}, fmt.Errorf("create representative for face %q: %w", value.FaceID, err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO person_representatives
		(person_id, face_id, selection_mode, face_quality, detection_confidence,
		 crop_version, mime_type, width, height, image, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(person_id) DO UPDATE SET face_id = excluded.face_id,
		 selection_mode = excluded.selection_mode, face_quality = excluded.face_quality,
		 detection_confidence = excluded.detection_confidence,
		 crop_version = excluded.crop_version, mime_type = excluded.mime_type,
		 width = excluded.width, height = excluded.height, image = excluded.image,
		 updated_at = excluded.updated_at`, personID, value.FaceID, mode, value.Quality,
		value.DetectionConfidence, CropVersion, mimeType, width, height, data, now)
	if err != nil {
		return Image{}, err
	}
	_ = audit.NewStore(s.db).Record(ctx, audit.Event{AccountID: actor.AccountID, Action: "representative-" + mode, EntityType: "person", EntityID: personID, New: map[string]any{"face_id": value.FaceID, "selection_mode": mode}})
	return s.Get(ctx, personID)
}

func crop(ctx context.Context, pythonPath, path string, left, top, right, bottom float64) ([]byte, int, int, string, error) {
	if pythonPath != "" {
		return cropWithPython(ctx, pythonPath, path, left, top, right, bottom)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, "", err
	}
	defer file.Close()
	source, _, err := image.Decode(file)
	if err != nil {
		return nil, 0, 0, "", err
	}
	width, height := source.Bounds().Dx(), source.Bounds().Dy()
	if width == 0 || height == 0 {
		return nil, 0, 0, "", fmt.Errorf("source image is empty")
	}
	boxWidth, boxHeight := right-left, bottom-top
	if boxWidth <= 0 || boxHeight <= 0 {
		return nil, 0, 0, "", fmt.Errorf("face bounding box is empty")
	}
	left -= boxWidth * 0.20
	top -= boxHeight * 0.20
	right += boxWidth * 0.20
	bottom += boxHeight * 0.20
	square := right - left
	if bottom-top > square {
		square = bottom - top
	}
	left -= (square - (right - left)) / 2
	top -= (square - (bottom - top)) / 2
	if left < 0 {
		left = 0
	}
	if top < 0 {
		top = 0
	}
	if left+square > float64(width) {
		left = float64(width) - square
	}
	if top+square > float64(height) {
		top = float64(height) - square
	}
	if square > float64(width) {
		left, square = 0, float64(width)
	}
	if square > float64(height) {
		top, square = 0, float64(height)
	}
	if square <= 0 {
		return nil, 0, 0, "", fmt.Errorf("face crop is outside source image")
	}
	minX, minY := int(left), int(top)
	maxX, maxY := int(left+square), int(top+square)
	if maxX > width {
		maxX = width
	}
	if maxY > height {
		maxY = height
	}
	outputSize := int(square)
	if outputSize > 96 {
		outputSize = 96
	}
	if outputSize < 1 {
		outputSize = 1
	}
	resized := image.NewRGBA(image.Rect(0, 0, outputSize, outputSize))
	for y := 0; y < outputSize; y++ {
		for x := 0; x < outputSize; x++ {
			sx := minX + x*(maxX-minX)/outputSize
			sy := minY + y*(maxY-minY)/outputSize
			resized.Set(x, y, source.At(sx, sy))
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, resized); err != nil {
		return nil, 0, 0, "", err
	}
	return output.Bytes(), outputSize, outputSize, "image/png", nil
}

func cropWithPython(ctx context.Context, pythonPath, path string, left, top, right, bottom float64) ([]byte, int, int, string, error) {
	script, err := cropScript.ReadFile("crop.py")
	if err != nil {
		return nil, 0, 0, "", err
	}
	command := exec.CommandContext(ctx, pythonPath, "-", path,
		strconv.FormatFloat(left, 'g', -1, 64), strconv.FormatFloat(top, 'g', -1, 64),
		strconv.FormatFloat(right, 'g', -1, 64), strconv.FormatFloat(bottom, 'g', -1, 64))
	command.Stdin = bytes.NewReader(script)
	output, err := command.Output()
	if err != nil {
		return nil, 0, 0, "", fmt.Errorf("Python representative crop: %w", err)
	}
	var result struct {
		Data     string `json:"data"`
		Width    int    `json:"width"`
		Height   int    `json:"height"`
		MIMEType string `json:"mime_type"`
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, 0, 0, "", fmt.Errorf("decode Python representative crop: %w", err)
	}
	data, err := base64.StdEncoding.DecodeString(result.Data)
	if err != nil {
		return nil, 0, 0, "", fmt.Errorf("decode representative image: %w", err)
	}
	return data, result.Width, result.Height, result.MIMEType, nil
}

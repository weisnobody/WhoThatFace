// Package config resolves application configuration and runtime paths.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/pjg/face_me_bro/internal/clustering"
	"github.com/pjg/face_me_bro/internal/recognition"
)

const (
	defaultConfigFile   = "config.json"
	defaultDatabase     = "face_me_bro.sqlite3"
	defaultSearchMethod = "exact"
)

// File contains settings that may be supplied by a JSON configuration file.
// Empty paths use the runtime data root and its standard subdirectories.
type File struct {
	RuntimeDataRoot               string  `json:"runtime_data_root,omitempty"`
	DatabasePath                  string  `json:"database_path,omitempty"`
	ModelRoot                     string  `json:"model_root,omitempty"`
	LogRoot                       string  `json:"log_root,omitempty"`
	TempRoot                      string  `json:"temp_root,omitempty"`
	ExportRoot                    string  `json:"export_root,omitempty"`
	BackupRoot                    string  `json:"backup_root,omitempty"`
	NeighborSearchMethod          string  `json:"neighbor_search_method,omitempty"`
	RecognitionHighSimilarity     float32 `json:"recognition_high_similarity,omitempty"`
	RecognitionMediumSimilarity   float32 `json:"recognition_medium_similarity,omitempty"`
	RecognitionLowSimilarity      float32 `json:"recognition_low_similarity,omitempty"`
	RecognitionMargin             float32 `json:"recognition_margin,omitempty"`
	RecognitionHighSupport        int     `json:"recognition_high_support,omitempty"`
	RecognitionMediumSupport      int     `json:"recognition_medium_support,omitempty"`
	RecognitionHighQuality        float32 `json:"recognition_high_quality,omitempty"`
	RecognitionMediumQuality      float32 `json:"recognition_medium_quality,omitempty"`
	RecognitionHighDetection      float32 `json:"recognition_high_detection,omitempty"`
	RecognitionMediumDetection    float32 `json:"recognition_medium_detection,omitempty"`
	RecognitionHighPurity         float32 `json:"recognition_high_purity,omitempty"`
	RecognitionMediumPurity       float32 `json:"recognition_medium_purity,omitempty"`
	RecognitionHighConnectivity   float32 `json:"recognition_high_connectivity,omitempty"`
	RecognitionMediumConnectivity float32 `json:"recognition_medium_connectivity,omitempty"`
	RecognitionRequireAgreement   *bool   `json:"recognition_require_agreement,omitempty"`
	ClusteringK                   int     `json:"clustering_k,omitempty"`
	ClusteringSimilarityThreshold float32 `json:"clustering_similarity_threshold,omitempty"`
	ClusteringCandidateScope      string  `json:"clustering_candidate_scope,omitempty"`
}

// Resolved is the complete configuration used by application services.
type Resolved struct {
	ConfigFile            string
	RuntimeDataRoot       string
	DatabasePath          string
	ModelRoot             string
	LogRoot               string
	TempRoot              string
	ExportRoot            string
	BackupRoot            string
	NeighborSearchMethod  string
	RecognitionThresholds recognition.Thresholds
	ClusteringConfig      clustering.Config
}

// DefaultConfigFile returns the default configuration file for the current
// operating system. The file is not created by this package.
func DefaultConfigFile() (string, error) {
	root, err := defaultRuntimeDataRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, defaultConfigFile), nil
}

// Load reads configPath, or applies defaults when configPath is empty. A path
// naming a directory refers to config.json inside that directory.
func Load(configPath string) (Resolved, error) {
	defaultFile, err := DefaultConfigFile()
	if err != nil {
		return Resolved{}, err
	}

	if configPath == "" {
		configPath = defaultFile
	} else {
		configPath, err = filepath.Abs(configPath)
		if err != nil {
			return Resolved{}, fmt.Errorf("resolve config path: %w", err)
		}
		if info, statErr := os.Stat(configPath); statErr == nil && info.IsDir() {
			configPath = filepath.Join(configPath, defaultConfigFile)
		}
	}

	settings := File{}
	data, readErr := os.ReadFile(configPath)
	if readErr == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			return Resolved{}, fmt.Errorf("parse configuration %q: %w", configPath, err)
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return Resolved{}, fmt.Errorf("read configuration %q: %w", configPath, readErr)
	}

	root := settings.RuntimeDataRoot
	if root == "" {
		root, err = defaultRuntimeDataRoot()
		if err != nil {
			return Resolved{}, err
		}
	}
	root, err = absolutePath(root, filepath.Dir(configPath))
	if err != nil {
		return Resolved{}, fmt.Errorf("resolve runtime data root: %w", err)
	}

	thresholds := recognition.DefaultThresholds()
	if settings.RecognitionHighSimilarity != 0 {
		thresholds.HighSimilarity = settings.RecognitionHighSimilarity
	}
	if settings.RecognitionMediumSimilarity != 0 {
		thresholds.MediumSimilarity = settings.RecognitionMediumSimilarity
	}
	if settings.RecognitionLowSimilarity != 0 {
		thresholds.LowSimilarity = settings.RecognitionLowSimilarity
	}
	if settings.RecognitionMargin != 0 {
		thresholds.Margin = settings.RecognitionMargin
	}
	if settings.RecognitionHighSupport != 0 {
		thresholds.HighSupport = settings.RecognitionHighSupport
	}
	if settings.RecognitionMediumSupport != 0 {
		thresholds.MediumSupport = settings.RecognitionMediumSupport
	}
	if settings.RecognitionHighQuality != 0 {
		thresholds.HighQuality = settings.RecognitionHighQuality
	}
	if settings.RecognitionMediumQuality != 0 {
		thresholds.MediumQuality = settings.RecognitionMediumQuality
	}
	if settings.RecognitionHighDetection != 0 {
		thresholds.HighDetection = settings.RecognitionHighDetection
	}
	if settings.RecognitionMediumDetection != 0 {
		thresholds.MediumDetection = settings.RecognitionMediumDetection
	}
	if settings.RecognitionHighPurity != 0 {
		thresholds.HighPurity = settings.RecognitionHighPurity
	}
	if settings.RecognitionMediumPurity != 0 {
		thresholds.MediumPurity = settings.RecognitionMediumPurity
	}
	if settings.RecognitionHighConnectivity != 0 {
		thresholds.HighConnectivity = settings.RecognitionHighConnectivity
	}
	if settings.RecognitionMediumConnectivity != 0 {
		thresholds.MediumConnectivity = settings.RecognitionMediumConnectivity
	}
	if settings.RecognitionRequireAgreement != nil {
		thresholds.RequireAgreement = *settings.RecognitionRequireAgreement
	}
	clusteringConfig := clustering.DefaultConfig()
	if settings.ClusteringK != 0 {
		clusteringConfig.K = settings.ClusteringK
	}
	if settings.ClusteringSimilarityThreshold != 0 {
		clusteringConfig.SimilarityThreshold = settings.ClusteringSimilarityThreshold
	}
	if settings.ClusteringCandidateScope != "" {
		clusteringConfig.CandidateScope = settings.ClusteringCandidateScope
	}
	clusteringConfig.NeighborSearchMethod = searchMethod(settings.NeighborSearchMethod)
	return Resolved{
		ConfigFile:            configPath,
		RuntimeDataRoot:       root,
		DatabasePath:          resolvePath(settings.DatabasePath, root, defaultDatabase),
		ModelRoot:             resolvePath(settings.ModelRoot, root, "models"),
		LogRoot:               resolvePath(settings.LogRoot, root, "logs"),
		TempRoot:              resolvePath(settings.TempRoot, root, "tmp"),
		ExportRoot:            resolvePath(settings.ExportRoot, root, "exports"),
		BackupRoot:            resolvePath(settings.BackupRoot, root, "backups"),
		NeighborSearchMethod:  searchMethod(settings.NeighborSearchMethod),
		RecognitionThresholds: thresholds,
		ClusteringConfig:      clusteringConfig,
	}, nil
}

func searchMethod(value string) string {
	if value == "ann" {
		return value
	}
	return defaultSearchMethod
}

func defaultRuntimeDataRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find user home directory: %w", err)
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "face_me_bro"), nil
	case "windows":
		if appData := os.Getenv("AppData"); appData != "" {
			return filepath.Join(appData, "face_me_bro"), nil
		}
		return filepath.Join(home, "AppData", "Roaming", "face_me_bro"), nil
	default:
		if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
			return filepath.Join(dataHome, "face_me_bro"), nil
		}
		return filepath.Join(home, ".local", "share", "face_me_bro"), nil
	}
}

func resolvePath(value, root, defaultName string) string {
	if value == "" {
		value = defaultName
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Join(root, value)
}

func absolutePath(value, base string) (string, error) {
	if filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	return filepath.Abs(filepath.Join(base, value))
}

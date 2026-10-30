// Package config resolves application configuration and runtime paths.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/weisnobody/whothatface/internal/candidates"
	"github.com/weisnobody/whothatface/internal/clustering"
	"github.com/weisnobody/whothatface/internal/recognition"
)

const (
	defaultConfigFile   = "config.json"
	configEnvironment   = "WTFACE_CONFIG"
	defaultDatabase     = "whothatface.sqlite3"
	defaultSearchMethod = "exact"
	defaultServeAddress = "127.0.0.1:8080"
	defaultProvider     = "CPUExecutionProvider"
	defaultMaxPixels    = int64(100_000_000)
)

// File contains settings that may be supplied by a JSON configuration file.
// Empty paths use the runtime data root and its standard subdirectories.
type File struct {
	RuntimeDataRoot                       string  `json:"runtime_data_root,omitempty"`
	DatabasePath                          string  `json:"database_path,omitempty"`
	ModelRoot                             string  `json:"model_root,omitempty"`
	PythonPath                            string  `json:"python_path,omitempty"`
	Provider                              string  `json:"provider,omitempty"`
	ServeAddress                          string  `json:"serve_address,omitempty"`
	LogRoot                               string  `json:"log_root,omitempty"`
	TempRoot                              string  `json:"temp_root,omitempty"`
	ExportRoot                            string  `json:"export_root,omitempty"`
	BackupRoot                            string  `json:"backup_root,omitempty"`
	AnalysisMaxPixels                     int64   `json:"analysis_max_pixels,omitempty"`
	AnalysisMaxWidth                      int     `json:"analysis_max_width,omitempty"`
	AnalysisMaxHeight                     int     `json:"analysis_max_height,omitempty"`
	AnalysisMaxPixelsOverride             int64   `json:"analysis_max_pixels_override,omitempty"`
	AnalysisMaxWidthOverride              int     `json:"analysis_max_width_override,omitempty"`
	AnalysisMaxHeightOverride             int     `json:"analysis_max_height_override,omitempty"`
	NeighborSearchMethod                  string  `json:"neighbor_search_method,omitempty"`
	RecognitionHighSimilarity             float32 `json:"recognition_high_similarity,omitempty"`
	RecognitionDirectAssignmentSimilarity float32 `json:"recognition_direct_assignment_similarity,omitempty"`
	RecognitionMediumSimilarity           float32 `json:"recognition_medium_similarity,omitempty"`
	RecognitionLowSimilarity              float32 `json:"recognition_low_similarity,omitempty"`
	RecognitionMargin                     float32 `json:"recognition_margin,omitempty"`
	RecognitionHighSupport                int     `json:"recognition_high_support,omitempty"`
	RecognitionMediumSupport              int     `json:"recognition_medium_support,omitempty"`
	RecognitionHighQuality                float32 `json:"recognition_high_quality,omitempty"`
	RecognitionMediumQuality              float32 `json:"recognition_medium_quality,omitempty"`
	RecognitionHighDetection              float32 `json:"recognition_high_detection,omitempty"`
	RecognitionMediumDetection            float32 `json:"recognition_medium_detection,omitempty"`
	RecognitionHighPurity                 float32 `json:"recognition_high_purity,omitempty"`
	RecognitionMediumPurity               float32 `json:"recognition_medium_purity,omitempty"`
	RecognitionHighConnectivity           float32 `json:"recognition_high_connectivity,omitempty"`
	RecognitionMediumConnectivity         float32 `json:"recognition_medium_connectivity,omitempty"`
	RecognitionRequireAgreement           *bool   `json:"recognition_require_agreement,omitempty"`
	ClusteringK                           int     `json:"clustering_k,omitempty"`
	ClusteringSimilarityThreshold         float32 `json:"clustering_similarity_threshold,omitempty"`
	ClusteringCandidateScope              string  `json:"clustering_candidate_scope,omitempty"`
	PossibleMaxProvisionalFaces           int     `json:"possible_max_provisional_faces,omitempty"`
	PossibleMaxTemporaryFaces             int     `json:"possible_max_temporary_faces,omitempty"`
	PossibleMaxCandidates                 int     `json:"possible_max_candidates,omitempty"`
	PossibleMinSimilarity                 float32 `json:"possible_min_similarity,omitempty"`
	PossibleIncludeDontTrack              *bool   `json:"possible_include_dont_track,omitempty"`
}

// Resolved is the complete configuration used by application services.
type Resolved struct {
	ConfigFile                string
	RuntimeDataRoot           string
	DatabasePath              string
	ModelRoot                 string
	PythonPath                string
	Provider                  string
	ServeAddress              string
	LogRoot                   string
	TempRoot                  string
	ExportRoot                string
	BackupRoot                string
	AnalysisMaxPixels         int64
	AnalysisMaxWidth          int
	AnalysisMaxHeight         int
	AnalysisMaxPixelsOverride int64
	AnalysisMaxWidthOverride  int
	AnalysisMaxHeightOverride int
	NeighborSearchMethod      string
	RecognitionThresholds     recognition.Thresholds
	ClusteringConfig          clustering.Config
	PossibleCandidates        candidates.Config
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
	environmentPath := strings.TrimSpace(os.Getenv(configEnvironment))
	if configPath != "" && environmentPath != "" {
		cliFile, err := configFilePath(configPath)
		if err != nil {
			return Resolved{}, fmt.Errorf("resolve CLI configuration path: %w", err)
		}
		environmentFile, err := configFilePath(environmentPath)
		if err != nil {
			return Resolved{}, fmt.Errorf("resolve %s: %w", configEnvironment, err)
		}
		if cliFile != environmentFile {
			return Resolved{}, fmt.Errorf("configuration specified by --config (%q) conflicts with %s (%q); specify only one configuration file", cliFile, configEnvironment, environmentFile)
		}
	}
	if configPath == "" && environmentPath != "" {
		configPath = environmentPath
	}
	defaultFile, err := DefaultConfigFile()
	if err != nil {
		return Resolved{}, err
	}

	if configPath == "" {
		configPath = defaultFile
	} else {
		configPath, err = configFilePath(configPath)
		if err != nil {
			return Resolved{}, fmt.Errorf("resolve config path: %w", err)
		}
	}

	settings := File{}
	data, readErr := os.ReadFile(configPath)
	if readErr == nil {
		data, err = parseJSONC(data)
		if err != nil {
			return Resolved{}, fmt.Errorf("parse configuration %q: %w", configPath, err)
		}
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
	if settings.RecognitionDirectAssignmentSimilarity != 0 {
		thresholds.DirectAssignmentSimilarity = settings.RecognitionDirectAssignmentSimilarity
	}
	if thresholds.DirectAssignmentSimilarity <= thresholds.HighSimilarity {
		return Resolved{}, fmt.Errorf("recognition_direct_assignment_similarity (%.3f) must be greater than recognition_high_similarity (%.3f)", thresholds.DirectAssignmentSimilarity, thresholds.HighSimilarity)
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
	clusteringConfig.RecognitionThresholds = thresholds
	possibleConfig := candidates.DefaultConfig()
	if settings.PossibleMaxProvisionalFaces != 0 {
		possibleConfig.MaxProvisionalFaces = settings.PossibleMaxProvisionalFaces
	}
	if settings.PossibleMaxTemporaryFaces != 0 {
		possibleConfig.MaxTemporaryFaces = settings.PossibleMaxTemporaryFaces
	}
	if settings.PossibleMaxCandidates != 0 {
		possibleConfig.MaxCandidates = settings.PossibleMaxCandidates
	}
	if settings.PossibleMinSimilarity != 0 {
		possibleConfig.MinSimilarity = settings.PossibleMinSimilarity
	}
	if settings.PossibleIncludeDontTrack != nil {
		possibleConfig.IncludeDontTrack = *settings.PossibleIncludeDontTrack
	}
	maxPixels := settings.AnalysisMaxPixels
	if maxPixels == 0 {
		maxPixels = defaultMaxPixels
	}
	if maxPixels < 0 || settings.AnalysisMaxWidth < 0 || settings.AnalysisMaxHeight < 0 || settings.AnalysisMaxPixelsOverride < 0 || settings.AnalysisMaxWidthOverride < 0 || settings.AnalysisMaxHeightOverride < 0 {
		return Resolved{}, fmt.Errorf("analysis resource limits must not be negative")
	}
	maxPixelsOverride := settings.AnalysisMaxPixelsOverride
	if maxPixelsOverride == 0 {
		maxPixelsOverride = maxPixels
	}
	maxWidthOverride := settings.AnalysisMaxWidthOverride
	if maxWidthOverride == 0 {
		maxWidthOverride = settings.AnalysisMaxWidth
	}
	maxHeightOverride := settings.AnalysisMaxHeightOverride
	if maxHeightOverride == 0 {
		maxHeightOverride = settings.AnalysisMaxHeight
	}
	if maxPixelsOverride < maxPixels || (maxWidthOverride != 0 && settings.AnalysisMaxWidth != 0 && maxWidthOverride < settings.AnalysisMaxWidth) || (maxHeightOverride != 0 && settings.AnalysisMaxHeight != 0 && maxHeightOverride < settings.AnalysisMaxHeight) {
		return Resolved{}, fmt.Errorf("analysis override limits must be greater than or equal to configured limits")
	}
	pythonPath := settings.PythonPath
	if pythonPath == "" {
		if runtime.GOOS == "windows" {
			pythonPath = filepath.Join(root, "venv", "Scripts", "python.exe")
		} else {
			pythonPath = filepath.Join(root, "venv", "bin", "python")
		}
	} else {
		pythonPath, err = absolutePath(pythonPath, root)
		if err != nil {
			return Resolved{}, fmt.Errorf("resolve Python path: %w", err)
		}
	}
	serveAddress := settings.ServeAddress
	if serveAddress == "" {
		serveAddress = defaultServeAddress
	}
	provider := settings.Provider
	if provider == "" {
		provider = defaultProvider
	}
	return Resolved{
		ConfigFile:                configPath,
		RuntimeDataRoot:           root,
		DatabasePath:              resolvePath(settings.DatabasePath, root, defaultDatabase),
		ModelRoot:                 resolvePath(settings.ModelRoot, root, "models"),
		PythonPath:                pythonPath,
		Provider:                  provider,
		ServeAddress:              serveAddress,
		LogRoot:                   resolvePath(settings.LogRoot, root, "logs"),
		TempRoot:                  resolvePath(settings.TempRoot, root, "tmp"),
		ExportRoot:                resolvePath(settings.ExportRoot, root, "exports"),
		BackupRoot:                resolvePath(settings.BackupRoot, root, "backups"),
		AnalysisMaxPixels:         maxPixels,
		AnalysisMaxWidth:          settings.AnalysisMaxWidth,
		AnalysisMaxHeight:         settings.AnalysisMaxHeight,
		AnalysisMaxPixelsOverride: maxPixelsOverride,
		AnalysisMaxWidthOverride:  maxWidthOverride,
		AnalysisMaxHeightOverride: maxHeightOverride,
		NeighborSearchMethod:      searchMethod(settings.NeighborSearchMethod),
		RecognitionThresholds:     thresholds,
		ClusteringConfig:          clusteringConfig,
		PossibleCandidates:        possibleConfig,
	}, nil
}

func configFilePath(value string) (string, error) {
	path, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
		path = filepath.Join(path, defaultConfigFile)
	}
	return filepath.Clean(path), nil
}

// parseJSONC accepts JSON plus // and /* */ comments and trailing commas.
// Comment markers inside quoted strings are preserved.
func parseJSONC(data []byte) ([]byte, error) {
	withoutComments := make([]byte, 0, len(data))
	inString, escaped, lineComment, blockComment := false, false, false, false
	for index := 0; index < len(data); index++ {
		current := data[index]
		if lineComment {
			if current == '\n' {
				lineComment = false
				withoutComments = append(withoutComments, current)
			}
			continue
		}
		if blockComment {
			if current == '*' && index+1 < len(data) && data[index+1] == '/' {
				blockComment = false
				index++
				withoutComments = append(withoutComments, ' ')
			} else if current == '\n' {
				withoutComments = append(withoutComments, current)
			}
			continue
		}
		if inString {
			withoutComments = append(withoutComments, current)
			if escaped {
				escaped = false
			} else if current == '\\' {
				escaped = true
			} else if current == '"' {
				inString = false
			}
			continue
		}
		if current == '"' {
			inString = true
			withoutComments = append(withoutComments, current)
			continue
		}
		if current == '/' && index+1 < len(data) && data[index+1] == '/' {
			lineComment = true
			index++
			continue
		}
		if current == '/' && index+1 < len(data) && data[index+1] == '*' {
			blockComment = true
			index++
			continue
		}
		withoutComments = append(withoutComments, current)
	}
	if blockComment {
		return nil, errors.New("unterminated block comment")
	}
	if inString {
		return nil, errors.New("unterminated string")
	}
	result := make([]byte, 0, len(withoutComments))
	inString, escaped = false, false
	for index := 0; index < len(withoutComments); index++ {
		current := withoutComments[index]
		if inString {
			result = append(result, current)
			if escaped {
				escaped = false
			} else if current == '\\' {
				escaped = true
			} else if current == '"' {
				inString = false
			}
			continue
		}
		if current == '"' {
			inString = true
			result = append(result, current)
			continue
		}
		if current == ',' {
			lookahead := index + 1
			for lookahead < len(withoutComments) && (withoutComments[lookahead] == ' ' || withoutComments[lookahead] == '\t' || withoutComments[lookahead] == '\r' || withoutComments[lookahead] == '\n') {
				lookahead++
			}
			if lookahead < len(withoutComments) && (withoutComments[lookahead] == '}' || withoutComments[lookahead] == ']') {
				continue
			}
		}
		result = append(result, current)
	}
	return result, nil
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
		return filepath.Join(home, "Library", "Application Support", "whothatface"), nil
	case "windows":
		if appData := os.Getenv("AppData"); appData != "" {
			return filepath.Join(appData, "whothatface"), nil
		}
		return filepath.Join(home, "AppData", "Roaming", "whothatface"), nil
	default:
		if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
			return filepath.Join(dataHome, "whothatface"), nil
		}
		return filepath.Join(home, ".local", "share", "whothatface"), nil
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

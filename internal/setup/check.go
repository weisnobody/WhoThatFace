// Package setup validates the external Python worker environment.
package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

type Check struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Details string `json:"details"`
}

type Report struct {
	Valid  bool    `json:"valid"`
	Checks []Check `json:"checks"`
}

func Validate(ctx context.Context, pythonPath, virtualEnv, modelRoot string) Report {
	var report Report
	add := func(name string, passed bool, details string) {
		report.Checks = append(report.Checks, Check{Name: name, Passed: passed, Details: details})
	}
	versionOutput, err := exec.CommandContext(ctx, pythonPath, "--version").CombinedOutput()
	if err != nil {
		add("python", false, fmt.Sprintf("%s: %v", strings.TrimSpace(string(versionOutput)), err))
	} else {
		version := parsePythonVersion(string(versionOutput))
		add("python", version >= 311, fmt.Sprintf("%s (requires Python 3.11+)", strings.TrimSpace(string(versionOutput))))
	}
	venvPython := pythonPath
	if virtualEnv != "" {
		if runtime.GOOS == "windows" {
			venvPython = filepath.Join(virtualEnv, "Scripts", "python.exe")
		} else {
			venvPython = filepath.Join(virtualEnv, "bin", "python")
		}
		_, statErr := os.Stat(venvPython)
		add("virtual-environment", statErr == nil, venvPython)
	}
	importOutput, err := exec.CommandContext(ctx, venvPython, "-c", "import insightface, onnxruntime; print(insightface.__version__); print(onnxruntime.__version__)").CombinedOutput()
	add("python-dependencies", err == nil, strings.TrimSpace(string(importOutput)))
	if modelRoot == "" {
		add("model-root", false, "model root is not configured")
	} else {
		info, statErr := os.Stat(modelRoot)
		add("model-root", statErr == nil && info.IsDir(), modelRoot)
		modelName := os.Getenv("FACE_ME_BRO_MODEL")
		if modelName == "" {
			modelName = "buffalo_l"
		}
		modelDir := filepath.Join(modelRoot, "models", modelName)
		manifestPath := filepath.Join(modelDir, "checksums.json")
		manifest, manifestErr := os.ReadFile(manifestPath)
		var expected map[string]string
		if manifestErr == nil {
			manifestErr = json.Unmarshal(manifest, &expected)
		}
		actual, checksumErr := modelChecksums(modelDir)
		valid := manifestErr == nil && checksumErr == nil && len(expected) > 0 && len(expected) == len(actual)
		if valid {
			for name, checksum := range expected {
				if actual[name] != checksum {
					valid = false
					break
				}
			}
		}
		add("model-checksums", valid, manifestPath)
	}
	for _, check := range report.Checks {
		if !check.Passed {
			return report
		}
	}
	report.Valid = true
	return report
}

func modelChecksums(root string) (map[string]string, error) {
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() == "checksums.json" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		result[filepath.ToSlash(relative)] = hex.EncodeToString(digest[:])
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

var pythonVersionPattern = regexp.MustCompile(`Python\s+(\d+)\.(\d+)`)

func parsePythonVersion(value string) int {
	match := pythonVersionPattern.FindStringSubmatch(value)
	if len(match) != 3 {
		return 0
	}
	major, _ := strconv.Atoi(match[1])
	minor, _ := strconv.Atoi(match[2])
	return major*100 + minor
}

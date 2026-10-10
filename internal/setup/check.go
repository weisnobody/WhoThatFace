// Package setup validates the external Python worker environment.
package setup

import (
	"context"
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
	}
	for _, check := range report.Checks {
		if !check.Passed {
			return report
		}
	}
	report.Valid = true
	return report
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

package setup

import "testing"

func TestParsePythonVersion(t *testing.T) {
	if got := parsePythonVersion("Python 3.11.9"); got != 311 {
		t.Errorf("version = %d", got)
	}
	if parsePythonVersion("unknown") != 0 {
		t.Error("unknown version should be zero")
	}
}

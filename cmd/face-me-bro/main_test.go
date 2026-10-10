package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestHelpListsCommandsWithoutOpeningDatabase(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"help"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Usage: face-me-bro", "collection", "cluster", "Global options", "help <command>"} {
		if !strings.Contains(output.String(), text) {
			t.Errorf("help output is missing %q", text)
		}
	}
}

func TestCommandHelpIncludesUsageParametersAndDescription(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"help", "face"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Usage: face-me-bro face", "Inspect or change", "--face-id", "--yes"} {
		if !strings.Contains(output.String(), text) {
			t.Errorf("face help output is missing %q", text)
		}
	}
}

func TestHelpFlagCanSelectACommandWithoutOpeningDatabase(t *testing.T) {
	var output bytes.Buffer
	if err := run(context.Background(), []string{"--help", "search"}, &output, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Usage: face-me-bro search") {
		t.Fatalf("unexpected help output: %s", output.String())
	}
}

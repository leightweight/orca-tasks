package web

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsLocalPath(t *testing.T) {
	t.Parallel()

	for path, want := range map[string]bool{
		"/projects":         true,
		"/browse/OPS-1":     true,
		"":                  false,
		"projects":          false,
		"//evil.example/x":  false,
		`/\evil.example`:    false,
		"https://evil.test": false,
	} {
		if got := isLocalPath(path); got != want {
			t.Errorf("isLocalPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestCodeProblem(t *testing.T) {
	t.Parallel()

	if !codeProblem(fmt.Errorf("checking: %w", errWrongCode)) || !codeProblem(errTooManyAttempts) {
		t.Error("a person's mistake wasn't recognized")
	}

	if codeProblem(errors.New("the database is gone")) {
		t.Error("a server error was treated as the person's mistake")
	}
}

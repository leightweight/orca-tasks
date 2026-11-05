package jira

import (
	"testing"
	"time"
)

func TestSuppressionTargetsOnlyTheDetailRead(t *testing.T) {
	t.Parallel()

	clock := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	s := newCreatedIssueSuppression(5 * time.Second)
	s.now = func() time.Time { return clock }
	s.record("ORCA-1")

	tests := []struct {
		name   string
		key    string
		expand string
		want   bool
	}{
		{"post-create detail read", "ORCA-1", "renderedFields", true},
		// The worktree card's summary lookup sends no expand, so it must survive.
		{"summary lookup", "ORCA-1", "", false},
		{"another issue", "ORCA-2", "renderedFields", false},
		{"key casing", "orca-1", "renderedFields", true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := s.suppresses(test.key, test.expand); got != test.want {
				t.Errorf("suppresses(%q, %q) = %v, want %v", test.key, test.expand, got, test.want)
			}
		})
	}
}

func TestSuppressionExpires(t *testing.T) {
	t.Parallel()

	clock := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	s := newCreatedIssueSuppression(5 * time.Second)
	s.now = func() time.Time { return clock }
	s.record("ORCA-1")

	clock = clock.Add(4 * time.Second)
	if !s.suppresses("ORCA-1", "renderedFields") {
		t.Error("should still suppress inside the window")
	}

	clock = clock.Add(2 * time.Second)
	if s.suppresses("ORCA-1", "renderedFields") {
		t.Error("should stop suppressing past the window")
	}

	if len(s.created) != 0 {
		t.Errorf("created = %v, want it pruned", s.created)
	}
}

func TestSuppressionDisabled(t *testing.T) {
	t.Parallel()

	s := newCreatedIssueSuppression(0)
	s.record("ORCA-1")

	if s.suppresses("ORCA-1", "renderedFields") {
		t.Error("a zero window must never suppress")
	}
}

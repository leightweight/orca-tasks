package jira

import (
	"strings"
	"sync"
	"time"
)

// createdIssueSuppression is a temporary workaround, to delete once Orca fixes
// the bug or ships plugin task sources (stablyai/orca#26282).
//
// After you create an issue, Orca opens its Tasks pane on it, and the pane's
// close button then does nothing. Clicking a row sets
// taskPageData.openJiraIssue, which is what close clears. The create path
// selects the issue through local component state that close never touches,
// so the X clears a field that is already unset and the pane stays open.
// Whether you notice is a race between the list refresh and the post-create
// read, which is why it happens about half the time.
//
// The create path only selects the issue when its detail read returns one:
//
//	jiraGetIssue(...).then((full) => { if (full) { ...; setSelectedJiraIssue(full) } })
//
// so failing that one read stops the pane opening at all. This fails it for
// the issue just created, for a few seconds. Only the detail read carries
// expand=renderedFields; the worktree card's summary lookup does not, so
// summaries keep working.
type createdIssueSuppression struct {
	window time.Duration
	now    func() time.Time

	mu      sync.Mutex
	created map[string]time.Time
}

func newCreatedIssueSuppression(window time.Duration) *createdIssueSuppression {
	return &createdIssueSuppression{window: window, now: time.Now, created: map[string]time.Time{}}
}

func (s *createdIssueSuppression) enabled() bool {
	return s != nil && s.window > 0
}

// record marks an issue as just created. The key includes the principal, since
// issue keys differ between users.
func (s *createdIssueSuppression) record(key string) {
	if !s.enabled() {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.prune()
	s.created[strings.ToUpper(key)] = s.now()
}

// suppresses reports whether to fail this read: a detail read of an issue
// created inside the window.
func (s *createdIssueSuppression) suppresses(key, expand string) bool {
	if !s.enabled() || !strings.Contains(expand, "renderedFields") {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.prune()
	at, ok := s.created[strings.ToUpper(key)]

	return ok && s.now().Sub(at) < s.window
}

// prune drops expired entries so the map cannot grow without bound. The
// caller holds the lock.
func (s *createdIssueSuppression) prune() {
	cutoff := s.now().Add(-s.window)

	for key, at := range s.created {
		if at.Before(cutoff) {
			delete(s.created, key)
		}
	}
}

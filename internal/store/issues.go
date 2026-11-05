package store

import (
	"context"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/leightweight/orca-tasks/internal/store/db"
)

// Person is a membership with the username it belongs to, for assigning an
// issue and recording who it was assigned to.
type Person struct {
	MembershipID int64
	Username     string
}

// NewIssue is what creating an issue takes.
type NewIssue struct {
	ProjectID   int64
	Summary     string
	Description string
	StatusID    int32
	TypeID      int32
	PriorityID  *int32
	Reporter    int64
	Assignee    *Person
	Labels      []string
}

// CreateIssue numbers and inserts an issue, and records its creation.
func (s *Store) CreateIssue(ctx context.Context, n NewIssue) (db.Issue, error) {
	var issue db.Issue

	err := s.InTx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		number, err := q.NextIssueNumber(ctx, n.ProjectID)
		if err != nil {
			return err
		}

		var assignee *int64
		if n.Assignee != nil {
			assignee = &n.Assignee.MembershipID
		}

		issue, err = q.InsertIssue(ctx, db.InsertIssueParams{
			ProjectID:   n.ProjectID,
			Number:      number,
			Summary:     n.Summary,
			Description: n.Description,
			StatusID:    n.StatusID,
			TypeID:      n.TypeID,
			PriorityID:  n.PriorityID,
			ReporterID:  n.Reporter,
			AssigneeID:  assignee,
			Labels:      CleanLabels(n.Labels),
		})
		if err != nil {
			return err
		}

		_, err = q.InsertHistory(ctx, []db.InsertHistoryParams{{
			ProjectID: issue.ProjectID, IssueID: issue.ID, ActorID: n.Reporter, Field: "created",
		}})

		return err
	})

	return issue, err
}

// IssueChange is an edit to an issue. Nil fields stay as they are.
type IssueChange struct {
	Summary     *string
	Description *string
	StatusID    *int32
	TypeID      *int32

	// SetPriority applies PriorityID, which nil clears.
	SetPriority bool
	PriorityID  *int32

	// SetAssignee applies Assignee, which nil clears.
	SetAssignee bool
	Assignee    *Person

	Labels *[]string

	// Comment, if not empty, is added along with the change.
	Comment string
}

// UpdateIssue applies a change and records each field it changed.
func (s *Store) UpdateIssue(ctx context.Context, catalog *Catalog, actor int64, current db.VisibleIssuesRow, change IssueChange) error {
	i := current.Issue
	history := []db.InsertHistoryParams{}

	record := func(field string, oldValue, newValue *string) {
		if deref(oldValue) == deref(newValue) {
			return
		}

		history = append(history, db.InsertHistoryParams{
			ProjectID: i.ProjectID, IssueID: i.ID, ActorID: actor, Field: field, OldValue: oldValue, NewValue: newValue,
		})
	}

	next := db.UpdateIssueParams{
		ID:          i.ID,
		Summary:     i.Summary,
		Description: i.Description,
		StatusID:    i.StatusID,
		TypeID:      i.TypeID,
		PriorityID:  i.PriorityID,
		AssigneeID:  i.AssigneeID,
		Labels:      i.Labels,
	}

	if change.Summary != nil {
		record("summary", &i.Summary, change.Summary)
		next.Summary = *change.Summary
	}

	if change.Description != nil {
		record("description", &i.Description, change.Description)
		next.Description = *change.Description
	}

	if change.StatusID != nil {
		record("status", new(catalog.StatusByID(i.StatusID).Name), new(catalog.StatusByID(*change.StatusID).Name))
		next.StatusID = *change.StatusID
	}

	if change.TypeID != nil {
		record("type", new(catalog.IssueTypeByID(i.TypeID).Name), new(catalog.IssueTypeByID(*change.TypeID).Name))
		next.TypeID = *change.TypeID
	}

	if change.SetPriority {
		record("priority", priorityName(catalog, i.PriorityID), priorityName(catalog, change.PriorityID))
		next.PriorityID = change.PriorityID
	}

	if change.SetAssignee {
		var username *string

		next.AssigneeID = nil

		if change.Assignee != nil {
			username = &change.Assignee.Username
			next.AssigneeID = &change.Assignee.MembershipID
		}

		record("assignee", current.AssigneeUsername, username)
	}

	if change.Labels != nil {
		next.Labels = CleanLabels(*change.Labels)
		oldLabels, newLabels := strings.Join(i.Labels, " "), strings.Join(next.Labels, " ")
		record("labels", &oldLabels, &newLabels)
	}

	return s.InTx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		if len(history) > 0 {
			if err := q.UpdateIssue(ctx, next); err != nil {
				return err
			}

			if _, err := q.InsertHistory(ctx, history); err != nil {
				return err
			}
		}

		if strings.TrimSpace(change.Comment) != "" {
			_, err := addComment(ctx, q, actor, i, change.Comment)
			return err
		}

		return nil
	})
}

// AddComment adds a comment to an issue and records it.
func (s *Store) AddComment(ctx context.Context, actor int64, issue db.Issue, body string) (db.Comment, error) {
	var comment db.Comment

	err := s.InTx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		var err error

		comment, err = addComment(ctx, q, actor, issue, body)

		return err
	})

	return comment, err
}

func addComment(ctx context.Context, q *db.Queries, actor int64, issue db.Issue, body string) (db.Comment, error) {
	comment, err := q.InsertComment(ctx, db.InsertCommentParams{ProjectID: issue.ProjectID, IssueID: issue.ID, AuthorID: actor, Body: body})
	if err != nil {
		return comment, err
	}

	if err := q.TouchIssue(ctx, issue.ID); err != nil {
		return comment, err
	}

	_, err = q.InsertHistory(ctx, []db.InsertHistoryParams{{
		ProjectID: issue.ProjectID, IssueID: issue.ID, ActorID: actor, Field: "comment", NewValue: &body,
	}})

	return comment, err
}

// CleanLabels trims labels, drops empty ones and duplicates, and keeps the
// order they came in.
func CleanLabels(labels []string) []string {
	clean := make([]string, 0, len(labels))

	for _, label := range labels {
		if label = strings.TrimSpace(label); label != "" && !slices.Contains(clean, label) {
			clean = append(clean, label)
		}
	}

	return clean
}

func priorityName(catalog *Catalog, id *int32) *string {
	if p := catalog.PriorityByID(id); p != nil {
		return &p.Name
	}

	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

package store

import (
	"context"
	"errors"
	"slices"
	"strconv"
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

// Parent is an issue another issue belongs to, such as an epic, with its
// number for recording what the parent was.
type Parent struct {
	ID     int64
	Number int32
}

// ErrParentCycle is setting an issue's parent to the issue itself or to one of
// the issues under it.
var ErrParentCycle = errors.New("an issue can't be its own parent or the parent of an issue above it")

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
	Parent      *Parent
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

		var assignee, parent *int64
		if n.Assignee != nil {
			assignee = &n.Assignee.MembershipID
		}

		if n.Parent != nil {
			parent = &n.Parent.ID
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
			ParentID:    parent,
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

	// SetParent applies Parent, which nil clears. The parent must be in the
	// issue's project.
	SetParent bool
	Parent    *Parent

	// Labels replaces the labels. AddLabels and RemoveLabels then edit them.
	Labels       *[]string
	AddLabels    []string
	RemoveLabels []string

	// Comment, if not empty, is added along with the change.
	Comment string
}

// UpdateIssue applies a change and records each field it changed. It locks
// the issue and applies the change to the row as it is then, so two edits at
// once both land instead of the later one undoing the earlier. A change of
// parent that would make a cycle fails with ErrParentCycle.
func (s *Store) UpdateIssue(ctx context.Context, catalog *Catalog, actor, issueID int64, change IssueChange) error {
	return s.InTx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		if change.SetParent {
			if err := q.LockIssueProject(ctx, issueID); err != nil {
				return err
			}
		}

		locked, err := q.LockIssue(ctx, issueID)
		if err != nil {
			return err
		}

		i := locked.Issue
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
			ParentID:    i.ParentID,
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

			record("assignee", locked.AssigneeUsername, username)
		}

		if change.SetParent {
			var number *string

			next.ParentID = nil

			if change.Parent != nil {
				ancestors, err := q.IssueAncestors(ctx, change.Parent.ID)
				if err != nil {
					return err
				}

				if slices.Contains(ancestors, i.ID) {
					return ErrParentCycle
				}

				number = new(strconv.Itoa(int(change.Parent.Number)))
				next.ParentID = &change.Parent.ID
			}

			var oldNumber *string
			if locked.ParentNumber != nil {
				oldNumber = new(strconv.Itoa(int(*locked.ParentNumber)))
			}

			record("parent", oldNumber, number)
		}

		if change.Labels != nil || len(change.AddLabels) > 0 || len(change.RemoveLabels) > 0 {
			labels := i.Labels
			if change.Labels != nil {
				labels = *change.Labels
			}

			next.Labels = slices.DeleteFunc(CleanLabels(append(slices.Clone(labels), change.AddLabels...)), func(label string) bool {
				return slices.Contains(change.RemoveLabels, label)
			})

			oldLabels, newLabels := strings.Join(i.Labels, " "), strings.Join(next.Labels, " ")
			record("labels", &oldLabels, &newLabels)
		}

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

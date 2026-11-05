package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/leightweight/orca-tasks/internal/store/db"
)

// AddAttachment records an attachment on an issue. The caller moves the file
// into place once this returns the attachment's ID.
func (s *Store) AddAttachment(ctx context.Context, actor int64, issue db.Issue, a db.InsertAttachmentParams) (db.Attachment, error) {
	var attachment db.Attachment

	err := s.InTx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		a.ProjectID, a.IssueID, a.UploaderID = issue.ProjectID, issue.ID, actor

		var err error
		if attachment, err = q.InsertAttachment(ctx, a); err != nil {
			return err
		}

		if err := q.TouchIssue(ctx, issue.ID); err != nil {
			return err
		}

		_, err = q.InsertHistory(ctx, []db.InsertHistoryParams{{
			ProjectID: issue.ProjectID, IssueID: issue.ID, ActorID: actor, Field: "attachment", NewValue: &a.Filename,
		}})

		return err
	})

	return attachment, err
}

// RemoveAttachment deletes an attachment's row and records it. The caller
// removes the file.
func (s *Store) RemoveAttachment(ctx context.Context, actor int64, a db.Attachment) error {
	return s.InTx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		if err := q.DeleteAttachment(ctx, a.ID); err != nil {
			return err
		}

		_, err := q.InsertHistory(ctx, []db.InsertHistoryParams{{
			ProjectID: a.ProjectID, IssueID: a.IssueID, ActorID: actor, Field: "attachment", OldValue: &a.Filename,
		}})

		return err
	})
}

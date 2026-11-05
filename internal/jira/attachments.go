package jira

import (
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"path"
	"strconv"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"

	"github.com/leightweight/orca-tasks/internal/attachments"
	"github.com/leightweight/orca-tasks/internal/store"
	"github.com/leightweight/orca-tasks/internal/store/db"
)

// attachmentsOf loads the attachments of several issues in one query.
func (s *Server) attachmentsOf(r *http.Request, issueIDs []int64) (map[int64][]attachmentPayload, error) {
	if len(issueIDs) == 0 {
		return nil, nil
	}

	rows, err := s.Store.ListAttachments(r.Context(), issueIDs)
	if err != nil {
		return nil, err
	}

	byIssue := make(map[int64][]attachmentPayload, len(issueIDs))
	for _, row := range rows {
		uploader := &person{row.UploaderUsername, row.UploaderName, row.UploaderEmail, row.UploaderActive}
		byIssue[row.Attachment.IssueID] = append(byIssue[row.Attachment.IssueID], s.mapAttachment(row.Attachment, uploader))
	}

	return byIssue, nil
}

// uploadAttachments takes multipart file parts named "file", as Jira does. It
// streams each to disk rather than buffering the request.
func (s *Server) uploadAttachments(w http.ResponseWriter, r *http.Request, c *Caller) error {
	// Jira's CSRF guard, which a cross-site form can't send.
	if !strings.EqualFold(r.Header.Get("X-Atlassian-Token"), "no-check") {
		return forbidden("Uploads need the header X-Atlassian-Token: no-check.")
	}

	row, err := s.issue(r.Context(), c, r.PathValue("key"))
	if err != nil {
		return err
	}

	actor, err := s.actor(r.Context(), c, row.Issue.ProjectID)
	if err != nil {
		return err
	}

	// One request holds at most one file's worth, plus room for the form.
	r.Body = http.MaxBytesReader(w, r.Body, s.MaxUpload+1<<20)

	parts, err := r.MultipartReader()
	if err != nil {
		return badRequest("Uploads must be multipart/form-data: %s", err)
	}

	me := &person{c.Username, c.DisplayName, c.Email, true}
	payloads := []attachmentPayload{}

	for {
		part, err := parts.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return badRequest("Malformed upload: %s", err)
		}

		if part.FormName() != "file" || part.FileName() == "" {
			continue
		}

		attachment, err := s.saveAttachment(r, actor, row.Issue, part)
		if err != nil {
			return err
		}

		payloads = append(payloads, s.mapAttachment(attachment, me))
	}

	if len(payloads) == 0 {
		return badRequest("The upload has no parts named \"file\".")
	}

	writeJSON(w, http.StatusOK, payloads)

	return nil
}

func (s *Server) saveAttachment(r *http.Request, actor int64, issue db.Issue, part *multipart.Part) (db.Attachment, error) {
	name := cleanFilename(part.FileName())

	upload, err := s.Files.Receive(part, part.Header.Get("Content-Type"), s.MaxUpload)
	if errors.Is(err, attachments.ErrTooLarge) {
		return db.Attachment{}, &httpError{status: http.StatusRequestEntityTooLarge, message: "The file is over the upload limit."}
	}

	if err != nil {
		return db.Attachment{}, err
	}

	attachment, err := s.Store.AddAttachment(r.Context(), actor, issue, db.InsertAttachmentParams{
		Filename: name,
		MimeType: upload.MIMEType,
		Size:     upload.Size,
		Sha256:   upload.SHA256,
	})
	if err != nil {
		s.Files.Discard(upload)
		return db.Attachment{}, err
	}

	if err := s.Files.Keep(upload, attachment.ID); err != nil {
		s.Files.Discard(upload)
		// The sweep would remove the row's missing file anyway; the row goes
		// now so the issue doesn't list an attachment that isn't there.
		_ = s.Store.DeleteAttachment(r.Context(), attachment.ID)

		return db.Attachment{}, err
	}

	return attachment, nil
}

// cleanFilename keeps the last path element and drops control characters, so
// a filename is safe to show and to put in a header.
func cleanFilename(name string) string {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '"' {
			return -1
		}

		return r
	}, name)

	if name == "" || name == "." || name == "/" {
		name = "attachment"
	}

	if len(name) > 255 {
		name = name[len(name)-255:]
	}

	return name
}

func (s *Server) visibleAttachment(r *http.Request, c *Caller) (db.VisibleAttachmentRow, error) {
	attachmentID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return db.VisibleAttachmentRow{}, notFound("Attachment %s doesn't exist.", r.PathValue("id"))
	}

	row, err := s.Store.VisibleAttachment(r.Context(), db.VisibleAttachmentParams{
		ID:          attachmentID,
		PrincipalID: c.PrincipalID,
		ProjectIds:  c.ProjectIDs,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, notFound("Attachment %d doesn't exist or you can't see it.", attachmentID)
	}

	return row, err
}

func (s *Server) attachment(w http.ResponseWriter, r *http.Request, c *Caller) error {
	row, err := s.visibleAttachment(r, c)
	if err != nil {
		return err
	}

	files, err := s.attachmentsOf(r, []int64{row.Attachment.IssueID})
	if err != nil {
		return err
	}

	for _, payload := range files[row.Attachment.IssueID] {
		if payload.ID == id(row.Attachment.ID) {
			writeJSON(w, http.StatusOK, payload)
			return nil
		}
	}

	return notFound("Attachment %d doesn't exist.", row.Attachment.ID)
}

// deleteAttachment lets the uploader or a project administrator delete an
// attachment. The file goes now; the sweep catches it if that fails.
func (s *Server) deleteAttachment(w http.ResponseWriter, r *http.Request, c *Caller) error {
	row, err := s.visibleAttachment(r, c)
	if err != nil {
		return err
	}

	if row.Membership.ID != row.Attachment.UploaderID && row.Membership.Role != store.RoleAdmin {
		return forbidden("Only the uploader or a project administrator can delete an attachment.")
	}

	if err := s.Store.RemoveAttachment(r.Context(), row.Membership.ID, row.Attachment); err != nil {
		return err
	}

	if err := s.Files.Remove(row.Attachment.ID); err != nil {
		s.Log.Warn("removing an attachment's file failed; the sweep will retry", "id", row.Attachment.ID, "error", err)
	}

	writeJSON(w, http.StatusNoContent, nil)

	return nil
}

// download serves an attachment's contents to an API key or a web session.
func (s *Server) download(w http.ResponseWriter, r *http.Request) error {
	c, err := s.authenticate(r)
	if err != nil && s.SessionCaller != nil {
		if session := s.SessionCaller(r); session != nil {
			c, err = session, nil
		}
	}

	if err != nil {
		return err
	}

	row, err := s.visibleAttachment(r, c)
	if err != nil {
		return err
	}

	f, err := s.Files.Open(row.Attachment.ID)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	disposition := "attachment"
	if attachments.InlineTypes[row.Attachment.MimeType] {
		disposition = "inline"
	}

	h := w.Header()
	h.Set("Content-Type", row.Attachment.MimeType)
	h.Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": row.Attachment.Filename}))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("Cache-Control", "private, max-age=3600")

	http.ServeContent(w, r, "", row.Attachment.CreatedAt, f)

	return nil
}

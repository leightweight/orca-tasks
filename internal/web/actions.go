package web

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/leightweight/orca-tasks/internal/attachments"
	"github.com/leightweight/orca-tasks/internal/store"
	"github.com/leightweight/orca-tasks/internal/store/db"
)

// taskAction finds the task a form on its page acts on and the signed-in
// user's membership in its project, which the change is recorded under.
func (w *Web) taskAction(rw http.ResponseWriter, r *http.Request) (db.VisibleIssuesRow, int64, bool) {
	row, ok := w.visibleIssue(rw, r)
	if !ok {
		return row, 0, false
	}

	actor, err := w.Store.GetMembershipByKey(r.Context(), db.GetMembershipByKeyParams{PrincipalID: currentUser(r.Context()).ID, Key: row.ProjectKey})
	if err != nil {
		w.serverError(rw, r, err)
		return row, 0, false
	}

	return row, actor.Membership.ID, true
}

// moveTask changes a task's status, with an optional comment about the move.
func (w *Web) moveTask(rw http.ResponseWriter, r *http.Request) {
	row, actor, ok := w.taskAction(rw, r)
	if !ok {
		return
	}

	page := "/browse/" + issueKey(row)

	status, err := w.Catalog.Status(r.PostFormValue("status"))
	if err != nil {
		w.redirectError(rw, r, page, "Choose a status.")
		return
	}

	change := store.IssueChange{StatusID: &status.ID, Comment: strings.ReplaceAll(r.PostFormValue("comment"), "\r\n", "\n")}
	if err := w.Store.UpdateIssue(r.Context(), w.Catalog, actor, row.Issue.ID, change); err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.redirect(rw, r, page, "Moved "+issueKey(row)+" to "+status.Name+".")
}

// assignTask assigns a task to the user the form chose, to the signed-in user
// for the Assign to me button, or to nobody.
func (w *Web) assignTask(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	row, actor, ok := w.taskAction(rw, r)
	if !ok {
		return
	}

	page := "/browse/" + issueKey(row)
	username := r.PostFormValue("assignee")

	if r.PostFormValue("me") == "1" {
		username = currentUser(ctx).Username
	}

	change := store.IssueChange{SetAssignee: true}

	if username != "" {
		membership, err := w.Store.ActiveMembership(ctx, db.ActiveMembershipParams{ProjectID: row.Issue.ProjectID, Username: username})
		if errors.Is(err, pgx.ErrNoRows) {
			w.redirectError(rw, r, page, username+" isn't a member of the project, so the task can't be assigned to them.")
			return
		}

		if err != nil {
			w.serverError(rw, r, err)
			return
		}

		change.Assignee = &store.Person{MembershipID: membership, Username: strings.ToLower(username)}
	}

	if err := w.Store.UpdateIssue(ctx, w.Catalog, actor, row.Issue.ID, change); err != nil {
		w.serverError(rw, r, err)
		return
	}

	if username == "" {
		w.redirect(rw, r, page, "Unassigned "+issueKey(row)+".")
		return
	}

	w.redirect(rw, r, page, "Assigned "+issueKey(row)+" to "+username+".")
}

func (w *Web) commentOnTask(rw http.ResponseWriter, r *http.Request) {
	row, actor, ok := w.taskAction(rw, r)
	if !ok {
		return
	}

	page := "/browse/" + issueKey(row)

	body := strings.ReplaceAll(r.PostFormValue("body"), "\r\n", "\n")
	if strings.TrimSpace(body) == "" {
		w.redirectError(rw, r, page+"#comment", "A comment can't be empty.")
		return
	}

	comment, err := w.Store.AddComment(r.Context(), actor, row.Issue, body)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.redirect(rw, r, page+"#comment-"+strconv.FormatInt(comment.ID, 10), "")
}

// attachToTask saves the files a form uploads and shows the Markdown that
// embeds each, to paste into the description or a comment.
func (w *Web) attachToTask(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	row, actor, ok := w.taskAction(rw, r)
	if !ok {
		return
	}

	page := "/browse/" + issueKey(row)

	// One request holds at most one file's worth, plus room for the form.
	r.Body = http.MaxBytesReader(rw, r.Body, w.MaxUpload+1<<20)

	parts, err := r.MultipartReader()
	if err != nil {
		w.redirectError(rw, r, page, "Choose a file to attach.")
		return
	}

	var lines []string

	for {
		part, err := parts.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			w.redirectError(rw, r, page, uploadProblem(err, len(lines)))
			return
		}

		if part.FormName() != "file" || part.FileName() == "" {
			continue
		}

		attachment, err := w.Files.Save(ctx, w.Store, actor, row.Issue, part.FileName(), part, part.Header.Get("Content-Type"), w.MaxUpload)
		if err != nil {
			if problem := uploadProblem(err, len(lines)); problem != "" {
				w.redirectError(rw, r, page, problem)
				return
			}

			w.serverError(rw, r, err)

			return
		}

		lines = append(lines, attachmentMarkdown(attachment, attachmentLink{
			Name: attachment.Filename,
			URL:  fmt.Sprintf("%s/secure/attachment/%d/%s", strings.TrimRight(w.BaseURL, "/"), attachment.ID, url.PathEscape(attachment.Filename)),
		}))
	}

	if len(lines) == 0 {
		w.redirectError(rw, r, page, "Choose a file to attach.")
		return
	}

	w.putSealed(ctx, sessionReveal, reveal{
		Title: "Attached",
		Note:  "Paste this into the description or a comment to show it there.",
		Lines: lines,
	})
	http.Redirect(rw, r, page, http.StatusSeeOther)
}

// uploadProblem says what went wrong with an upload in a way the person can
// act on, or returns empty for an error that isn't theirs.
func uploadProblem(err error, saved int) string {
	var maxBytes *http.MaxBytesError

	tooLarge := errors.Is(err, attachments.ErrTooLarge) || errors.As(err, &maxBytes)
	if !tooLarge {
		return ""
	}

	problem := "That file is over the upload limit."
	if saved > 0 {
		problem += fmt.Sprintf(" The %d before it were attached.", saved)
	}

	return problem
}

// attachmentMarkdown is the Markdown that embeds an attachment: an image for
// images, a link for anything else.
func attachmentMarkdown(a db.Attachment, link attachmentLink) string {
	markdown := fmt.Sprintf("[%s](%s)", link.Name, link.URL)
	if strings.HasPrefix(a.MimeType, "image/") {
		markdown = "!" + markdown
	}

	return markdown
}

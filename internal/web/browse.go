package web

import (
	"bytes"
	"cmp"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark/v2/parser"
	"github.com/yuin/goldmark/v2/renderer/html"

	"github.com/leightweight/orca-tasks/internal/store"
	"github.com/leightweight/orca-tasks/internal/store/db"
)

type issuePage struct {
	// The choices the forms on the page offer, and the task's values for them.
	Statuses         []db.Status
	StatusID         int32
	Members          []db.ListMembersRow
	AssigneeUsername string

	Key         string
	ProjectKey  string
	ProjectName string
	Status      string
	Category    string
	Type        string
	Priority    string
	Assignee    string
	Reporter    string
	Labels      []string
	Created     time.Time
	Updated     time.Time
	Parent      *issueLink
	Children    []issueLink

	// ChildFilter is which child tasks show: open, done, or all. The counts
	// are of all the children, for the filter's tabs.
	ChildFilter string
	ChildOpen   int
	ChildDone   int
	Description template.HTML
	Attachments []attachmentLink
	Comments    []commentView
}

type issueLink struct {
	Key      string
	Summary  string
	Status   string
	Category string
}

type attachmentLink struct {
	Name string
	URL  string
	Size int64
}

type commentView struct {
	ID      int64
	Author  string
	Created time.Time
	Body    template.HTML
}

// markdown renders Markdown to HTML. goldmark drops raw HTML unless told
// otherwise, so the result is safe to put in a page.
func markdown(text string) template.HTML {
	source := []byte(text)

	var out bytes.Buffer
	if err := html.New().Render(&out, source, parser.New().Parse(source)); err != nil {
		return template.HTML(template.HTMLEscapeString(text))
	}

	return template.HTML(out.String())
}

// preview renders Markdown for a form's Preview button.
func (w *Web) preview(rw http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(rw, r.Body, 1<<20)

	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = rw.Write([]byte(markdown(r.PostFormValue("text"))))
}

// visibleIssue finds the task the path's key names among those the signed-in
// user can see, showing a 404 if there isn't one.
func (w *Web) visibleIssue(rw http.ResponseWriter, r *http.Request) (db.VisibleIssuesRow, bool) {
	ctx := r.Context()

	project, numberText, _ := strings.Cut(r.PathValue("key"), "-")

	number, err := strconv.ParseInt(numberText, 10, 32)
	if err != nil {
		w.notFound(rw, r)
		return db.VisibleIssuesRow{}, false
	}

	rows, err := w.Store.VisibleIssues(ctx, db.VisibleIssuesParams{
		PrincipalID: currentUser(ctx).ID,
		Key:         &project,
		Number:      new(int32(number)),
	})
	if err != nil {
		w.serverError(rw, r, err)
		return db.VisibleIssuesRow{}, false
	}

	if len(rows) != 1 {
		w.notFound(rw, r)
		return db.VisibleIssuesRow{}, false
	}

	return rows[0], true
}

// browse shows a task. Orca links each task here.
func (w *Web) browse(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	row, ok := w.visibleIssue(rw, r)
	if !ok {
		return
	}

	i := row.Issue

	comments, err := w.Store.ListComments(ctx, i.ID)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	children, err := w.Store.VisibleIssues(ctx, db.VisibleIssuesParams{
		PrincipalID: currentUser(ctx).ID,
		ParentID:    &i.ID,
	})
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	files, err := w.Store.ListAttachments(ctx, []int64{i.ID})
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	status := w.Catalog.StatusByID(i.StatusID)

	page := issuePage{
		Key:         issueKey(row),
		ProjectKey:  row.ProjectKey,
		ProjectName: row.ProjectName,
		Status:      status.Name,
		Category:    status.Category,
		Type:        w.Catalog.IssueTypeByID(i.TypeID).Name,
		Reporter:    row.ReporterName,
		Labels:      i.Labels,
		Created:     i.CreatedAt,
		Updated:     i.UpdatedAt,
		Description: markdown(i.Description),
	}

	if p := w.Catalog.PriorityByID(i.PriorityID); p != nil {
		page.Priority = p.Name
	}

	if row.AssigneeName != nil {
		page.Assignee = *row.AssigneeName
		page.AssigneeUsername = *row.AssigneeUsername
	}

	page.Statuses, page.StatusID = w.Catalog.Statuses, i.StatusID

	members, err := w.Store.ListMembers(ctx, i.ProjectID)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	page.Members = slices.DeleteFunc(members, func(m db.ListMembersRow) bool { return m.Principal.DisabledAt != nil })

	if row.ParentNumber != nil {
		parentStatus := w.Catalog.StatusByID(*row.ParentStatusID)
		page.Parent = &issueLink{
			Key:      fmt.Sprintf("%s-%d", row.ProjectKey, *row.ParentNumber),
			Summary:  *row.ParentSummary,
			Status:   parentStatus.Name,
			Category: parentStatus.Category,
		}
	}

	slices.SortFunc(children, func(a, b db.VisibleIssuesRow) int { return cmp.Compare(a.Issue.Number, b.Issue.Number) })

	page.ChildFilter = r.URL.Query().Get("children")
	if page.ChildFilter != "open" && page.ChildFilter != "done" {
		page.ChildFilter = "all"
	}

	for _, c := range children {
		childStatus := w.Catalog.StatusByID(c.Issue.StatusID)

		done := childStatus.Category == store.CategoryDone
		if done {
			page.ChildDone++
		} else {
			page.ChildOpen++
		}

		if (page.ChildFilter == "open" && done) || (page.ChildFilter == "done" && !done) {
			continue
		}

		page.Children = append(page.Children, issueLink{
			Key:      fmt.Sprintf("%s-%d", c.ProjectKey, c.Issue.Number),
			Summary:  c.Issue.Summary,
			Status:   childStatus.Name,
			Category: childStatus.Category,
		})
	}

	for _, f := range files {
		page.Attachments = append(page.Attachments, attachmentLink{
			Name: f.Attachment.Filename,
			URL:  fmt.Sprintf("/secure/attachment/%d/%s", f.Attachment.ID, url.PathEscape(f.Attachment.Filename)),
			Size: f.Attachment.Size,
		})
	}

	for _, c := range comments {
		page.Comments = append(page.Comments, commentView{
			ID: c.Comment.ID, Author: c.AuthorName, Created: c.Comment.CreatedAt, Body: markdown(c.Comment.Body),
		})
	}

	w.render(rw, r, http.StatusOK, "browse", fmt.Sprintf("%s-%d: %s", row.ProjectKey, i.Number, i.Summary), page)
}

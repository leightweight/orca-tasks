package web

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark/v2/parser"
	"github.com/yuin/goldmark/v2/renderer/html"

	"github.com/leightweight/orca-tasks/internal/store/db"
)

type issuePage struct {
	Status      string
	Type        string
	Priority    string
	Assignee    string
	Reporter    string
	Labels      []string
	Updated     time.Time
	Description template.HTML
	Attachments []attachmentLink
	Comments    []commentView
}

type attachmentLink struct {
	Name string
	URL  string
	Size int64
}

type commentView struct {
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

// browse shows an issue read-only. Orca links each issue here.
func (w *Web) browse(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	key := r.PathValue("key")

	project, numberText, _ := strings.Cut(key, "-")

	number, err := strconv.ParseInt(numberText, 10, 32)
	if err != nil {
		w.notFound(rw, r)
		return
	}

	rows, err := w.Store.VisibleIssues(ctx, db.VisibleIssuesParams{
		PrincipalID: currentUser(ctx).ID,
		Key:         &project,
		Number:      new(int32(number)),
	})
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	if len(rows) != 1 {
		w.notFound(rw, r)
		return
	}

	row := rows[0]
	i := row.Issue

	comments, err := w.Store.ListComments(ctx, i.ID)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	files, err := w.Store.ListAttachments(ctx, []int64{i.ID})
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	page := issuePage{
		Status:      w.Catalog.StatusByID(i.StatusID).Name,
		Type:        w.Catalog.IssueTypeByID(i.TypeID).Name,
		Reporter:    row.ReporterName,
		Labels:      i.Labels,
		Updated:     i.UpdatedAt,
		Description: markdown(i.Description),
	}

	if p := w.Catalog.PriorityByID(i.PriorityID); p != nil {
		page.Priority = p.Name
	}

	if row.AssigneeName != nil {
		page.Assignee = *row.AssigneeName
	}

	for _, f := range files {
		page.Attachments = append(page.Attachments, attachmentLink{
			Name: f.Attachment.Filename,
			URL:  fmt.Sprintf("/secure/attachment/%d/%s", f.Attachment.ID, url.PathEscape(f.Attachment.Filename)),
			Size: f.Attachment.Size,
		})
	}

	for _, c := range comments {
		page.Comments = append(page.Comments, commentView{Author: c.AuthorName, Created: c.Comment.CreatedAt, Body: markdown(c.Comment.Body)})
	}

	w.render(rw, r, http.StatusOK, "browse", fmt.Sprintf("%s-%d: %s", row.ProjectKey, i.Number, i.Summary), page)
}

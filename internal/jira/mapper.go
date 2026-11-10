package jira

import (
	"bytes"
	"cmp"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yuin/goldmark/v2/extension"
	"github.com/yuin/goldmark/v2/parser"
	"github.com/yuin/goldmark/v2/renderer/html"

	"github.com/leightweight/orca-tasks/internal/jql"
	"github.com/leightweight/orca-tasks/internal/store"
	"github.com/leightweight/orca-tasks/internal/store/db"
)

// jiraTime is the timestamp layout Jira sends.
const jiraTime = "2006-01-02T15:04:05.000-0700"

// Jira's built-in status categories, so Orca draws the usual grey, yellow,
// and green.
var (
	categoryIDs    = map[string]int{store.CategoryNew: 2, store.CategoryIndeterminate: 4, store.CategoryDone: 3}
	categoryColors = map[string]string{store.CategoryNew: "blue-gray", store.CategoryIndeterminate: "yellow", store.CategoryDone: "green"}
	categoryNames  = map[string]string{store.CategoryNew: "To Do", store.CategoryIndeterminate: "In Progress", store.CategoryDone: "Done"}
)

func id(n int64) string { return strconv.FormatInt(n, 10) }

// person is a principal as an issue, comment, or attachment shows it.
type person struct {
	username, name, email string
	active                bool
}

// mapUser renders a person. Server and Data Center have no accountId, so Orca
// identifies people by name.
func mapUser(p *person) *userPayload {
	if p == nil {
		return nil
	}

	return &userPayload{
		Name:         p.username,
		Key:          p.username,
		DisplayName:  firstNonEmpty(p.name, p.username),
		EmailAddress: p.email,
		Active:       p.active,
	}
}

func mapStatus(s db.Status) statusPayload {
	return statusPayload{
		ID:   strconv.Itoa(int(s.ID)),
		Name: s.Name,
		StatusCategory: statusCategoryPayload{
			ID:        categoryIDs[s.Category],
			Key:       s.Category,
			Name:      categoryNames[s.Category],
			ColorName: categoryColors[s.Category],
		},
	}
}

func mapIssueType(t db.IssueType) issueTypePayload {
	return issueTypePayload{ID: strconv.Itoa(int(t.ID)), Name: t.Name, Description: t.Description}
}

func mapPriority(p *db.Priority) *priorityPayload {
	if p == nil {
		return nil
	}

	return &priorityPayload{ID: strconv.Itoa(int(p.ID)), Name: p.Name}
}

func issueKey(row db.VisibleIssuesRow) string {
	return fmt.Sprintf("%s-%d", row.ProjectKey, row.Issue.Number)
}

// parentKey is the key of an issue's parent, which is in the same project, or
// empty without one.
func parentKey(row db.VisibleIssuesRow) string {
	if row.ParentNumber == nil {
		return ""
	}

	return fmt.Sprintf("%s-%d", row.ProjectKey, *row.ParentNumber)
}

func reporter(row db.VisibleIssuesRow) *person {
	return &person{row.ReporterUsername, row.ReporterName, row.ReporterEmail, row.ReporterActive}
}

func assignee(row db.VisibleIssuesRow) *person {
	if row.AssigneeUsername == nil {
		return nil
	}

	return &person{*row.AssigneeUsername, deref(row.AssigneeName), deref(row.AssigneeEmail), row.AssigneeActive}
}

// mapIssue renders an issue. Descriptions stay Markdown, since Orca passes a
// plain string body through as it is.
func (s *Server) mapIssue(row db.VisibleIssuesRow, withDescription bool, attachments []attachmentPayload) issuePayload {
	i := row.Issue
	status := s.Catalog.StatusByID(i.StatusID)

	var resolution *resolutionPayload
	if status.Category == store.CategoryDone {
		resolution = &resolutionPayload{ID: "10000", Name: "Done"}
	}

	if attachments == nil {
		attachments = []attachmentPayload{}
	}

	fields := issueFields{
		Summary:    i.Summary,
		Project:    projectPayload{ID: id(i.ProjectID), Key: row.ProjectKey, Name: row.ProjectName},
		IssueType:  mapIssueType(s.Catalog.IssueTypeByID(i.TypeID)),
		Status:     mapStatus(status),
		Assignee:   mapUser(assignee(row)),
		Reporter:   mapUser(reporter(row)),
		Priority:   mapPriority(s.Catalog.PriorityByID(i.PriorityID)),
		Labels:     i.Labels,
		Created:    i.CreatedAt.Format(jiraTime),
		Updated:    i.UpdatedAt.Format(jiraTime),
		Resolution: resolution,
		Attachment: attachments,
	}

	if withDescription {
		fields.Description = i.Description
	}

	if i.ParentID != nil && row.ParentNumber != nil {
		fields.Parent = &parentPayload{
			ID:   id(*i.ParentID),
			Key:  parentKey(row),
			Self: fmt.Sprintf("%s/rest/api/2/issue/%s", s.BaseURL, id(*i.ParentID)),
			Fields: parentFields{
				Summary:   deref(row.ParentSummary),
				Status:    mapStatus(s.Catalog.StatusByID(*row.ParentStatusID)),
				IssueType: mapIssueType(s.Catalog.IssueTypeByID(*row.ParentTypeID)),
			},
		}
	}

	return issuePayload{
		ID:     id(i.ID),
		Key:    issueKey(row),
		Self:   fmt.Sprintf("%s/rest/api/2/issue/%s", s.BaseURL, id(i.ID)),
		Fields: fields,
	}
}

// renderMarkdown converts a description to HTML, with the GitHub-flavored
// Markdown that the web interface renders. goldmark drops raw HTML
// unless told otherwise, so the result is safe to show.
func renderMarkdown(text string) string {
	source := []byte(text)

	var out bytes.Buffer
	if err := html.New(html.WithExtensions(extension.NewGFMHTMLRenderer())).
		Render(&out, source, parser.New(parser.WithExtensions(extension.NewGFMParser())).Parse(source)); err != nil {
		return ""
	}

	return out.String()
}

func (s *Server) mapComment(c db.ListCommentsRow) commentPayload {
	return commentPayload{
		ID:      id(c.Comment.ID),
		Body:    c.Comment.Body,
		Author:  mapUser(&person{c.AuthorUsername, c.AuthorName, c.AuthorEmail, c.AuthorActive}),
		Created: c.Comment.CreatedAt.Format(jiraTime),
		Updated: c.Comment.UpdatedAt.Format(jiraTime),
	}
}

func (s *Server) attachmentURL(a db.Attachment) string {
	return fmt.Sprintf("%s/secure/attachment/%d/%s", s.BaseURL, a.ID, url.PathEscape(a.Filename))
}

func (s *Server) mapAttachment(a db.Attachment, uploader *person) attachmentPayload {
	return attachmentPayload{
		Self:     fmt.Sprintf("%s/rest/api/2/attachment/%d", s.BaseURL, a.ID),
		ID:       id(a.ID),
		Filename: a.Filename,
		Author:   mapUser(uploader),
		Created:  a.CreatedAt.Format(jiraTime),
		Size:     a.Size,
		MimeType: a.MimeType,
		Content:  s.attachmentURL(a),
	}
}

// jqlFields are the fields an issue answers to in JQL. sprint has no values,
// so Orca's sprint in openSprints() matches nothing rather than failing.
var jqlFields = map[string]bool{
	"key": true, "id": true, "summary": true, "description": true, "text": true, "project": true,
	"status": true, "statuscategory": true, "type": true, "priority": true, "assignee": true,
	"reporter": true, "labels": true, "resolution": true, "created": true, "updated": true, "sprint": true,
	"parent": true,
}

// JQLSyntaxError is a query JQL can't parse, as opposed to one that names an
// unknown field.
type JQLSyntaxError struct{ Err error }

func (e *JQLSyntaxError) Error() string { return "invalid JQL syntax: " + e.Err.Error() }
func (e *JQLSyntaxError) Unwrap() error { return e.Err }

// ParseJQL parses a query and checks it names only fields issues answer to.
func ParseJQL(text string) (*jql.Query, error) {
	query, err := jql.Parse(text)
	if err != nil {
		return nil, &JQLSyntaxError{Err: err}
	}

	if err := query.Validate(func(field string) bool { return jqlFields[field] }); err != nil {
		return nil, err
	}

	return query, nil
}

// Filter keeps the issues a query matches, in the query's order. Search and
// the web interface's task list both use it, so a filter matches the same
// issues in Orca and in the browser. currentUsers are the names
// currentUser() matches.
func Filter(query *jql.Query, rows []db.VisibleIssuesRow, catalog *store.Catalog, currentUsers []string, now time.Time) []db.VisibleIssuesRow {
	ctx := jql.Context{CurrentUsers: currentUsers, Now: now}

	records := make([]issueRecord, 0, len(rows))
	for _, row := range rows {
		if record := (issueRecord{row: row, catalog: catalog}); query.Match(record, ctx) {
			records = append(records, record)
		}
	}

	jql.Sort(records, query.Terms())

	matched := make([]db.VisibleIssuesRow, len(records))
	for i, record := range records {
		matched[i] = record.row
	}

	return matched
}

// issueRecord adapts an issue to the JQL evaluator.
type issueRecord struct {
	row     db.VisibleIssuesRow
	catalog *store.Catalog
}

func (r issueRecord) Field(name string) []string {
	i := r.row.Issue

	switch name {
	case "key":
		return []string{issueKey(r.row)}
	case "id":
		return []string{id(i.ID)}
	case "summary":
		return []string{i.Summary}
	case "description":
		return []string{i.Description}
	case "text":
		// Jira's text searches every free-text field at once.
		return append([]string{i.Summary, i.Description}, i.Labels...)
	case "project":
		return []string{r.row.ProjectKey, r.row.ProjectName, id(i.ProjectID)}
	case "status":
		return []string{r.catalog.StatusByID(i.StatusID).Name}
	case "statuscategory":
		category := r.catalog.StatusByID(i.StatusID).Category
		return []string{category, categoryNames[category]}
	case "type":
		return []string{r.catalog.IssueTypeByID(i.TypeID).Name}
	case "priority":
		if p := r.catalog.PriorityByID(i.PriorityID); p != nil {
			return []string{p.Name}
		}

		return nil
	case "assignee":
		return aliases(assignee(r.row))
	case "reporter":
		return aliases(reporter(r.row))
	case "parent":
		if i.ParentID == nil {
			return nil
		}

		return []string{parentKey(r.row), id(*i.ParentID)}
	case "labels":
		return i.Labels
	case "resolution":
		if r.catalog.StatusByID(i.StatusID).Category == store.CategoryDone {
			return []string{"Done"}
		}

		return nil
	case "created":
		return []string{i.CreatedAt.Format(jiraTime)}
	case "updated":
		return []string{i.UpdatedAt.Format(jiraTime)}
	default:
		return nil
	}
}

func (r issueRecord) Timestamp(name string) (time.Time, bool) {
	switch name {
	case "created":
		return r.row.Issue.CreatedAt, true
	case "updated":
		return r.row.Issue.UpdatedAt, true
	default:
		return time.Time{}, false
	}
}

// aliases lets a query match a username, display name, or email.
func aliases(p *person) []string {
	if p == nil {
		return nil
	}

	names := []string{p.username}

	if p.name != "" && !strings.EqualFold(p.name, p.username) {
		names = append(names, p.name)
	}

	if p.email != "" {
		names = append(names, p.email)
	}

	return names
}

func deref(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

// Compare orders the fields whose text doesn't sort: keys by project and then
// number, so OPS-2 comes before OPS-10, priorities by rank, where ascending is
// Lowest first as in Jira, and statuses in workflow order.
func (r issueRecord) Compare(field string, other jql.Record) (int, bool) {
	o, ok := other.(issueRecord)
	if !ok {
		return 0, false
	}

	switch field {
	case "key":
		if byProject := strings.Compare(r.row.ProjectKey, o.row.ProjectKey); byProject != 0 {
			return byProject, true
		}

		return cmp.Compare(int64(r.row.Issue.Number), int64(o.row.Issue.Number)), true
	case "priority":
		return cmp.Compare(r.priorityRank(), o.priorityRank()), true
	case "status":
		return cmp.Compare(int64(r.catalog.StatusByID(r.row.Issue.StatusID).Position), int64(r.catalog.StatusByID(o.row.Issue.StatusID).Position)), true
	case "statuscategory":
		return cmp.Compare(categoryRank[r.catalog.StatusByID(r.row.Issue.StatusID).Category],
			categoryRank[r.catalog.StatusByID(o.row.Issue.StatusID).Category]), true
	default:
		return 0, false
	}
}

// categoryRank orders status categories through the workflow.
var categoryRank = map[string]int64{store.CategoryNew: 0, store.CategoryIndeterminate: 1, store.CategoryDone: 2}

// priorityRank is higher for a more urgent priority, and lowest with none.
// Positions count down from Highest, which is 1.
func (r issueRecord) priorityRank() int64 {
	p := r.catalog.PriorityByID(r.row.Issue.PriorityID)
	if p == nil {
		return math.MinInt64
	}

	return -int64(p.Position)
}

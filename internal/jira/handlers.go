package jira

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/leightweight/orca-tasks/internal/jql"
	"github.com/leightweight/orca-tasks/internal/store"
	"github.com/leightweight/orca-tasks/internal/store/db"
)

func (s *Server) myself(w http.ResponseWriter, _ *http.Request, c *Caller) error {
	writeJSON(w, http.StatusOK, mapUser(&person{c.Username, c.DisplayName, c.Email, true}))
	return nil
}

type searchRequest struct {
	JQL        string   `json:"jql"`
	MaxResults int      `json:"maxResults"`
	StartAt    int      `json:"startAt"`
	Fields     []string `json:"fields"`
	Expand     []string `json:"expand"`
}

func (s *Server) searchPost(w http.ResponseWriter, r *http.Request, c *Caller) error {
	var request searchRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		return badRequest("Malformed search request: %s", err)
	}

	return s.search(w, r, c, request)
}

func (s *Server) searchGet(w http.ResponseWriter, r *http.Request, c *Caller) error {
	query := r.URL.Query()
	maxResults, _ := strconv.Atoi(query.Get("maxResults"))
	startAt, _ := strconv.Atoi(query.Get("startAt"))

	request := searchRequest{JQL: query.Get("jql"), MaxResults: maxResults, StartAt: startAt}

	if query.Has("fields") {
		request.Fields = strings.Split(query.Get("fields"), ",")
	}

	if query.Has("expand") {
		request.Expand = strings.Split(query.Get("expand"), ",")
	}

	return s.search(w, r, c, request)
}

// maxPageSize caps a page of search results, as Jira does.
const maxPageSize = 100

// search filters the caller's issues with JQL in Go. It loads every issue the
// caller can see in one query, which a task list of thousands of issues
// handles in milliseconds, and then pages the result.
func (s *Server) search(w http.ResponseWriter, r *http.Request, c *Caller, request searchRequest) error {
	query, err := jql.Parse(request.JQL)
	if err != nil {
		// Orca shows a syntax hint when the message mentions JQL.
		return badRequest("Invalid JQL syntax: %s", err)
	}

	if err := query.Validate(func(field string) bool { return jqlFields[field] }); err != nil {
		return badRequest("Invalid JQL: %s", err)
	}

	rows, err := s.Store.VisibleIssues(r.Context(), db.VisibleIssuesParams{PrincipalID: c.PrincipalID, ProjectIds: c.ProjectIDs})
	if err != nil {
		return err
	}

	ctx := jql.Context{CurrentUsers: c.names(), Now: s.Now()}

	records := make([]issueRecord, 0, len(rows))
	for _, row := range rows {
		if record := (issueRecord{row: row, catalog: s.Catalog}); query.Match(record, ctx) {
			records = append(records, record)
		}
	}

	jql.Sort(records, query.Terms())

	// A request over the cap gets the cap, not the default, so a client that
	// pages by the size it asked for doesn't skip issues.
	maxResults := min(request.MaxResults, maxPageSize)
	if maxResults <= 0 {
		maxResults = 50
	}

	startAt := min(max(request.StartAt, 0), len(records))
	page := records[startAt:]
	page = page[:min(len(page), maxResults)]

	wants := func(field string) bool {
		return len(request.Fields) == 0 || slices.ContainsFunc(request.Fields, func(f string) bool {
			f = strings.TrimSpace(f)
			return strings.EqualFold(f, field) || f == "*all"
		})
	}

	var files map[int64][]attachmentPayload

	if wants("attachment") {
		ids := make([]int64, len(page))
		for i, record := range page {
			ids[i] = record.row.Issue.ID
		}

		if files, err = s.attachmentsOf(r, ids); err != nil {
			return err
		}
	}

	rendered := slices.Contains(request.Expand, "renderedFields")

	issues := make([]issuePayload, 0, len(page))
	for _, record := range page {
		issue := s.mapIssue(record.row, wants("description"), files[record.row.Issue.ID])
		if rendered {
			issue.RenderedFields = &renderedFields{Description: renderMarkdown(record.row.Issue.Description)}
		}

		issues = append(issues, issue)
	}

	writeJSON(w, http.StatusOK, searchResponse{
		StartAt:    startAt,
		MaxResults: maxResults,
		Total:      len(records),
		IsLast:     startAt+len(page) >= len(records),
		Issues:     issues,
	})

	return nil
}

func (s *Server) projects(w http.ResponseWriter, r *http.Request, c *Caller) error {
	rows, err := s.Store.ListMyProjects(r.Context(), c.PrincipalID)
	if err != nil {
		return err
	}

	payloads := make([]projectPayload, 0, len(rows))

	for _, row := range rows {
		if c.canSee(row.Project.ID) {
			payloads = append(payloads, projectPayload{ID: id(row.Project.ID), Key: row.Membership.Key, Name: row.Project.Name})
		}
	}

	writeJSON(w, http.StatusOK, payloads)

	return nil
}

func (s *Server) statuses(w http.ResponseWriter, _ *http.Request, _ *Caller) error {
	payloads := make([]statusPayload, 0, len(s.Catalog.Statuses))
	for _, status := range s.Catalog.Statuses {
		payloads = append(payloads, mapStatus(status))
	}

	writeJSON(w, http.StatusOK, payloads)

	return nil
}

func (s *Server) issueTypes(w http.ResponseWriter, _ *http.Request, _ *Caller) error {
	writeJSON(w, http.StatusOK, s.issueTypePayloads())
	return nil
}

func (s *Server) issueTypePayloads() []issueTypePayload {
	payloads := make([]issueTypePayload, 0, len(s.Catalog.IssueTypes))
	for _, t := range s.Catalog.IssueTypes {
		payloads = append(payloads, mapIssueType(t))
	}

	return payloads
}

func (s *Server) priorities(w http.ResponseWriter, _ *http.Request, _ *Caller) error {
	payloads := make([]priorityPayload, 0, len(s.Catalog.Priorities))
	for i := range s.Catalog.Priorities {
		payloads = append(payloads, *mapPriority(&s.Catalog.Priorities[i]))
	}

	writeJSON(w, http.StatusOK, payloads)

	return nil
}

// users answers both user searches with the people who share a project with
// the caller. Server and Data Center filter by username, Cloud by query, and
// Orca asks for "." to mean everyone. An assignable search can name a project
// or an issue.
func (s *Server) users(w http.ResponseWriter, r *http.Request, c *Caller) error {
	query := r.URL.Query()

	search := firstNonEmpty(query.Get("username"), query.Get("query"))
	if search == "." {
		search = ""
	}

	maxResults, err := strconv.ParseInt(query.Get("maxResults"), 10, 32)
	if err != nil || maxResults <= 0 {
		maxResults = 50
	}

	maxResults = min(maxResults, 1000)

	params := db.SearchPeopleParams{
		PrincipalID: c.PrincipalID,
		ProjectIds:  c.ProjectIDs,
		Search:      search,
		MaxResults:  int32(maxResults),
	}

	switch {
	case query.Get("issueKey") != "":
		issue, err := s.issue(r.Context(), c, query.Get("issueKey"))
		if err != nil {
			return err
		}

		params.ProjectID = &issue.Issue.ProjectID
	case query.Get("project") != "":
		_, project, err := s.project(r.Context(), c, query.Get("project"))
		if err != nil {
			return err
		}

		params.ProjectID = &project.ID
	}

	people, err := s.Store.SearchPeople(r.Context(), params)
	if err != nil {
		return err
	}

	payloads := make([]userPayload, 0, len(people))
	for _, p := range people {
		payloads = append(payloads, *mapUser(&person{p.Username, p.DisplayName, p.Email, true}))
	}

	writeJSON(w, http.StatusOK, payloads)

	return nil
}

func (s *Server) favouriteFilters(w http.ResponseWriter, _ *http.Request, _ *Caller) error {
	writeJSON(w, http.StatusOK, []filterPayload{})
	return nil
}

// fields is the site's field catalog, which Orca searches for sprint and
// story point custom fields. There are none.
func (s *Server) fields(w http.ResponseWriter, _ *http.Request, _ *Caller) error {
	writeJSON(w, http.StatusOK, []struct{}{})
	return nil
}

func (s *Server) getIssue(w http.ResponseWriter, r *http.Request, c *Caller) error {
	row, err := s.issue(r.Context(), c, r.PathValue("key"))
	if err != nil {
		return err
	}

	expand := r.URL.Query().Get("expand")

	if s.created.suppresses(suppressionKey(c, row.Issue.ID), expand) {
		return notFound("Reading %s is suppressed briefly after creating it, to work around an Orca bug.", issueKey(row))
	}

	files, err := s.attachmentsOf(r, []int64{row.Issue.ID})
	if err != nil {
		return err
	}

	issue := s.mapIssue(row, true, files[row.Issue.ID])
	if strings.Contains(expand, "renderedFields") {
		issue.RenderedFields = &renderedFields{Description: renderMarkdown(row.Issue.Description)}
	}

	writeJSON(w, http.StatusOK, issue)

	return nil
}

func suppressionKey(c *Caller, issueID int64) string {
	return fmt.Sprintf("%d:%d", c.PrincipalID, issueID)
}

type issueRequest struct {
	Fields map[string]json.RawMessage `json:"fields"`
	Update struct {
		Labels []struct {
			Add    string `json:"add"`
			Remove string `json:"remove"`
		} `json:"labels"`
	} `json:"update"`
}

func (s *Server) createIssue(w http.ResponseWriter, r *http.Request, c *Caller) error {
	ctx := r.Context()

	var request issueRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		return badRequest("Malformed create request: %s", err)
	}

	projectRef := decodeReference(request.Fields["project"])
	if projectRef == "" {
		return badRequest("A project is required.")
	}

	membership, project, err := s.project(ctx, c, projectRef)
	if err != nil {
		return err
	}

	issue := store.NewIssue{
		ProjectID:   project.ID,
		Summary:     strings.TrimSpace(decodeString(request.Fields["summary"])),
		Description: decodeString(request.Fields["description"]),
		StatusID:    s.Catalog.Statuses[0].ID,
		TypeID:      s.Catalog.IssueTypes[0].ID,
		Reporter:    membership.ID,
		Labels:      decodeStrings(request.Fields["labels"]),
	}

	if issue.Summary == "" {
		return badRequest("Summary is required.")
	}

	if ref := decodeReference(request.Fields["issuetype"]); ref != "" {
		t, err := s.Catalog.IssueType(ref)
		if err != nil {
			return err
		}

		issue.TypeID = t.ID
	}

	// Jira doesn't take a status on create, but the CLI's --status does.
	if ref := decodeReference(request.Fields["status"]); ref != "" {
		status, err := s.Catalog.Status(ref)
		if err != nil {
			return err
		}

		issue.StatusID = status.ID
	}

	if ref := decodeReference(request.Fields["priority"]); ref != "" {
		p, err := s.Catalog.Priority(ref)
		if err != nil {
			return err
		}

		issue.PriorityID = &p.ID
	}

	if issue.Assignee, err = s.person(ctx, c, project.ID, decodeReference(request.Fields["assignee"])); err != nil {
		return err
	}

	created, err := s.Store.CreateIssue(ctx, issue)
	if err != nil {
		return err
	}

	s.created.record(suppressionKey(c, created.ID))

	key := fmt.Sprintf("%s-%d", membership.Key, created.Number)

	writeJSON(w, http.StatusCreated, map[string]string{
		"id":   id(created.ID),
		"key":  key,
		"self": fmt.Sprintf("%s/rest/api/2/issue/%d", s.BaseURL, created.ID),
	})

	return nil
}

func (s *Server) updateIssue(w http.ResponseWriter, r *http.Request, c *Caller) error {
	ctx := r.Context()

	var request issueRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		return badRequest("Malformed update request: %s", err)
	}

	row, err := s.issue(ctx, c, r.PathValue("key"))
	if err != nil {
		return err
	}

	var change store.IssueChange

	if raw, ok := request.Fields["summary"]; ok {
		summary := strings.TrimSpace(decodeString(raw))
		if summary == "" {
			return badRequest("Summary can't be empty.")
		}

		change.Summary = &summary
	}

	if raw, ok := request.Fields["description"]; ok {
		change.Description = new(decodeString(raw))
	}

	if raw, ok := request.Fields["labels"]; ok {
		change.Labels = new(decodeStrings(raw))
	}

	// The store applies these to the labels as they are when it locks the issue.
	for _, op := range request.Update.Labels {
		change.AddLabels = append(change.AddLabels, op.Add)
		change.RemoveLabels = append(change.RemoveLabels, op.Remove)
	}

	if raw, ok := request.Fields["priority"]; ok {
		// null clears the priority.
		change.SetPriority = true

		if ref := decodeReference(raw); ref != "" {
			p, err := s.Catalog.Priority(ref)
			if err != nil {
				return err
			}

			change.PriorityID = &p.ID
		}
	}

	if raw, ok := request.Fields["assignee"]; ok {
		change.SetAssignee = true
		if change.Assignee, err = s.person(ctx, c, row.Issue.ProjectID, decodeReference(raw)); err != nil {
			return err
		}
	}

	if ref := decodeReference(request.Fields["issuetype"]); ref != "" {
		t, err := s.Catalog.IssueType(ref)
		if err != nil {
			return err
		}

		change.TypeID = &t.ID
	}

	if err := s.apply(r, c, row, change); err != nil {
		return err
	}

	writeJSON(w, http.StatusNoContent, nil)

	return nil
}

func (s *Server) apply(r *http.Request, c *Caller, row db.VisibleIssuesRow, change store.IssueChange) error {
	actor, err := s.actor(r.Context(), c, row.Issue.ProjectID)
	if err != nil {
		return err
	}

	return s.Store.UpdateIssue(r.Context(), s.Catalog, actor, row.Issue.ID, change)
}

func (s *Server) assign(w http.ResponseWriter, r *http.Request, c *Caller) error {
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return badRequest("Malformed assignee request: %s", err)
	}

	row, err := s.issue(r.Context(), c, r.PathValue("key"))
	if err != nil {
		return err
	}

	// Server and Data Center name the assignee by name. accountId is accepted
	// too.
	assignee, err := s.person(r.Context(), c, row.Issue.ProjectID, firstNonEmpty(decodeString(body["name"]), decodeString(body["accountId"])))
	if err != nil {
		return err
	}

	if err := s.apply(r, c, row, store.IssueChange{SetAssignee: true, Assignee: assignee}); err != nil {
		return err
	}

	writeJSON(w, http.StatusNoContent, nil)

	return nil
}

// transitionID encodes the target status, so a transition needs no state of
// its own.
func transitionID(s db.Status) string { return "to-" + strconv.Itoa(int(s.ID)) }

func (s *Server) transitions(w http.ResponseWriter, r *http.Request, c *Caller) error {
	row, err := s.issue(r.Context(), c, r.PathValue("key"))
	if err != nil {
		return err
	}

	transitions := make([]transitionPayload, 0, len(s.Catalog.Statuses))

	for _, status := range s.Catalog.Statuses {
		if status.ID != row.Issue.StatusID {
			transitions = append(transitions, transitionPayload{ID: transitionID(status), Name: status.Name, To: mapStatus(status)})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"expand": "transitions", "transitions": transitions})

	return nil
}

func (s *Server) transition(w http.ResponseWriter, r *http.Request, c *Caller) error {
	var body struct {
		Transition struct {
			ID string `json:"id"`
		} `json:"transition"`
		Update struct {
			Comment []struct {
				Add struct {
					Body json.RawMessage `json:"body"`
				} `json:"add"`
			} `json:"comment"`
		} `json:"update"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return badRequest("Malformed transition request: %s", err)
	}

	status, err := s.Catalog.Status(strings.TrimPrefix(body.Transition.ID, "to-"))
	if err != nil || !strings.HasPrefix(body.Transition.ID, "to-") {
		return badRequest("Unknown transition %q.", body.Transition.ID)
	}

	row, err := s.issue(r.Context(), c, r.PathValue("key"))
	if err != nil {
		return err
	}

	change := store.IssueChange{StatusID: &status.ID}

	// Orca's transition dialog can attach a comment to the move.
	for _, comment := range body.Update.Comment {
		if text := decodeString(comment.Add.Body); strings.TrimSpace(text) != "" {
			change.Comment = text
		}
	}

	if err := s.apply(r, c, row, change); err != nil {
		return err
	}

	writeJSON(w, http.StatusNoContent, nil)

	return nil
}

func (s *Server) comments(w http.ResponseWriter, r *http.Request, c *Caller) error {
	row, err := s.issue(r.Context(), c, r.PathValue("key"))
	if err != nil {
		return err
	}

	comments, err := s.Store.ListComments(r.Context(), row.Issue.ID)
	if err != nil {
		return err
	}

	payloads := make([]commentPayload, 0, len(comments))
	for _, comment := range comments {
		payloads = append(payloads, s.mapComment(comment))
	}

	page := onePage(payloads)
	page.Comments = payloads
	writeJSON(w, http.StatusOK, page)

	return nil
}

func (s *Server) addComment(w http.ResponseWriter, r *http.Request, c *Caller) error {
	var body struct {
		Body json.RawMessage `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return badRequest("Malformed comment request: %s", err)
	}

	text := decodeString(body.Body)
	if strings.TrimSpace(text) == "" {
		return badRequest("Comment body is required.")
	}

	row, err := s.issue(r.Context(), c, r.PathValue("key"))
	if err != nil {
		return err
	}

	actor, err := s.actor(r.Context(), c, row.Issue.ProjectID)
	if err != nil {
		return err
	}

	comment, err := s.Store.AddComment(r.Context(), actor, row.Issue, text)
	if err != nil {
		return err
	}

	writeJSON(w, http.StatusCreated, s.mapComment(db.ListCommentsRow{
		Comment:        comment,
		AuthorUsername: c.Username,
		AuthorName:     c.DisplayName,
		AuthorEmail:    c.Email,
		AuthorActive:   true,
	}))

	return nil
}

func (s *Server) createMetaIssueTypes(w http.ResponseWriter, r *http.Request, c *Caller) error {
	if _, _, err := s.project(r.Context(), c, r.PathValue("project")); err != nil {
		return err
	}

	payloads := s.issueTypePayloads()
	page := onePage(payloads)
	page.IssueTypes = payloads
	writeJSON(w, http.StatusOK, page)

	return nil
}

// createMetaFields lists the fields the create dialog may send. Orca renders
// only the required ones, plus an assignee picker when assignee is listed.
func (s *Server) createMetaFields(w http.ResponseWriter, r *http.Request, c *Caller) error {
	_, project, err := s.project(r.Context(), c, r.PathValue("project"))
	if err != nil {
		return err
	}

	people, err := s.Store.SearchPeople(r.Context(), db.SearchPeopleParams{
		PrincipalID: c.PrincipalID,
		ProjectIds:  c.ProjectIDs,
		ProjectID:   &project.ID,
		MaxResults:  1000,
	})
	if err != nil {
		return err
	}

	priorities := make([]createFieldValue, 0, len(s.Catalog.Priorities))
	for _, p := range s.Catalog.Priorities {
		priorities = append(priorities, createFieldValue{ID: strconv.Itoa(int(p.ID)), Name: p.Name})
	}

	users := make([]createFieldValue, 0, len(people))
	for _, p := range people {
		users = append(users, createFieldValue{ID: p.Username, Name: firstNonEmpty(p.DisplayName, p.Username)})
	}

	fields := []createField{
		{FieldID: "summary", Key: "summary", Name: "Summary", Required: true, Schema: createFieldSchema{Type: "string"}},
		{FieldID: "description", Key: "description", Name: "Description", Schema: createFieldSchema{Type: "string"}},
		{FieldID: "labels", Key: "labels", Name: "Labels", Schema: createFieldSchema{Type: "array", Items: "string"}},
		{FieldID: "priority", Key: "priority", Name: "Priority", Schema: createFieldSchema{Type: "priority"}, AllowedValues: priorities},
		{FieldID: "assignee", Key: "assignee", Name: "Assignee", Schema: createFieldSchema{Type: "user"}, AllowedValues: users},
	}

	page := onePage(fields)
	page.Values = fields
	writeJSON(w, http.StatusOK, page)

	return nil
}

// boards gives each project one board, with the project's ID.
func (s *Server) boards(w http.ResponseWriter, r *http.Request, c *Caller) error {
	var boards []boardPayload

	if wanted := r.URL.Query().Get("projectKeyOrId"); wanted != "" {
		_, project, err := s.project(r.Context(), c, wanted)
		if err != nil {
			return err
		}

		boards = append(boards, boardPayload{ID: int(project.ID), Name: project.Name, Type: "kanban"})
	} else {
		rows, err := s.Store.ListMyProjects(r.Context(), c.PrincipalID)
		if err != nil {
			return err
		}

		for _, row := range rows {
			if c.canSee(row.Project.ID) {
				boards = append(boards, boardPayload{ID: int(row.Project.ID), Name: row.Project.Name, Type: "kanban"})
			}
		}
	}

	if boards == nil {
		boards = []boardPayload{}
	}

	page := onePage(boards)
	page.Values = boards
	writeJSON(w, http.StatusOK, page)

	return nil
}

// boardConfiguration gives every status a column of its own, in order.
func (s *Server) boardConfiguration(w http.ResponseWriter, r *http.Request, c *Caller) error {
	if _, err := strconv.ParseInt(r.PathValue("id"), 10, 64); err != nil {
		return notFound("Board %s doesn't exist.", r.PathValue("id"))
	}

	_, project, err := s.project(r.Context(), c, r.PathValue("id"))
	if err != nil {
		return notFound("Board %s doesn't exist.", r.PathValue("id"))
	}

	config := boardConfiguration{ID: int(project.ID), Name: project.Name}
	for _, status := range s.Catalog.Statuses {
		config.ColumnConfig.Columns = append(config.ColumnConfig.Columns, boardColumn{
			Name:     status.Name,
			Statuses: []boardStatus{{ID: strconv.Itoa(int(status.ID))}},
		})
	}

	writeJSON(w, http.StatusOK, config)

	return nil
}

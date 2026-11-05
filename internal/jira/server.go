// Package jira serves the subset of the Jira Server and Data Center REST API
// v2 that Orca and the CLI call. Server rather than Cloud on purpose: v2 sends
// plain-text bodies instead of Atlassian Document Format, so Markdown reaches
// Orca unchanged, and /project returns a plain array instead of a paged
// envelope.
package jira

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/leightweight/orca-tasks/internal/attachments"
	"github.com/leightweight/orca-tasks/internal/auth"
	"github.com/leightweight/orca-tasks/internal/store"
	"github.com/leightweight/orca-tasks/internal/store/db"
)

// Caller is the principal a request acts for, and what its credential allows.
type Caller struct {
	PrincipalID int64
	Username    string
	DisplayName string
	Email       string
	ReadOnly    bool

	// User is the user the caller acts for: itself, or an agent's owner.
	User string

	// ProjectIDs limits the caller to some projects. Nil means every project
	// the principal is a member of.
	ProjectIDs []int64
}

func (c *Caller) canSee(projectID int64) bool {
	return c.ProjectIDs == nil || slices.Contains(c.ProjectIDs, projectID)
}

// names are what currentUser() matches: the caller, and the user it acts for.
func (c *Caller) names() []string {
	if c.User == "" || c.User == c.Username {
		return []string{c.Username}
	}

	return []string{c.Username, c.User}
}

// Limiter counts failed sign-ins and API key checks.
type Limiter interface {
	// Allowed reports whether none of the keys is over its limit.
	Allowed(ctx context.Context, keys ...string) bool

	// Failed counts a failure against each key.
	Failed(ctx context.Context, keys ...string)
}

// Server is the API.
type Server struct {
	Store     *store.Store
	Catalog   *store.Catalog
	Files     *attachments.Dir
	BaseURL   string
	MaxUpload int64
	Log       *slog.Logger
	Limiter   Limiter
	ClientIP  func(*http.Request) string

	// SessionCaller returns the signed-in user of a web session, or nil. Only
	// attachment downloads accept a session, so /browse can show images.
	SessionCaller func(*http.Request) *Caller

	// Now is the clock JQL's relative dates use.
	Now func() time.Time

	created *createdIssueSuppression
}

// SuppressCreatedDetail turns on the created-issue workaround; see
// suppression.go.
func (s *Server) SuppressCreatedDetail(window time.Duration) {
	s.created = newCreatedIssueSuppression(window)
}

// handler is a route with its caller authenticated.
type handler func(w http.ResponseWriter, r *http.Request, c *Caller) error

// httpError is an error with the status it should be reported with.
type httpError struct {
	status  int
	message string
}

func (e *httpError) Error() string { return e.message }

func badRequest(format string, args ...any) error {
	return &httpError{status: http.StatusBadRequest, message: fmt.Sprintf(format, args...)}
}

func notFound(format string, args ...any) error {
	return &httpError{status: http.StatusNotFound, message: fmt.Sprintf(format, args...)}
}

func forbidden(format string, args ...any) error {
	return &httpError{status: http.StatusForbidden, message: fmt.Sprintf(format, args...)}
}

// Register adds the API's routes to a mux.
func (s *Server) Register(mux *http.ServeMux) {
	handle := func(pattern string, h handler) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			c, err := s.authenticate(r)
			if err == nil {
				err = s.checkAccess(r, c)
			}

			if err == nil {
				err = h(w, r, c)
			}

			if err != nil {
				s.fail(w, r, err)
			}
		})
	}

	handle("GET /rest/api/2/myself", s.myself)
	handle("POST /rest/api/2/search", s.searchPost)
	// Orca always POSTs. The GET form is for poking at it with curl.
	handle("GET /rest/api/2/search", s.searchGet)

	handle("GET /rest/api/2/project", s.projects)
	handle("GET /rest/api/2/status", s.statuses)
	handle("GET /rest/api/2/issuetype", s.issueTypes)
	handle("GET /rest/api/2/priority", s.priorities)
	handle("GET /rest/api/2/user/assignable/search", s.users)
	handle("GET /rest/api/2/user/search", s.users)
	handle("GET /rest/api/2/filter/favourite", s.favouriteFilters)
	handle("GET /rest/api/2/field", s.fields)

	handle("POST /rest/api/2/issue", s.createIssue)
	handle("GET /rest/api/2/issue/{key}", s.getIssue)
	handle("PUT /rest/api/2/issue/{key}", s.updateIssue)
	handle("PUT /rest/api/2/issue/{key}/assignee", s.assign)
	handle("GET /rest/api/2/issue/{key}/transitions", s.transitions)
	handle("POST /rest/api/2/issue/{key}/transitions", s.transition)
	handle("GET /rest/api/2/issue/{key}/comment", s.comments)
	handle("POST /rest/api/2/issue/{key}/comment", s.addComment)
	handle("POST /rest/api/2/issue/{key}/attachments", s.uploadAttachments)
	handle("GET /rest/api/2/attachment/{id}", s.attachment)
	handle("DELETE /rest/api/2/attachment/{id}", s.deleteAttachment)

	handle("GET /rest/api/2/issue/createmeta/{project}/issuetypes", s.createMetaIssueTypes)
	handle("GET /rest/api/2/issue/createmeta/{project}/issuetypes/{issueType}", s.createMetaFields)

	// Orca orders its board columns by the project's board, when it has
	// exactly one. Each project gets one, with a column per status.
	handle("GET /rest/agile/1.0/board", s.boards)
	handle("GET /rest/agile/1.0/board/{id}/configuration", s.boardConfiguration)

	// Downloads also accept a web session, for the images on /browse pages.
	download := func(w http.ResponseWriter, r *http.Request) {
		if err := s.download(w, r); err != nil {
			s.fail(w, r, err)
		}
	}

	mux.HandleFunc("GET /secure/attachment/{id}/{filename}", download)
	mux.HandleFunc("GET /secure/attachment/{id}", download)
	mux.HandleFunc("GET /rest/api/2/attachment/content/{id}", download)

	notFoundRoute := func(w http.ResponseWriter, r *http.Request) {
		s.Log.Warn("a request for a route the API doesn't have", "method", r.Method, "path", r.URL.Path)
		writeError(w, http.StatusNotFound, "No such resource: "+r.URL.Path)
	}

	mux.HandleFunc("/rest/", notFoundRoute)
}

var errBadCredentials = &httpError{status: http.StatusForbidden, message: "The API key is missing, wrong, expired, or revoked."}

var errTooManyFailures = &httpError{status: http.StatusTooManyRequests, message: "Too many failed attempts. Wait a few minutes and try again."}

// authenticate reads an API key from a bearer token, or from HTTP Basic with
// the principal's username.
func (s *Server) authenticate(r *http.Request) (*Caller, error) {
	ctx := r.Context()
	ip := "ip:" + s.ClientIP(r)

	if s.Limiter != nil && !s.Limiter.Allowed(ctx, ip) {
		return nil, errTooManyFailures
	}

	key, username := credentials(r)
	if !strings.HasPrefix(key, auth.APIKeyPrefix) {
		return nil, s.failed(ctx, ip)
	}

	row, err := s.Store.AuthenticateAPIKey(ctx, auth.HashToken(key))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && username != "" && !strings.EqualFold(username, row.Username)) {
		return nil, s.failed(ctx, ip)
	}

	if err != nil {
		return nil, err
	}

	if err := s.Store.TouchAPIKey(ctx, row.ID); err != nil {
		return nil, err
	}

	c := &Caller{
		PrincipalID: row.PrincipalID,
		Username:    row.Username,
		DisplayName: row.DisplayName,
		Email:       row.Email,
		ReadOnly:    row.ReadOnly,
		User:        row.UserUsername,
	}

	if !row.AllProjects {
		c.ProjectIDs = append([]int64{}, row.ProjectIds...)
	}

	return c, nil
}

func (s *Server) failed(ctx context.Context, ip string) error {
	if s.Limiter != nil {
		s.Limiter.Failed(ctx, ip)
	}

	return errBadCredentials
}

// credentials returns the API key from the Authorization header, and the
// username when it came by HTTP Basic.
func credentials(r *http.Request) (key, username string) {
	header := r.Header.Get("Authorization")

	scheme, value, _ := strings.Cut(header, " ")
	switch strings.ToLower(scheme) {
	case "bearer":
		return strings.TrimSpace(value), ""
	case "basic":
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
		if err != nil {
			return "", ""
		}

		user, password, _ := strings.Cut(string(decoded), ":")

		return password, user
	}

	return "", ""
}

// checkAccess stops read-only keys from changing anything. A search is a read,
// even though Orca POSTs it.
func (s *Server) checkAccess(r *http.Request, c *Caller) error {
	if !c.ReadOnly || r.Method == http.MethodGet || (r.Method == http.MethodPost && r.URL.Path == "/rest/api/2/search") {
		return nil
	}

	return forbidden("This API key is read-only.")
}

// fail reports an error a route returned.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	var status *httpError
	var unknown *store.UnknownError

	switch {
	case errors.As(err, &status):
		writeError(w, status.status, status.message)
	case errors.As(err, &unknown):
		writeError(w, http.StatusBadRequest, unknown.Error())
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "Not found.")
	default:
		s.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", err)
		writeError(w, http.StatusInternalServerError, "The server failed to handle the request.")
	}
}

// issue finds an issue the caller can see, by key under the caller's keys or
// by ID.
func (s *Server) issue(ctx context.Context, c *Caller, keyOrID string) (db.VisibleIssuesRow, error) {
	params := db.VisibleIssuesParams{PrincipalID: c.PrincipalID, ProjectIds: c.ProjectIDs}

	if n, err := strconv.ParseInt(keyOrID, 10, 64); err == nil {
		params.IssueID = &n
	} else {
		project, number, ok := strings.Cut(keyOrID, "-")
		n, err := strconv.ParseInt(number, 10, 32)

		if !ok || err != nil {
			return db.VisibleIssuesRow{}, notFound("Issue %s doesn't exist or you can't see it.", keyOrID)
		}

		params.Key = &project
		params.Number = new(int32(n))
	}

	rows, err := s.Store.VisibleIssues(ctx, params)
	if err != nil {
		return db.VisibleIssuesRow{}, err
	}

	if len(rows) != 1 {
		return db.VisibleIssuesRow{}, notFound("Issue %s doesn't exist or you can't see it.", keyOrID)
	}

	return rows[0], nil
}

// project finds a project the caller can see, by the caller's key for it or
// by ID, with the caller's membership.
func (s *Server) project(ctx context.Context, c *Caller, keyOrID string) (db.Membership, db.Project, error) {
	keyOrID = strings.TrimSpace(keyOrID)

	if n, err := strconv.ParseInt(keyOrID, 10, 64); err == nil {
		m, err := s.Store.GetMembership(ctx, db.GetMembershipParams{ProjectID: n, PrincipalID: c.PrincipalID})
		if err == nil && c.canSee(n) {
			p, err := s.Store.GetProject(ctx, n)
			return m, p, err
		}

		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return db.Membership{}, db.Project{}, err
		}
	} else {
		row, err := s.Store.GetMembershipByKey(ctx, db.GetMembershipByKeyParams{PrincipalID: c.PrincipalID, Key: keyOrID})
		if err == nil && c.canSee(row.Project.ID) {
			return row.Membership, row.Project, nil
		}

		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return db.Membership{}, db.Project{}, err
		}
	}

	return db.Membership{}, db.Project{}, badRequest("Unknown project %q.", keyOrID)
}

// actor is the caller's membership in an issue's project, which changes are
// recorded against.
func (s *Server) actor(ctx context.Context, c *Caller, projectID int64) (int64, error) {
	m, err := s.Store.GetMembership(ctx, db.GetMembershipParams{ProjectID: projectID, PrincipalID: c.PrincipalID})
	return m.ID, err
}

// person looks up someone to assign an issue to, who must be a user with an
// active membership in its project. Agents can't be assignees. Jira's "-1",
// automatic assignment, means the caller's user: an agent assigns its owner.
func (s *Server) person(ctx context.Context, c *Caller, projectID int64, username string) (*store.Person, error) {
	username = strings.TrimSpace(username)

	switch username {
	case "":
		return nil, nil
	case "-1":
		username = c.User
	}

	membership, err := s.Store.ActiveMembership(ctx, db.ActiveMembershipParams{ProjectID: projectID, Username: username})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, badRequest("%q isn't a user in the project, so the issue can't be assigned to them. Issues are assigned to users, not agents.", username)
	}

	if err != nil {
		return nil, err
	}

	return &store.Person{MembershipID: membership, Username: strings.ToLower(username)}, nil
}

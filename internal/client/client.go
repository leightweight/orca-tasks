// Package client calls the server's Jira-compatible API, for the CLI.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Client is an API client signed in with an API key.
type Client struct {
	BaseURL string
	Token   string

	// HTTP is for API calls, which finish in seconds.
	HTTP *http.Client

	// Uploads is for attachments, which can take minutes on a slow link, so
	// it has no deadline for the whole request: only the context and a limit
	// on waiting for the response once the file is sent.
	Uploads *http.Client
}

// callTimeout bounds an API call, and responseTimeout an upload's wait for
// the server to answer once its file is sent.
const (
	callTimeout     = 30 * time.Second
	responseTimeout = time.Minute
)

func newClient(baseURL, token string, transport *http.Transport) *Client {
	transport.ResponseHeaderTimeout = responseTimeout

	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: callTimeout, Transport: transport},
		Uploads: &http.Client{Transport: transport},
	}
}

// New returns a client with a request timeout.
func New(baseURL, token string) *Client {
	transport, _ := http.DefaultTransport.(*http.Transport)

	return newClient(baseURL, token, transport.Clone())
}

// NewSocket returns a client for a local server's Unix socket, which needs no
// API key: the server trusts it as the local agent.
func NewSocket(path string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		},
	}

	return newClient("http://localhost", "", transport)
}

// Error is a response the server refused. Status 400 means the request was
// wrong, such as an unknown status, and the message says what's valid.
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string { return e.Message }

// BadRequest reports whether an error is the server rejecting the input.
func BadRequest(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Status == http.StatusBadRequest
}

func (c *Client) do(ctx context.Context, method, path, contentType string, body io.Reader, into any) error {
	return c.doWith(ctx, c.HTTP, method, path, contentType, body, into)
}

func (c *Client) doWith(ctx context.Context, httpClient *http.Client, method, path, contentType string, body io.Reader, into any) error {
	request, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}

	if c.Token != "" {
		request.Header.Set("Authorization", "Bearer "+c.Token)
	}
	request.Header.Set("Accept", "application/json")

	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}

	request.Header.Set("X-Atlassian-Token", "no-check")

	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode >= 300 {
		var envelope struct {
			ErrorMessages []string          `json:"errorMessages"`
			Errors        map[string]string `json:"errors"`
		}

		data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<16))
		message := strings.TrimSpace(string(data))

		if json.Unmarshal(data, &envelope) == nil && len(envelope.ErrorMessages)+len(envelope.Errors) > 0 {
			messages := envelope.ErrorMessages
			for field, text := range envelope.Errors {
				messages = append(messages, field+": "+text)
			}

			message = strings.Join(messages, "; ")
		}

		return &Error{Status: response.StatusCode, Message: message}
	}

	if into == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}

	return json.NewDecoder(response.Body).Decode(into)
}

func (c *Client) get(ctx context.Context, path string, into any) error {
	return c.do(ctx, http.MethodGet, path, "", nil, into)
}

func (c *Client) send(ctx context.Context, method, path string, payload, into any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	return c.do(ctx, method, path, "application/json", bytes.NewReader(data), into)
}

// Named is anything with an ID and a name, such as a status or priority.
type Named struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Status is a status and its category.
type Status struct {
	Named

	StatusCategory struct {
		Key string `json:"key"`
	} `json:"statusCategory"`
}

// User is a person or an agent.
type User struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Active      bool   `json:"active"`
}

// Project is a project under the caller's key for it.
type Project struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

// Attachment is a file on an issue.
type Attachment struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
	Content  string `json:"content"`
	Author   *User  `json:"author"`
	Created  string `json:"created"`
}

// Issue is an issue with the fields the CLI shows.
type Issue struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Fields struct {
		Summary     string       `json:"summary"`
		Description string       `json:"description"`
		Status      Status       `json:"status"`
		IssueType   Named        `json:"issuetype"`
		Priority    *Named       `json:"priority"`
		Assignee    *User        `json:"assignee"`
		Reporter    *User        `json:"reporter"`
		Labels      []string     `json:"labels"`
		Created     string       `json:"created"`
		Updated     string       `json:"updated"`
		Project     Project      `json:"project"`
		Parent      *Parent      `json:"parent"`
		Attachment  []Attachment `json:"attachment"`
	} `json:"fields"`
}

// Parent is the issue an issue belongs to, such as an epic.
type Parent struct {
	Key    string `json:"key"`
	Fields struct {
		Summary string `json:"summary"`
	} `json:"fields"`
}

// Comment is a comment on an issue.
type Comment struct {
	ID      string `json:"id"`
	Body    string `json:"body"`
	Author  *User  `json:"author"`
	Created string `json:"created"`
}

// Time parses a Jira timestamp.
func Time(value string) time.Time {
	t, _ := time.Parse("2006-01-02T15:04:05.000-0700", value)
	return t
}

// Projects lists the projects the key can reach.
func (c *Client) Projects(ctx context.Context) ([]Project, error) {
	var projects []Project
	return projects, c.get(ctx, "/rest/api/2/project", &projects)
}

// Statuses lists the statuses.
func (c *Client) Statuses(ctx context.Context) ([]Status, error) {
	var statuses []Status
	return statuses, c.get(ctx, "/rest/api/2/status", &statuses)
}

// IssueTypes lists the issue types.
func (c *Client) IssueTypes(ctx context.Context) ([]Named, error) {
	var types []Named
	return types, c.get(ctx, "/rest/api/2/issuetype", &types)
}

// Priorities lists the priorities.
func (c *Client) Priorities(ctx context.Context) ([]Named, error) {
	var priorities []Named
	return priorities, c.get(ctx, "/rest/api/2/priority", &priorities)
}

// Users lists the people who share a project with the caller.
func (c *Client) Users(ctx context.Context) ([]User, error) {
	var users []User
	return users, c.get(ctx, "/rest/api/2/user/search?username=.&maxResults=1000", &users)
}

// Search returns every issue matching a JQL query, a page at a time.
func (c *Client) Search(ctx context.Context, jql string) ([]Issue, error) {
	var issues []Issue

	for {
		var page struct {
			Total  int     `json:"total"`
			IsLast bool    `json:"isLast"`
			Issues []Issue `json:"issues"`
		}

		request := map[string]any{"jql": jql, "startAt": len(issues), "maxResults": 100}
		if err := c.send(ctx, http.MethodPost, "/rest/api/2/search", request, &page); err != nil {
			return nil, err
		}

		issues = append(issues, page.Issues...)

		if page.IsLast || len(page.Issues) == 0 || len(issues) >= page.Total {
			return issues, nil
		}
	}
}

// Issue reads one issue.
func (c *Client) Issue(ctx context.Context, key string) (*Issue, error) {
	var issue Issue
	return &issue, c.get(ctx, "/rest/api/2/issue/"+url.PathEscape(key), &issue)
}

// Comments lists an issue's comments, oldest first.
func (c *Client) Comments(ctx context.Context, key string) ([]Comment, error) {
	var page struct {
		Comments []Comment `json:"comments"`
	}

	return page.Comments, c.get(ctx, "/rest/api/2/issue/"+url.PathEscape(key)+"/comment", &page)
}

// CreateIssue creates an issue from Jira fields and returns its key.
func (c *Client) CreateIssue(ctx context.Context, fields map[string]any) (string, error) {
	var created struct {
		Key string `json:"key"`
	}

	return created.Key, c.send(ctx, http.MethodPost, "/rest/api/2/issue", map[string]any{"fields": fields}, &created)
}

// UpdateIssue changes an issue's fields and labels.
func (c *Client) UpdateIssue(ctx context.Context, key string, fields map[string]any, addLabels, removeLabels []string) error {
	labels := make([]map[string]string, 0, len(addLabels)+len(removeLabels))
	for _, label := range addLabels {
		labels = append(labels, map[string]string{"add": label})
	}

	for _, label := range removeLabels {
		labels = append(labels, map[string]string{"remove": label})
	}

	payload := map[string]any{"fields": fields}
	if len(labels) > 0 {
		payload["update"] = map[string]any{"labels": labels}
	}

	return c.send(ctx, http.MethodPut, "/rest/api/2/issue/"+url.PathEscape(key), payload, nil)
}

// Move moves an issue to the status with a name, with an optional comment.
// An unknown status is a bad request listing the valid ones.
func (c *Client) Move(ctx context.Context, key, status, comment string) error {
	var available struct {
		Transitions []struct {
			ID string `json:"id"`
			To Status `json:"to"`
		} `json:"transitions"`
	}

	if err := c.get(ctx, "/rest/api/2/issue/"+url.PathEscape(key)+"/transitions", &available); err != nil {
		return err
	}

	names := make([]string, 0, len(available.Transitions))

	for _, t := range available.Transitions {
		if strings.EqualFold(t.To.Name, strings.TrimSpace(status)) {
			payload := map[string]any{"transition": map[string]string{"id": t.ID}}
			if strings.TrimSpace(comment) != "" {
				payload["update"] = map[string]any{"comment": []any{map[string]any{"add": map[string]string{"body": comment}}}}
			}

			return c.send(ctx, http.MethodPost, "/rest/api/2/issue/"+url.PathEscape(key)+"/transitions", payload, nil)
		}

		names = append(names, strconv.Quote(t.To.Name))
	}

	return &Error{
		Status:  http.StatusBadRequest,
		Message: fmt.Sprintf("can't move %s to %q; use one of %s", key, status, strings.Join(names, ", ")),
	}
}

// Assign assigns an issue. "-1" assigns the caller's user, which for an
// agent is its owner.
func (c *Client) Assign(ctx context.Context, key, username string) error {
	return c.send(ctx, http.MethodPut, "/rest/api/2/issue/"+url.PathEscape(key)+"/assignee", map[string]string{"name": username}, nil)
}

// DeleteIssue deletes an issue. The server refuses one with child issues, and
// lets only its reporter or a project administrator delete it.
func (c *Client) DeleteIssue(ctx context.Context, key string) error {
	return c.do(ctx, http.MethodDelete, "/rest/api/2/issue/"+url.PathEscape(key), "", nil, nil)
}

// AddComment comments on an issue.
func (c *Client) AddComment(ctx context.Context, key, body string) (*Comment, error) {
	var comment Comment
	return &comment, c.send(ctx, http.MethodPost, "/rest/api/2/issue/"+url.PathEscape(key)+"/comment", map[string]string{"body": body}, &comment)
}

// Attach uploads a file to an issue.
func (c *Client) Attach(ctx context.Context, key, path string) (*Attachment, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	// Stream the file through a pipe rather than reading it into memory.
	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)

	go func() {
		part, err := form.CreateFormFile("file", filepath.Base(path))
		if err == nil {
			_, err = io.Copy(part, file)
		}

		if err == nil {
			err = form.Close()
		}

		_ = writer.CloseWithError(err)
	}()

	var attachments []Attachment
	route := "/rest/api/2/issue/" + url.PathEscape(key) + "/attachments"
	if err := c.doWith(ctx, c.Uploads, http.MethodPost, route, form.FormDataContentType(), reader, &attachments); err != nil {
		return nil, err
	}

	if len(attachments) != 1 {
		return nil, fmt.Errorf("the server returned %d attachments for one file", len(attachments))
	}

	return &attachments[0], nil
}

// Package cli is the task commands: kong command structs that call the
// server's API and print tables or JSON.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/jedib0t/go-pretty/v6/table"

	"github.com/leightweight/orca-tasks/internal/client"
)

// UsageError is a mistake in the command's input, which exits 2.
type UsageError struct{ Err error }

func (e UsageError) Error() string { return e.Err.Error() }
func (e UsageError) Unwrap() error { return e.Err }

// Env is what the commands write to, so tests can capture it.
type Env struct {
	Stdout io.Writer
	Stdin  io.Reader
}

// Flags are the connection and output flags every command takes.
type Flags struct {
	URL    string `env:"ORCA_TASKS_URL" required:"" placeholder:"URL" help:"The server's URL."`
	Token  string `env:"ORCA_TASKS_TOKEN" required:"" placeholder:"KEY" help:"An API key from the server's API keys page."`
	Format string `short:"f" default:"table" enum:"table,json" help:"Output format: table or json."`
}

func (f *Flags) client() *client.Client { return client.New(f.URL, f.Token) }

// check turns the server rejecting the input into a usage error.
func check(err error) error {
	if client.BadRequest(err) {
		return UsageError{err}
	}

	return err
}

func (f *Flags) render(env *Env, data any, t table.Writer) error {
	if f.Format == "json" {
		encoder := json.NewEncoder(env.Stdout)
		encoder.SetIndent("", "  ")

		return encoder.Encode(data)
	}

	t.SetOutputMirror(env.Stdout)
	t.SetStyle(table.StyleLight)
	t.Render()

	return nil
}

// readArg reads stdin for "-" and returns anything else as it is.
func readArg(env *Env, value string) (string, error) {
	if value != "-" {
		return value, nil
	}

	data, err := io.ReadAll(env.Stdin)
	if err != nil {
		return "", fmt.Errorf("reading stdin: %w", err)
	}

	return string(data), nil
}

// Catalog lists what the other commands accept.
type Catalog struct {
	Flags
}

type catalogEntry struct {
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Detail string `json:"detail"`
}

// Run lists every project, status, type, priority, and user, so an agent can
// look them up instead of guessing.
func (c *Catalog) Run(ctx context.Context, env *Env) error {
	api := c.client()

	projects, err := api.Projects(ctx)
	if err != nil {
		return err
	}

	statuses, err := api.Statuses(ctx)
	if err != nil {
		return err
	}

	types, err := api.IssueTypes(ctx)
	if err != nil {
		return err
	}

	priorities, err := api.Priorities(ctx)
	if err != nil {
		return err
	}

	users, err := api.Users(ctx)
	if err != nil {
		return err
	}

	entries := []catalogEntry{}

	for _, p := range projects {
		entries = append(entries, catalogEntry{"project", p.Key, p.Name})
	}

	for _, s := range statuses {
		entries = append(entries, catalogEntry{"status", s.Name, s.StatusCategory.Key})
	}

	for _, t := range types {
		entries = append(entries, catalogEntry{"type", t.Name, t.Description})
	}

	for _, p := range priorities {
		entries = append(entries, catalogEntry{"priority", p.Name, ""})
	}

	for _, u := range users {
		entries = append(entries, catalogEntry{"user", u.Name, u.DisplayName})
	}

	t := table.NewWriter()
	t.AppendHeader(table.Row{"Kind", "Name", "Detail"})

	for _, e := range entries {
		t.AppendRow(table.Row{e.Kind, e.Name, e.Detail})
	}

	return c.render(env, entries, t)
}

// ProjectList lists the projects the key can reach.
type ProjectList struct {
	Flags
}

// Run lists the projects.
func (c *ProjectList) Run(ctx context.Context, env *Env) error {
	projects, err := c.client().Projects(ctx)
	if err != nil {
		return err
	}

	t := table.NewWriter()
	t.AppendHeader(table.Row{"Key", "Name"})

	for _, p := range projects {
		t.AppendRow(table.Row{p.Key, p.Name})
	}

	return c.render(env, projects, t)
}

// summary is an issue as `task list` and the change commands print it.
type summary struct {
	Key      string    `json:"key"`
	Summary  string    `json:"summary"`
	Status   string    `json:"status"`
	Type     string    `json:"type"`
	Priority string    `json:"priority"`
	Assignee string    `json:"assignee"`
	Labels   []string  `json:"labels"`
	Updated  time.Time `json:"updated"`
}

func summarize(i client.Issue) summary {
	s := summary{
		Key:     i.Key,
		Summary: i.Fields.Summary,
		Status:  i.Fields.Status.Name,
		Type:    i.Fields.IssueType.Name,
		Labels:  i.Fields.Labels,
		Updated: client.Time(i.Fields.Updated),
	}

	if i.Fields.Priority != nil {
		s.Priority = i.Fields.Priority.Name
	}

	if i.Fields.Assignee != nil {
		s.Assignee = i.Fields.Assignee.Name
	}

	if s.Labels == nil {
		s.Labels = []string{}
	}

	return s
}

func (f *Flags) renderIssues(env *Env, issues []client.Issue) error {
	rows := make([]summary, 0, len(issues))
	for _, i := range issues {
		rows = append(rows, summarize(i))
	}

	t := table.NewWriter()
	t.AppendHeader(table.Row{"Key", "Summary", "Status", "Type", "Priority", "Assignee", "Labels", "Updated"})

	for _, s := range rows {
		t.AppendRow(table.Row{s.Key, s.Summary, s.Status, s.Type, s.Priority, s.Assignee, strings.Join(s.Labels, ", "), s.Updated.Local().Format(time.DateTime)})
	}

	return f.render(env, rows, t)
}

// TaskList lists tasks.
type TaskList struct {
	Flags

	JQL string `arg:"" optional:"" help:"Only tasks matching this JQL, such as 'statusCategory != Done ORDER BY key'."`
}

// Run lists the tasks matching the query.
func (c *TaskList) Run(ctx context.Context, env *Env) error {
	issues, err := c.client().Search(ctx, c.JQL)
	if err != nil {
		return check(err)
	}

	return c.renderIssues(env, issues)
}

// TaskShow shows one task.
type TaskShow struct {
	Flags

	Key string `arg:"" help:"Task key, such as OPS-1."`
}

type detail struct {
	summary

	Project     string             `json:"project"`
	Category    string             `json:"statusCategory"`
	Reporter    string             `json:"reporter"`
	Created     time.Time          `json:"created"`
	Description string             `json:"description"`
	Comments    []commentDetail    `json:"comments"`
	Attachments []attachmentDetail `json:"attachments"`
}

type commentDetail struct {
	ID      string    `json:"id"`
	Author  string    `json:"author"`
	Created time.Time `json:"created"`
	Body    string    `json:"body"`
}

type attachmentDetail struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	URL      string `json:"url"`
}

// Run shows the task's fields, description, comments, and attachments.
func (c *TaskShow) Run(ctx context.Context, env *Env) error {
	api := c.client()

	issue, err := api.Issue(ctx, c.Key)
	if err != nil {
		return check(err)
	}

	comments, err := api.Comments(ctx, c.Key)
	if err != nil {
		return err
	}

	d := detail{
		summary:     summarize(*issue),
		Project:     issue.Fields.Project.Key,
		Category:    issue.Fields.Status.StatusCategory.Key,
		Created:     client.Time(issue.Fields.Created),
		Description: issue.Fields.Description,
		Comments:    []commentDetail{},
		Attachments: []attachmentDetail{},
	}

	if issue.Fields.Reporter != nil {
		d.Reporter = issue.Fields.Reporter.Name
	}

	for _, comment := range comments {
		author := ""
		if comment.Author != nil {
			author = comment.Author.Name
		}

		d.Comments = append(d.Comments, commentDetail{ID: comment.ID, Author: author, Created: client.Time(comment.Created), Body: comment.Body})
	}

	for _, a := range issue.Fields.Attachment {
		d.Attachments = append(d.Attachments, attachmentDetail{ID: a.ID, Filename: a.Filename, Size: a.Size, URL: a.Content})
	}

	if c.Format == "json" {
		return c.render(env, d, nil)
	}

	fields := table.NewWriter()
	fields.SetTitle(d.Key)
	fields.AppendRows([]table.Row{
		{"Summary", d.Summary},
		{"Status", d.Status},
		{"Type", d.Type},
		{"Priority", d.Priority},
		{"Assignee", d.Assignee},
		{"Reporter", d.Reporter},
		{"Labels", strings.Join(d.Labels, ", ")},
		{"Created", d.Created.Local().Format(time.DateTime)},
		{"Updated", d.Updated.Local().Format(time.DateTime)},
		{"Description", d.Description},
	})

	if err := c.render(env, d, fields); err != nil {
		return err
	}

	if len(d.Attachments) > 0 {
		files := table.NewWriter()
		files.SetTitle("Attachments")
		files.AppendHeader(table.Row{"ID", "File", "Size", "URL"})

		for _, a := range d.Attachments {
			files.AppendRow(table.Row{a.ID, a.Filename, a.Size, a.URL})
		}

		if err := c.render(env, d, files); err != nil {
			return err
		}
	}

	if len(d.Comments) > 0 {
		list := table.NewWriter()
		list.SetTitle("Comments")
		list.AppendHeader(table.Row{"ID", "Created", "Author", "Comment"})

		for _, comment := range d.Comments {
			list.AppendRow(table.Row{comment.ID, comment.Created.Local().Format(time.DateTime), comment.Author, comment.Body})
		}

		return c.render(env, d, list)
	}

	return nil
}

// TaskCreate creates a task.
type TaskCreate struct {
	Flags

	Project     string   `arg:"" help:"Project key, such as OPS."`
	Summary     string   `arg:"" help:"One-line title."`
	Description string   `help:"Markdown body. - reads it from stdin."`
	Type        string   `help:"Task type. Defaults to Task."`
	Status      string   `help:"Starting status. Defaults to To Do."`
	Priority    string   `help:"Priority. Defaults to none."`
	Assignee    string   `help:"Username to assign it to. Defaults to nobody."`
	Label       []string `help:"Label to add. Repeatable."`
}

// Run creates the task and prints its key.
func (c *TaskCreate) Run(ctx context.Context, env *Env) error {
	description, err := readArg(env, c.Description)
	if err != nil {
		return err
	}

	if strings.TrimSpace(c.Summary) == "" {
		return UsageError{errors.New("the summary can't be empty")}
	}

	fields := map[string]any{
		"project":     map[string]string{"key": c.Project},
		"summary":     c.Summary,
		"description": description,
		"labels":      append([]string{}, c.Label...),
	}

	for field, value := range map[string]string{"issuetype": c.Type, "status": c.Status, "priority": c.Priority, "assignee": c.Assignee} {
		if strings.TrimSpace(value) != "" {
			fields[field] = map[string]string{"name": value}
		}
	}

	api := c.client()

	key, err := api.CreateIssue(ctx, fields)
	if err != nil {
		return check(err)
	}

	return c.printIssue(ctx, env, api, key)
}

func (f *Flags) printIssue(ctx context.Context, env *Env, api *client.Client, key string) error {
	issue, err := api.Issue(ctx, key)
	if err != nil {
		return err
	}

	return f.renderIssues(env, []client.Issue{*issue})
}

// TaskEdit changes a task.
type TaskEdit struct {
	Flags

	Key         string   `arg:"" help:"Task key, such as OPS-1."`
	Summary     *string  `help:"New title."`
	Description *string  `help:"New Markdown body, replacing the old one. - reads it from stdin."`
	Type        *string  `help:"New task type."`
	Priority    *string  `help:"New priority. Empty clears it."`
	Assignee    *string  `help:"Username to assign it to. Empty unassigns it."`
	AddLabel    []string `help:"Label to add. Repeatable."`
	RemoveLabel []string `help:"Label to remove. Repeatable."`
}

// Run applies the changes and prints the task.
func (c *TaskEdit) Run(ctx context.Context, env *Env) error {
	fields := map[string]any{}

	if c.Summary != nil {
		if strings.TrimSpace(*c.Summary) == "" {
			return UsageError{errors.New("the summary can't be empty")}
		}

		fields["summary"] = *c.Summary
	}

	if c.Description != nil {
		description, err := readArg(env, *c.Description)
		if err != nil {
			return err
		}

		fields["description"] = description
	}

	if c.Type != nil {
		fields["issuetype"] = map[string]string{"name": *c.Type}
	}

	for field, value := range map[string]*string{"priority": c.Priority, "assignee": c.Assignee} {
		if value == nil {
			continue
		}

		if strings.TrimSpace(*value) == "" {
			fields[field] = nil
		} else {
			fields[field] = map[string]string{"name": *value}
		}
	}

	api := c.client()

	if err := api.UpdateIssue(ctx, c.Key, fields, c.AddLabel, c.RemoveLabel); err != nil {
		return check(err)
	}

	return c.printIssue(ctx, env, api, c.Key)
}

// TaskMove moves a task to another status.
type TaskMove struct {
	Flags

	Key     string `arg:"" help:"Task key, such as OPS-1."`
	Status  string `arg:"" help:"Status to move it to, such as \"In Progress\" or Done."`
	Comment string `help:"Comment to add with the move. - reads it from stdin."`
}

// Run moves the task and prints it.
func (c *TaskMove) Run(ctx context.Context, env *Env) error {
	comment, err := readArg(env, c.Comment)
	if err != nil {
		return err
	}

	api := c.client()

	if err := api.Move(ctx, c.Key, c.Status, comment); err != nil {
		return check(err)
	}

	return c.printIssue(ctx, env, api, c.Key)
}

// TaskComment comments on a task.
type TaskComment struct {
	Flags

	Key  string `arg:"" help:"Task key, such as OPS-1."`
	Body string `arg:"" help:"Markdown comment. - reads it from stdin."`
}

// Run adds the comment and prints it.
func (c *TaskComment) Run(ctx context.Context, env *Env) error {
	body, err := readArg(env, c.Body)
	if err != nil {
		return err
	}

	if strings.TrimSpace(body) == "" {
		return UsageError{errors.New("the comment can't be empty")}
	}

	comment, err := c.client().AddComment(ctx, c.Key, body)
	if err != nil {
		return check(err)
	}

	t := table.NewWriter()
	t.AppendHeader(table.Row{"Task", "ID", "Created"})
	t.AppendRow(table.Row{strings.ToUpper(c.Key), comment.ID, client.Time(comment.Created).Local().Format(time.DateTime)})

	return c.render(env, comment, t)
}

// TaskAttach uploads files to a task.
type TaskAttach struct {
	Flags

	Key   string   `arg:"" help:"Task key, such as OPS-1."`
	Files []string `arg:"" type:"existingfile" help:"Files to attach."`
}

type attached struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	URL      string `json:"url"`
	Markdown string `json:"markdown"`
}

// Run uploads each file and prints the Markdown that embeds it.
func (c *TaskAttach) Run(ctx context.Context, env *Env) error {
	api := c.client()
	results := make([]attached, 0, len(c.Files))

	for _, path := range c.Files {
		a, err := api.Attach(ctx, c.Key, path)
		if err != nil {
			return check(err)
		}

		markdown := fmt.Sprintf("[%s](%s)", a.Filename, a.Content)
		if strings.HasPrefix(a.MimeType, "image/") {
			markdown = "!" + markdown
		}

		results = append(results, attached{ID: a.ID, Filename: a.Filename, URL: a.Content, Markdown: markdown})
	}

	if c.Format == "json" {
		return c.render(env, results, nil)
	}

	for _, r := range results {
		if _, err := fmt.Fprintln(env.Stdout, r.Markdown); err != nil {
			return err
		}
	}

	return nil
}

// DefaultEnv is the process's standard input and output.
func DefaultEnv() *Env {
	return &Env{Stdout: os.Stdout, Stdin: os.Stdin}
}

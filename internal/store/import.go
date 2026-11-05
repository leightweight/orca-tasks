package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/leightweight/orca-tasks/internal/store/db"
)

// ImportFormat and ImportVersion identify an export file this server reads.
const (
	ImportFormat  = "orca-tasks-export"
	ImportVersion = 1
)

// ImportFile is an export of projects, issues, and comments from another task
// list. People are named by the usernames the export knows them by.
type ImportFile struct {
	Format   string          `json:"format"`
	Version  int             `json:"version"`
	Projects []ImportProject `json:"projects"`
}

// ImportProject is a project and its issues.
type ImportProject struct {
	Key        string        `json:"key"`
	Name       string        `json:"name"`
	NextNumber int32         `json:"next_number"`
	Issues     []ImportIssue `json:"issues"`
}

// ImportIssue is an issue, keeping its number and times.
type ImportIssue struct {
	Number      int32           `json:"number"`
	Summary     string          `json:"summary"`
	Description string          `json:"description"`
	Status      string          `json:"status"`
	Type        string          `json:"type"`
	Priority    string          `json:"priority"`
	Assignee    string          `json:"assignee"`
	Reporter    string          `json:"reporter"`
	Labels      []string        `json:"labels"`
	Created     time.Time       `json:"created"`
	Updated     time.Time       `json:"updated"`
	Comments    []ImportComment `json:"comments"`
}

// ImportComment is a comment, keeping its author and times.
type ImportComment struct {
	Author  string    `json:"author"`
	Body    string    `json:"body"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// ImportOptions says who owns the imported projects and who the export's
// people are here.
type ImportOptions struct {
	// Owner is the username of the user who administers every imported
	// project. An issue or comment with no reporter or author is theirs.
	Owner string

	// Users maps the export's usernames to this server's. A name it doesn't
	// list is looked up as a user, then as one of the owner's agents.
	Users map[string]string

	// DryRun checks and applies everything, then rolls it back.
	DryRun bool
}

// ImportedProject is what an import did to one project.
type ImportedProject struct {
	Key      string
	Issues   int
	Comments int
}

// ErrDryRun is returned by a dry run that would have succeeded.
var ErrDryRun = errors.New("dry run: nothing was saved")

// Import creates the projects in an export, with their issues and comments,
// in one transaction. Issues keep their numbers, so the owner sees them under
// the same keys as before, and issues and comments keep their times and
// people. It fails, saving nothing, if anything in the file doesn't resolve.
func (s *Store) Import(ctx context.Context, catalog *Catalog, file ImportFile, options ImportOptions) ([]ImportedProject, error) {
	if file.Format != ImportFormat || file.Version != ImportVersion {
		return nil, fmt.Errorf("this isn't a version %d %s file (format %q, version %d)", ImportVersion, ImportFormat, file.Format, file.Version)
	}

	var results []ImportedProject

	err := s.InTx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		im := &importer{q: q, catalog: catalog, options: options, principals: map[string]db.Principal{}}

		owner, err := q.GetPrincipalByUsername(ctx, options.Owner)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("there's no user named %q to own the projects; create it first", options.Owner)
		}

		if err != nil {
			return err
		}

		if owner.Kind != "user" {
			return fmt.Errorf("the owner, %s, is an agent; name a user", owner.Username)
		}

		im.owner = owner

		if err := im.resolveAll(ctx, file); err != nil {
			return err
		}

		taken, err := q.ActiveKeys(ctx, owner.ID)
		if err != nil {
			return err
		}

		for _, p := range file.Projects {
			result, err := im.project(ctx, p, taken)
			if err != nil {
				return fmt.Errorf("project %s: %w", p.Key, err)
			}

			taken = append(taken, result.Key)
			results = append(results, result)
		}

		if options.DryRun {
			return ErrDryRun
		}

		return nil
	})

	return results, err
}

type importer struct {
	q          *db.Queries
	catalog    *Catalog
	options    ImportOptions
	owner      db.Principal
	principals map[string]db.Principal
}

// resolve finds the principal an export's username maps to, for mustPrincipal
// to return. An empty name is the owner.
func (im *importer) resolve(ctx context.Context, name string) error {
	if _, ok := im.principals[name]; ok || name == "" {
		return nil
	}

	username := name
	if mapped, ok := im.options.Users[name]; ok {
		username = mapped
	}

	p, err := im.q.GetPrincipalByUsername(ctx, username)

	// A name that isn't a user may be one of the owner's agents, which export
	// files usually name without their owner.
	if errors.Is(err, pgx.ErrNoRows) && !strings.Contains(username, AgentSeparator) {
		p, err = im.q.GetPrincipalByUsername(ctx, im.owner.Username+AgentSeparator+username)
	}

	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("there's no user named %q, and %s has no agent by that name; map it with --user %s=USERNAME", username, im.owner.Username, name)
	}

	if err != nil {
		return err
	}

	im.principals[name] = p

	return nil
}

// resolveAll checks the whole file before anything is written, so a failed
// import reports every problem at once: unknown names, duplicate keys and
// numbers, empty summaries, and missing or backward times.
func (im *importer) resolveAll(ctx context.Context, file ImportFile) error {
	var problems []string

	check := func(err error) {
		if err != nil && !slices.Contains(problems, err.Error()) {
			problems = append(problems, err.Error())
		}
	}

	keys := map[string]bool{}

	for _, p := range file.Projects {
		key, err := NormalizeKey(p.Key)
		if err != nil {
			check(fmt.Errorf("project key %q: %w", p.Key, err))
		} else if keys[key] {
			check(fmt.Errorf("the file has more than one project %s", key))
		}

		keys[key] = true
		numbers := map[int32]bool{}

		for _, i := range p.Issues {
			issue := fmt.Sprintf("%s-%d", p.Key, i.Number)

			switch {
			case i.Number <= 0:
				check(fmt.Errorf("%s: issue numbers start at 1", issue))
			case numbers[i.Number]:
				check(fmt.Errorf("%s appears more than once", issue))
			}

			numbers[i.Number] = true

			if strings.TrimSpace(i.Summary) == "" {
				check(fmt.Errorf("%s has no summary", issue))
			}

			check(checkTimes(issue, i.Created, i.Updated))

			for n, c := range i.Comments {
				check(checkTimes(fmt.Sprintf("comment %d on %s", n+1, issue), c.Created, c.Updated))
			}

			for _, name := range []string{i.Reporter, i.Assignee} {
				if name != "" {
					check(im.resolve(ctx, name))
				}
			}

			for _, c := range i.Comments {
				check(im.resolve(ctx, c.Author))
			}

			_, err := im.catalog.Status(i.Status)
			check(err)

			_, err = im.catalog.IssueType(i.Type)
			check(err)

			if i.Priority != "" {
				_, err = im.catalog.Priority(i.Priority)
				check(err)
			}
		}
	}

	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "\n"))
	}

	return nil
}

// checkTimes requires both times, and an update no earlier than the
// creation. A missing time is refused rather than replaced, since a guess
// would misdate the history.
func checkTimes(what string, created, updated time.Time) error {
	switch {
	case created.IsZero() || updated.IsZero():
		return fmt.Errorf("%s needs both created and updated times", what)
	case updated.Before(created):
		return fmt.Errorf("%s was updated before it was created", what)
	default:
		return nil
	}
}

// project creates one project and copies its issues and comments in.
func (im *importer) project(ctx context.Context, p ImportProject, taken []string) (ImportedProject, error) {
	key, err := NormalizeKey(p.Key)
	if err != nil {
		return ImportedProject{}, err
	}

	// The owner must see the project under its old key, or every issue key
	// that Orca or a note refers to would change.
	if slices.Contains(taken, key) {
		return ImportedProject{}, fmt.Errorf("%s already uses the key %s for another project; rename that one first", im.owner.Username, key)
	}

	project, err := im.q.InsertProject(ctx, db.InsertProjectParams{Key: key, Name: cmpOr(strings.TrimSpace(p.Name), key)})
	if err != nil {
		return ImportedProject{}, err
	}

	if _, err := addMember(ctx, im.q, project, im.owner.ID, RoleAdmin); err != nil {
		return ImportedProject{}, err
	}

	members, err := im.memberships(ctx, project, p)
	if err != nil {
		return ImportedProject{}, err
	}

	member := func(name string) int64 { return members[im.mustPrincipal(name).ID] }

	issues := make([]db.ImportIssuesParams, 0, len(p.Issues))
	next := p.NextNumber
	comments := 0

	for _, i := range p.Issues {
		status, _ := im.catalog.Status(i.Status)
		issueType, _ := im.catalog.IssueType(i.Type)

		row := db.ImportIssuesParams{
			ProjectID:   project.ID,
			Number:      i.Number,
			Summary:     i.Summary,
			Description: i.Description,
			StatusID:    status.ID,
			TypeID:      issueType.ID,
			ReporterID:  member(i.Reporter),
			Labels:      CleanLabels(i.Labels),
			CreatedAt:   i.Created,
			UpdatedAt:   i.Updated,
		}

		if i.Priority != "" {
			priority, _ := im.catalog.Priority(i.Priority)
			row.PriorityID = &priority.ID
		}

		// Agents can't be assignees, so an agent's issues go to its owner.
		if i.Assignee != "" {
			assignee := im.mustPrincipal(i.Assignee)

			user := assignee.ID
			if assignee.OwnerID != nil {
				user = *assignee.OwnerID
			}

			row.AssigneeID = new(members[user])
		}

		issues = append(issues, row)
		next = max(next, i.Number+1)
		comments += len(i.Comments)
	}

	if _, err := im.q.ImportIssues(ctx, issues); err != nil {
		return ImportedProject{}, fmt.Errorf("copying issues: %w", err)
	}

	if err := im.q.RaiseNextNumber(ctx, db.RaiseNextNumberParams{ID: project.ID, NextNumber: next}); err != nil {
		return ImportedProject{}, err
	}

	ids, err := im.q.IssueNumbers(ctx, project.ID)
	if err != nil {
		return ImportedProject{}, err
	}

	byNumber := make(map[int32]int64, len(ids))
	for _, row := range ids {
		byNumber[row.Number] = row.ID
	}

	rows := make([]db.ImportCommentsParams, 0, comments)
	history := make([]db.InsertHistoryParams, 0, len(p.Issues))
	ownerMembership := members[im.owner.ID]

	for _, i := range p.Issues {
		history = append(history, db.InsertHistoryParams{
			ProjectID: project.ID, IssueID: byNumber[i.Number], ActorID: ownerMembership, Field: "imported",
		})

		for _, c := range i.Comments {
			rows = append(rows, db.ImportCommentsParams{
				ProjectID: project.ID,
				IssueID:   byNumber[i.Number],
				AuthorID:  member(c.Author),
				Body:      c.Body,
				CreatedAt: c.Created,
				UpdatedAt: c.Updated,
			})
		}
	}

	if _, err := im.q.ImportComments(ctx, rows); err != nil {
		return ImportedProject{}, fmt.Errorf("copying comments: %w", err)
	}

	if _, err := im.q.InsertHistory(ctx, history); err != nil {
		return ImportedProject{}, err
	}

	return ImportedProject{Key: key, Issues: len(issues), Comments: comments}, nil
}

// memberships makes everyone the project's issues and comments name a member,
// and returns each principal's membership. A user joins as a member; an agent
// joins with its owner.
func (im *importer) memberships(ctx context.Context, project db.Project, p ImportProject) (map[int64]int64, error) {
	members := map[int64]int64{}

	join := func(name string) error {
		principal := im.mustPrincipal(name)
		if _, ok := members[principal.ID]; ok {
			return nil
		}

		user := principal.ID
		if principal.OwnerID != nil {
			user = *principal.OwnerID
		}

		if user != im.owner.ID {
			if _, err := addMember(ctx, im.q, project, user, RoleMember); err != nil {
				return err
			}
		}

		// An agent's owner is recorded too, since its issues are assigned to the
		// owner.
		for _, id := range []int64{principal.ID, user} {
			m, err := im.q.GetMembership(ctx, db.GetMembershipParams{ProjectID: project.ID, PrincipalID: id})
			if err != nil {
				return fmt.Errorf("%s has no membership in the project: %w", principal.Username, err)
			}

			members[id] = m.ID
		}

		return nil
	}

	if err := join(""); err != nil {
		return nil, err
	}

	for _, i := range p.Issues {
		names := []string{i.Reporter, i.Assignee}
		for _, c := range i.Comments {
			names = append(names, c.Author)
		}

		for _, name := range names {
			if name == "" {
				continue
			}

			if err := join(name); err != nil {
				return nil, err
			}
		}
	}

	return members, nil
}

// mustPrincipal returns a principal resolveAll already found. An empty name
// is the owner.
func (im *importer) mustPrincipal(name string) db.Principal {
	if name == "" {
		return im.owner
	}

	return im.principals[name]
}

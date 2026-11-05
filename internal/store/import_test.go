package store_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/leightweight/orca-tasks/internal/store"
	"github.com/leightweight/orca-tasks/internal/store/db"
	"github.com/leightweight/orca-tasks/internal/testutil"
)

func importFile() store.ImportFile {
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	return store.ImportFile{
		Format:  store.ImportFormat,
		Version: store.ImportVersion,
		Projects: []store.ImportProject{{
			Key:        "ops",
			Name:       "Operations",
			NextNumber: 5,
			Issues: []store.ImportIssue{
				{Number: 1, Summary: "First", Status: "Done", Type: "Task", Reporter: "jleight", Created: created, Updated: created},
				{
					Number: 3, Summary: "Third", Status: "in progress", Type: "Bug", Priority: "High",
					Reporter: "claude", Assignee: "bob", Labels: []string{"a"}, Created: created, Updated: created.Add(time.Hour),
					Comments: []store.ImportComment{
						{Author: "claude", Body: "Started.", Created: created, Updated: created},
						{Author: "", Body: "No author.", Created: created, Updated: created},
					},
				},
			},
		}},
	}
}

func TestImport(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	// newFixture already gave alice an OPS, so import as bob, who's only a
	// member there; the agent of alice and alice herself appear in the file.
	options := store.ImportOptions{Owner: "bob", Users: map[string]string{"claude": "alice/claude", "jleight": "alice"}}

	file := importFile()
	file.Projects[0].Issues[1].Assignee = "bob"

	if _, err := f.s.Import(testutil.Context(t), f.catalog, file, options); err == nil || !strings.Contains(err.Error(), "already uses the key OPS") {
		t.Fatalf("importing over bob's OPS: %v", err)
	}

	file.Projects[0].Key = "OLD"

	dryRun := store.ImportOptions{Owner: "bob", DryRun: true, Users: options.Users}
	if _, err := f.s.Import(testutil.Context(t), f.catalog, file, dryRun); !errors.Is(err, store.ErrDryRun) {
		t.Fatalf("dry run: %v", err)
	}

	if got := f.visible(t, f.bob, nil); len(got) != 0 {
		t.Fatalf("the dry run saved issues: %v", got)
	}

	results, err := f.s.Import(testutil.Context(t), f.catalog, file, options)
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 1 || results[0].Key != "OLD" || results[0].Issues != 2 || results[0].Comments != 2 {
		t.Errorf("results = %+v", results)
	}

	rows, err := f.s.VisibleIssues(testutil.Context(t), db.VisibleIssuesParams{PrincipalID: f.bob.ID, Key: new("OLD"), Number: new(int32(3))})
	if err != nil || len(rows) != 1 {
		t.Fatalf("OLD-3: %+v, %v", rows, err)
	}

	third := rows[0]
	if third.ReporterUsername != "alice/claude" || third.AssigneeUsername == nil || *third.AssigneeUsername != "bob" ||
		!third.Issue.UpdatedAt.Equal(time.Date(2026, 9, 1, 13, 0, 0, 0, time.UTC)) {
		t.Errorf("OLD-3 = reporter %s, assignee %v, updated %s", third.ReporterUsername, third.AssigneeUsername, third.Issue.UpdatedAt)
	}

	comments, err := f.s.ListComments(testutil.Context(t), third.Issue.ID)
	if err != nil || len(comments) != 2 || comments[0].AuthorUsername != "alice/claude" || comments[1].AuthorUsername != "bob" {
		t.Errorf("comments = %+v, %v", comments, err)
	}

	// The reporter's and author's owner, alice, joined as a member.
	if got := f.visible(t, f.alice, nil); len(got) != 2 {
		t.Errorf("alice sees %v, want the two imported issues", got)
	}

	project, err := f.s.GetProject(testutil.Context(t), third.Issue.ProjectID)
	if err != nil || project.NextNumber != 5 {
		t.Errorf("next number = %d, %v; want 5", project.NextNumber, err)
	}
}

func TestImportReportsEveryUnknownName(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	_, err := f.s.Import(testutil.Context(t), f.catalog, importFile(), store.ImportOptions{Owner: "bob"})
	if err == nil || !strings.Contains(err.Error(), `"jleight"`) || !strings.Contains(err.Error(), `"claude"`) {
		t.Errorf("import with unmapped names: %v", err)
	}

	if got := f.visible(t, f.bob, nil); len(got) != 0 {
		t.Errorf("a failed import saved issues: %v", got)
	}
}

func TestImportFindsTheOwnersAgents(t *testing.T) {
	t.Parallel()

	f := newFixture(t)

	// alice owns the import, so "claude" is her agent without a mapping, and
	// an issue assigned to it goes to her.
	file := importFile()
	file.Projects[0].Key = "OLD"
	file.Projects[0].Issues[1].Assignee = "claude"

	if _, err := f.s.Import(testutil.Context(t), f.catalog, file, store.ImportOptions{Owner: "alice", Users: map[string]string{"jleight": "alice"}}); err != nil {
		t.Fatal(err)
	}

	rows, err := f.s.VisibleIssues(testutil.Context(t), db.VisibleIssuesParams{PrincipalID: f.alice.ID, Key: new("OLD"), Number: new(int32(3))})
	if err != nil || len(rows) != 1 {
		t.Fatalf("OLD-3: %+v, %v", rows, err)
	}

	if rows[0].ReporterUsername != "alice/claude" || rows[0].AssigneeUsername == nil || *rows[0].AssigneeUsername != "alice" {
		t.Errorf("OLD-3 = reporter %s, assignee %v; want alice/claude and alice", rows[0].ReporterUsername, rows[0].AssigneeUsername)
	}
}

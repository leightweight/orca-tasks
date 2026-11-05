package store_test

import (
	"testing"

	"github.com/leightweight/orca-tasks/internal/store"
	"github.com/leightweight/orca-tasks/internal/store/db"
	"github.com/leightweight/orca-tasks/internal/testutil"
)

type fixture struct {
	s       *store.Store
	catalog *store.Catalog
	alice   db.Principal
	bob     db.Principal
	agent   db.Principal
	ops     db.Project
	other   db.Project
}

// newFixture has alice administering OPS, which bob joins, and OTHER, which
// bob can't see. Alice has an agent.
func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{s: testutil.Store(t)}
	f.alice = createUser(t, f.s, "alice")
	f.bob = createUser(t, f.s, "bob")

	var err error
	if f.agent, err = f.s.CreateAgent(testutil.Context(t), f.alice.ID, "claude", "Claude"); err != nil {
		t.Fatal(err)
	}

	if f.ops, err = f.s.CreateProject(testutil.Context(t), f.alice.ID, "OPS", "Operations"); err != nil {
		t.Fatal(err)
	}

	if f.other, err = f.s.CreateProject(testutil.Context(t), f.alice.ID, "OTHER", ""); err != nil {
		t.Fatal(err)
	}

	if _, err := f.s.AddMember(testutil.Context(t), f.ops.ID, f.bob.ID, store.RoleMember); err != nil {
		t.Fatal(err)
	}

	if f.catalog, err = f.s.LoadCatalog(testutil.Context(t)); err != nil {
		t.Fatal(err)
	}

	return f
}

func (f *fixture) membership(t *testing.T, project db.Project, principal db.Principal) int64 {
	t.Helper()

	m, err := f.s.GetMembership(testutil.Context(t), db.GetMembershipParams{ProjectID: project.ID, PrincipalID: principal.ID})
	if err != nil {
		t.Fatal(err)
	}

	return m.ID
}

func (f *fixture) createIssue(t *testing.T, project db.Project, reporter db.Principal, summary string) db.Issue {
	t.Helper()

	issue, err := f.s.CreateIssue(testutil.Context(t), store.NewIssue{
		ProjectID: project.ID,
		Summary:   summary,
		StatusID:  f.catalog.Statuses[0].ID,
		TypeID:    f.catalog.IssueTypes[0].ID,
		Reporter:  f.membership(t, project, reporter),
		Labels:    []string{" backend", "backend", ""},
	})
	if err != nil {
		t.Fatal(err)
	}

	return issue
}

func (f *fixture) visible(t *testing.T, principal db.Principal, projects []int64) []string {
	t.Helper()

	rows, err := f.s.VisibleIssues(testutil.Context(t), db.VisibleIssuesParams{PrincipalID: principal.ID, ProjectIds: projects})
	if err != nil {
		t.Fatal(err)
	}

	keys := make([]string, 0, len(rows))
	for _, r := range rows {
		keys = append(keys, r.ProjectKey)
	}

	return keys
}

func TestVisibility(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	first := f.createIssue(t, f.ops, f.agent, "Rotate the keys")
	f.createIssue(t, f.other, f.alice, "Secret")

	if first.Number != 1 || len(first.Labels) != 1 {
		t.Errorf("issue = number %d, labels %q; want 1 and one label", first.Number, first.Labels)
	}

	if got := f.visible(t, f.alice, nil); len(got) != 2 {
		t.Errorf("alice sees %v, want both projects", got)
	}

	if got := f.visible(t, f.bob, nil); len(got) != 1 || got[0] != "OPS" {
		t.Errorf("bob sees %v, want only OPS", got)
	}

	if got := f.visible(t, f.agent, []int64{f.other.ID}); len(got) != 1 || got[0] != "OTHER" {
		t.Errorf("a key limited to OTHER sees %v", got)
	}

	if err := f.s.RemoveMember(testutil.Context(t), f.ops.ID, f.bob.ID); err != nil {
		t.Fatal(err)
	}

	if got := f.visible(t, f.bob, nil); len(got) != 0 {
		t.Errorf("bob sees %v after leaving", got)
	}

	rows, err := f.s.VisibleIssues(testutil.Context(t), db.VisibleIssuesParams{PrincipalID: f.alice.ID, Key: new("ops"), Number: new(int32(1))})
	if err != nil || len(rows) != 1 || rows[0].ReporterUsername != "alice/claude" {
		t.Fatalf("OPS-1 = %+v, %v; want one issue reported by the agent", rows, err)
	}
}

func TestUpdateRecordsHistory(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	issue := f.createIssue(t, f.ops, f.alice, "Rotate the keys")

	rows, err := f.s.VisibleIssues(testutil.Context(t), db.VisibleIssuesParams{PrincipalID: f.alice.ID, IssueID: &issue.ID})
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}

	done, err := f.catalog.Status("done")
	if err != nil {
		t.Fatal(err)
	}

	err = f.s.UpdateIssue(testutil.Context(t), f.catalog, f.membership(t, f.ops, f.bob), rows[0].Issue.ID, store.IssueChange{
		Summary:     new("Rotate the keys"),
		StatusID:    &done.ID,
		SetAssignee: true,
		Assignee:    &store.Person{MembershipID: f.membership(t, f.ops, f.bob), Username: "bob"},
		Comment:     "Done.",
	})
	if err != nil {
		t.Fatal(err)
	}

	history, err := f.s.ListHistory(testutil.Context(t), issue.ID)
	if err != nil {
		t.Fatal(err)
	}

	fields := make([]string, 0, len(history))
	for _, h := range history {
		fields = append(fields, h.Field)
	}

	// The unchanged summary isn't recorded.
	if want := []string{"created", "status", "assignee", "comment"}; len(fields) != len(want) {
		t.Errorf("history = %v, want %v", fields, want)
	}
}

func TestDeletingAProjectDeletesItsIssues(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	issue := f.createIssue(t, f.ops, f.bob, "Rotate the keys")

	if _, err := f.s.AddComment(testutil.Context(t), f.membership(t, f.ops, f.bob), issue, "On it."); err != nil {
		t.Fatal(err)
	}

	if err := f.s.DeleteProject(testutil.Context(t), f.ops.ID); err != nil {
		t.Fatal(err)
	}

	if got := f.visible(t, f.alice, nil); len(got) != 0 {
		t.Errorf("alice still sees %v", got)
	}

	users, err := f.s.ListUsers(testutil.Context(t))
	if err != nil {
		t.Fatal(err)
	}

	for _, u := range users {
		if u.IssuesReported != 0 || u.Comments != 0 {
			t.Errorf("%s still has %d issues and %d comments", u.Principal.Username, u.IssuesReported, u.Comments)
		}
	}
}

func TestUserCounts(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	f.createIssue(t, f.ops, f.agent, "From the agent")
	f.createIssue(t, f.other, f.alice, "From alice")

	users, err := f.s.ListUsers(testutil.Context(t))
	if err != nil {
		t.Fatal(err)
	}

	for _, u := range users {
		if u.Principal.Username == "alice" && (u.IssuesReported != 2 || u.Agents != 1 || u.Projects != 2) {
			t.Errorf("alice = %d issues, %d agents, %d projects; want 2, 1, 2", u.IssuesReported, u.Agents, u.Projects)
		}
	}
}

func TestConcurrentEditsBothLand(t *testing.T) {
	t.Parallel()

	f := newFixture(t)
	issue := f.createIssue(t, f.ops, f.alice, "Rotate the keys")
	actor := f.membership(t, f.ops, f.alice)

	done, err := f.catalog.Status("done")
	if err != nil {
		t.Fatal(err)
	}

	changes := []store.IssueChange{
		{Summary: new("Rotate every key")},
		{StatusID: &done.ID},
		{AddLabels: []string{"security"}},
		{AddLabels: []string{"urgent"}},
	}

	errs := make(chan error, len(changes))

	for _, change := range changes {
		go func() { errs <- f.s.UpdateIssue(testutil.Context(t), f.catalog, actor, issue.ID, change) }()
	}

	for range changes {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}

	rows, err := f.s.VisibleIssues(testutil.Context(t), db.VisibleIssuesParams{PrincipalID: f.alice.ID, IssueID: &issue.ID})
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}

	got := rows[0].Issue
	if got.Summary != "Rotate every key" || got.StatusID != done.ID || len(got.Labels) != 3 {
		t.Errorf("issue = %q, status %d, labels %q; want every edit applied", got.Summary, got.StatusID, got.Labels)
	}
}

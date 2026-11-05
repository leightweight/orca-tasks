package store_test

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/leightweight/orca-tasks/internal/store"
	"github.com/leightweight/orca-tasks/internal/store/db"
	"github.com/leightweight/orca-tasks/internal/testutil"
)

func TestProjectKeysAreUniquePerUser(t *testing.T) {
	t.Parallel()

	s := testutil.Store(t)
	alice := createUser(t, s, "alice")
	bob := createUser(t, s, "bob")

	agent, err := s.CreateAgent(testutil.Context(t), alice.ID, "alice-claude", "Claude")
	if err != nil {
		t.Fatal(err)
	}

	ours, err := s.CreateProject(testutil.Context(t), alice.ID, "ops", "Alice's ops")
	if err != nil {
		t.Fatal(err)
	}

	theirs, err := s.CreateProject(testutil.Context(t), bob.ID, "OPS", "Bob's ops")
	if err != nil {
		t.Fatal(err)
	}

	key, err := s.AddMember(testutil.Context(t), theirs.ID, alice.ID, store.RoleMember)
	if err != nil || key != "OPS2" {
		t.Fatalf("joining a second OPS gave key %q, %v; want OPS2", key, err)
	}

	// The agent sees both under its owner's keys.
	for key, want := range map[string]int64{"OPS": ours.ID, "OPS2": theirs.ID} {
		row, err := s.GetMembershipByKey(testutil.Context(t), db.GetMembershipByKeyParams{PrincipalID: agent.ID, Key: key})
		if err != nil || row.Project.ID != want {
			t.Errorf("agent's %s = project %d, %v; want %d", key, row.Project.ID, err, want)
		}
	}

	if err := s.SetMemberKey(testutil.Context(t), theirs.ID, alice.ID, "ops"); !errors.Is(err, store.ErrKeyTaken) {
		t.Errorf("renaming to a key in use: %v, want ErrKeyTaken", err)
	}

	if err := s.SetMemberKey(testutil.Context(t), theirs.ID, alice.ID, "BOBOPS"); err != nil {
		t.Fatal(err)
	}

	row, err := s.GetMembershipByKey(testutil.Context(t), db.GetMembershipByKeyParams{PrincipalID: agent.ID, Key: "bobops"})
	if err != nil || row.Project.ID != theirs.ID {
		t.Errorf("the agent didn't follow the rename: %+v, %v", row, err)
	}
}

func TestRemovingAMemberKeepsTheirMembership(t *testing.T) {
	t.Parallel()

	s := testutil.Store(t)
	alice := createUser(t, s, "alice")
	bob := createUser(t, s, "bob")

	agent, err := s.CreateAgent(testutil.Context(t), bob.ID, "bob-claude", "")
	if err != nil {
		t.Fatal(err)
	}

	project, err := s.CreateProject(testutil.Context(t), alice.ID, "OPS", "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.AddMember(testutil.Context(t), project.ID, bob.ID, store.RoleMember); err != nil {
		t.Fatal(err)
	}

	before, err := s.GetMembership(testutil.Context(t), db.GetMembershipParams{ProjectID: project.ID, PrincipalID: bob.ID})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.RemoveMember(testutil.Context(t), project.ID, bob.ID); err != nil {
		t.Fatal(err)
	}

	for _, id := range []int64{bob.ID, agent.ID} {
		_, err := s.GetMembership(testutil.Context(t), db.GetMembershipParams{ProjectID: project.ID, PrincipalID: id})
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("principal %d is still an active member: %v", id, err)
		}
	}

	if _, err := s.AddMember(testutil.Context(t), project.ID, bob.ID, store.RoleMember); err != nil {
		t.Fatal(err)
	}

	after, err := s.GetMembership(testutil.Context(t), db.GetMembershipParams{ProjectID: project.ID, PrincipalID: bob.ID})
	if err != nil || after.ID != before.ID {
		t.Errorf("re-adding gave membership %d, %v; want the old one, %d", after.ID, err, before.ID)
	}

	if _, err := s.GetMembership(testutil.Context(t), db.GetMembershipParams{ProjectID: project.ID, PrincipalID: agent.ID}); err != nil {
		t.Errorf("the agent wasn't restored: %v", err)
	}
}

func TestValidation(t *testing.T) {
	t.Parallel()

	s := testutil.Store(t)
	alice := createUser(t, s, "alice")

	if _, err := s.CreateProject(testutil.Context(t), alice.ID, "1OPS", ""); !errors.Is(err, store.ErrInvalidKey) {
		t.Errorf("key starting with a digit: %v", err)
	}

	if _, err := s.CreateAgent(testutil.Context(t), alice.ID, "Alice-Bot", ""); err != nil {
		t.Errorf("a username in capitals should be lowercased, got %v", err)
	}

	if _, err := s.CreateAgent(testutil.Context(t), alice.ID, "alice", ""); !errors.Is(err, store.ErrUsernameTaken) {
		t.Errorf("duplicate username: %v", err)
	}
}

package store

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/leightweight/orca-tasks/internal/store/db"
)

// Membership roles.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

var (
	projectKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,19}$`)
	usernamePattern   = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)

	// ErrInvalidKey is returned for a project key that isn't uppercase letters,
	// digits, and underscores starting with a letter.
	ErrInvalidKey = errors.New("a project key starts with a letter and has only uppercase letters, digits, and underscores, up to 20 characters")

	// ErrKeyTaken is returned when a user already sees another project under a key.
	ErrKeyTaken = errors.New("you already use that key for another project")

	// ErrInvalidUsername is returned for a username that doesn't fit the pattern.
	ErrInvalidUsername = errors.New("a username starts with a lowercase letter and has only lowercase letters, digits, '.', '_', and '-', up to 64 characters")

	// ErrUsernameTaken is returned when a user already has a username, or an
	// owner already has an agent with a name.
	ErrUsernameTaken = errors.New("that name is taken")
)

// NormalizeKey uppercases a project key and checks its shape.
func NormalizeKey(key string) (string, error) {
	key = strings.ToUpper(strings.TrimSpace(key))
	if !projectKeyPattern.MatchString(key) {
		return "", ErrInvalidKey
	}

	return key, nil
}

// NormalizeUsername lowercases a username and checks its shape.
func NormalizeUsername(username string) (string, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if !usernamePattern.MatchString(username) {
		return "", ErrInvalidUsername
	}

	return username, nil
}

// freeKey is want if it's not taken, or want with the lowest number appended
// that isn't.
func freeKey(taken []string, want string) string {
	key := want
	for n := 2; slices.Contains(taken, key); n++ {
		key = want + strconv.Itoa(n)
	}

	return key
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// CreateProject creates a project with a user as its administrator.
func (s *Store) CreateProject(ctx context.Context, userID int64, key, name string) (db.Project, error) {
	key, err := NormalizeKey(key)
	if err != nil {
		return db.Project{}, err
	}

	name = strings.TrimSpace(name)
	if name == "" {
		name = key
	}

	var project db.Project

	err = s.InTx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		if project, err = q.InsertProject(ctx, db.InsertProjectParams{Key: key, Name: name}); err != nil {
			return err
		}

		_, err := addMember(ctx, q, project, userID, RoleAdmin)

		return err
	})

	return project, err
}

// AddMember adds a user to a project, or changes their role if they're already
// a member. It returns the key the user sees the project under.
func (s *Store) AddMember(ctx context.Context, projectID, userID int64, role string) (string, error) {
	var key string

	err := s.InTx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		project, err := q.GetProject(ctx, projectID)
		if err != nil {
			return err
		}

		key, err = addMember(ctx, q, project, userID, role)

		return err
	})

	return key, err
}

func addMember(ctx context.Context, q *db.Queries, project db.Project, userID int64, role string) (string, error) {
	if role != RoleAdmin && role != RoleMember {
		return "", fmt.Errorf("unknown role %q", role)
	}

	existing, err := q.GetMembership(ctx, db.GetMembershipParams{ProjectID: project.ID, PrincipalID: userID})
	if err == nil {
		_, err = q.SetMembershipRole(ctx, db.SetMembershipRoleParams{ProjectID: project.ID, UserID: userID, Role: role})
		return existing.Key, err
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}

	taken, err := q.ActiveKeys(ctx, userID)
	if err != nil {
		return "", err
	}

	membership, err := q.UpsertMembership(ctx, db.UpsertMembershipParams{
		ProjectID:   project.ID,
		PrincipalID: userID,
		Role:        role,
		Key:         freeKey(taken, project.Key),
	})
	if err != nil {
		return "", err
	}

	return membership.Key, q.SyncAgentMemberships(ctx, db.SyncAgentMembershipsParams{UserID: userID, ProjectID: &project.ID})
}

// RemoveMember removes a user and their agents from a project. Everything that
// references their memberships stays.
func (s *Store) RemoveMember(ctx context.Context, projectID, userID int64) error {
	return s.InTx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		if _, err := q.RemoveMembership(ctx, db.RemoveMembershipParams{ProjectID: projectID, UserID: userID}); err != nil {
			return err
		}

		return q.SyncAgentMemberships(ctx, db.SyncAgentMembershipsParams{UserID: userID, ProjectID: &projectID})
	})
}

// SetMemberKey changes the key a user and their agents see a project under.
func (s *Store) SetMemberKey(ctx context.Context, projectID, userID int64, key string) error {
	key, err := NormalizeKey(key)
	if err != nil {
		return err
	}

	err = s.InTx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		n, err := q.SetMembershipKey(ctx, db.SetMembershipKeyParams{ProjectID: projectID, UserID: userID, Key: key})
		if err != nil {
			return err
		}

		if n == 0 {
			return pgx.ErrNoRows
		}

		return q.SyncAgentMemberships(ctx, db.SyncAgentMembershipsParams{UserID: userID, ProjectID: &projectID})
	})
	if isUniqueViolation(err) {
		return ErrKeyTaken
	}

	return err
}

// AgentSeparator joins an agent's owner and name into its username, as in
// jleight/claude. Usernames can't contain it, so an agent's username never
// collides with a user's, and each owner's agent names are their own.
const AgentSeparator = "/"

// AgentName is the name an agent's owner gave it: its username without the
// owner.
func AgentName(agent db.Principal) string {
	_, name, _ := strings.Cut(agent.Username, AgentSeparator)
	return name
}

// CreateAgent creates an agent with memberships in its owner's projects. Its
// username is the owner's and the name together, so the name only has to be
// unique among the owner's agents.
func (s *Store) CreateAgent(ctx context.Context, ownerID int64, name, displayName string) (db.Principal, error) {
	name, err := NormalizeUsername(name)
	if err != nil {
		return db.Principal{}, err
	}

	var agent db.Principal

	err = s.InTx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		owner, err := q.GetPrincipal(ctx, ownerID)
		if err != nil {
			return err
		}

		agent, err = q.CreateAgent(ctx, db.CreateAgentParams{
			Username:    owner.Username + AgentSeparator + name,
			DisplayName: cmpOr(strings.TrimSpace(displayName), name),
			OwnerID:     &ownerID,
		})
		if err != nil {
			return err
		}

		return q.SyncAgentMemberships(ctx, db.SyncAgentMembershipsParams{UserID: ownerID})
	})
	if isUniqueViolation(err) {
		return db.Principal{}, ErrUsernameTaken
	}

	return agent, err
}

func cmpOr(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}

	return ""
}

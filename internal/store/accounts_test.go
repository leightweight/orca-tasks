package store_test

import (
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/leightweight/orca-tasks/internal/auth"
	"github.com/leightweight/orca-tasks/internal/store"
	"github.com/leightweight/orca-tasks/internal/store/db"
	"github.com/leightweight/orca-tasks/internal/testutil"
)

func createUser(t *testing.T, s *store.Store, username string) db.Principal {
	t.Helper()

	hash := "unused"
	user, err := s.CreateUser(testutil.Context(t), db.CreateUserParams{
		Username:     username,
		DisplayName:  username,
		PasswordHash: &hash,
		TotpSecret:   []byte("sealed"),
	})
	if err != nil {
		t.Fatal(err)
	}

	return user
}

func createKey(t *testing.T, s *store.Store, principal int64, params db.CreateAPIKeyParams) []byte {
	t.Helper()

	_, hash := auth.NewAPIKey()
	params.PrincipalID = principal
	params.KeyHash = hash
	params.AllProjects = true

	if _, err := s.CreateAPIKey(testutil.Context(t), params); err != nil {
		t.Fatal(err)
	}

	return hash
}

func TestAuthenticateAPIKey(t *testing.T) {
	t.Parallel()

	s := testutil.Store(t)
	user := createUser(t, s, "jleight")

	agent, err := s.CreateAgent(testutil.Context(t), user.ID, "claude", "Claude")
	if err != nil {
		t.Fatal(err)
	}

	live := createKey(t, s, user.ID, db.CreateAPIKeyParams{Name: "live"})
	agentKey := createKey(t, s, agent.ID, db.CreateAPIKeyParams{Name: "agent"})
	past := time.Now().Add(-time.Hour)
	expired := createKey(t, s, user.ID, db.CreateAPIKeyParams{Name: "expired", ExpiresAt: &past})

	row, err := s.AuthenticateAPIKey(testutil.Context(t), live)
	if err != nil || row.PrincipalID != user.ID {
		t.Fatalf("live key: %+v, %v", row, err)
	}

	if row, err := s.AuthenticateAPIKey(testutil.Context(t), agentKey); err != nil || row.Username != "jleight/claude" || row.UserUsername != "jleight" {
		t.Fatalf("agent key: %+v, %v", row, err)
	}

	if _, err := s.AuthenticateAPIKey(testutil.Context(t), expired); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("expired key: %v, want no rows", err)
	}

	keys, err := s.ListAPIKeys(testutil.Context(t), user.ID)
	if err != nil || len(keys) != 3 {
		t.Fatalf("ListAPIKeys = %d keys, %v", len(keys), err)
	}

	for _, k := range keys {
		if k.ApiKey.Name == "live" {
			if n, err := s.RevokeAPIKey(testutil.Context(t), db.RevokeAPIKeyParams{ID: k.ApiKey.ID, OwnerID: agent.ID}); err != nil || n != 0 {
				t.Errorf("an agent revoked its owner's key: %d, %v", n, err)
			}

			if n, err := s.RevokeAPIKey(testutil.Context(t), db.RevokeAPIKeyParams{ID: k.ApiKey.ID, OwnerID: user.ID}); err != nil || n != 1 {
				t.Errorf("revoking: %d, %v", n, err)
			}
		}
	}

	if _, err := s.AuthenticateAPIKey(testutil.Context(t), live); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("revoked key: %v, want no rows", err)
	}

	// Disabling the owner stops the agent's keys too.
	if err := s.SetDisabled(testutil.Context(t), db.SetDisabledParams{ID: user.ID, Disabled: true}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.AuthenticateAPIKey(testutil.Context(t), agentKey); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("agent key of a disabled owner: %v, want no rows", err)
	}
}

func TestTOTPStepsAndRecoveryCodesAreSingleUse(t *testing.T) {
	t.Parallel()

	s := testutil.Store(t)
	user := createUser(t, s, "jleight")

	if n, _ := s.UseTOTPStep(testutil.Context(t), db.UseTOTPStepParams{ID: user.ID, Step: 100}); n != 1 {
		t.Error("the first use of a step was refused")
	}

	if n, _ := s.UseTOTPStep(testutil.Context(t), db.UseTOTPStepParams{ID: user.ID, Step: 100}); n != 0 {
		t.Error("a step was used twice")
	}

	codes, hashes := auth.NewRecoveryCodes()
	if err := s.InsertRecoveryCodes(testutil.Context(t), db.InsertRecoveryCodesParams{PrincipalID: user.ID, CodeHashes: hashes}); err != nil {
		t.Fatal(err)
	}

	use := db.UseRecoveryCodeParams{PrincipalID: user.ID, CodeHash: auth.HashRecoveryCode(codes[3])}
	if n, _ := s.UseRecoveryCode(testutil.Context(t), use); n != 1 {
		t.Error("a recovery code was refused")
	}

	if n, _ := s.UseRecoveryCode(testutil.Context(t), use); n != 0 {
		t.Error("a recovery code was used twice")
	}

	if n, _ := s.CountRecoveryCodes(testutil.Context(t), user.ID); n != auth.RecoveryCodeCount-1 {
		t.Errorf("%d codes left, want %d", n, auth.RecoveryCodeCount-1)
	}
}

func TestResetLinks(t *testing.T) {
	t.Parallel()

	s := testutil.Store(t)
	user := createUser(t, s, "jleight")

	_, live := auth.NewToken("")
	_, stale := auth.NewToken("")

	for hash, expires := range map[*[]byte]time.Time{&live: time.Now().Add(time.Hour), &stale: time.Now().Add(-time.Hour)} {
		err := s.CreateResetLink(testutil.Context(t), db.CreateResetLinkParams{TokenHash: *hash, PrincipalID: user.ID, ExpiresAt: expires})
		if err != nil {
			t.Fatal(err)
		}
	}

	if _, err := s.GetResetLink(testutil.Context(t), stale); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("expired link: %v, want no rows", err)
	}

	if row, err := s.GetResetLink(testutil.Context(t), live); err != nil || row.Principal.ID != user.ID {
		t.Fatalf("live link: %+v, %v", row, err)
	}

	if err := s.UseResetLinks(testutil.Context(t), user.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := s.GetResetLink(testutil.Context(t), live); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("used link: %v, want no rows", err)
	}
}

func TestPasswordOnlyUsersCanUseKeys(t *testing.T) {
	t.Parallel()

	s := testutil.Store(t)
	hash := "unused"

	passwordOnly, err := s.CreateUser(testutil.Context(t), db.CreateUserParams{Username: "local", DisplayName: "Local", PasswordHash: &hash})
	if err != nil {
		t.Fatal(err)
	}

	invited, err := s.CreateUser(testutil.Context(t), db.CreateUserParams{Username: "invited", DisplayName: "Invited"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.AuthenticateAPIKey(testutil.Context(t), createKey(t, s, passwordOnly.ID, db.CreateAPIKeyParams{Name: "a"})); err != nil {
		t.Errorf("a key of a user with only a password: %v", err)
	}

	if _, err := s.AuthenticateAPIKey(testutil.Context(t), createKey(t, s, invited.ID, db.CreateAPIKeyParams{Name: "b"})); !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("a key of a user who hasn't set a password: %v, want no rows", err)
	}
}

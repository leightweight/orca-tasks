package web

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/leightweight/orca-tasks/internal/auth"
	"github.com/leightweight/orca-tasks/internal/store"
	"github.com/leightweight/orca-tasks/internal/store/db"
)

// TOTPIssuer names the account in authenticator apps.
const TOTPIssuer = "orca-tasks"

// pendingLifetime is how long a correct password waits for its TOTP code.
const pendingLifetime = 10 * time.Minute

// Enrollment purposes.
const (
	purposeSetup = "setup"
	purposeReset = "reset"
)

// enrollment is a password and TOTP secret waiting for the user to confirm a
// code. It lives in the server-side session, so the secret never reaches a
// cookie.
type enrollment struct {
	Purpose      string
	PrincipalID  int64
	TokenHash    []byte
	Username     string
	DisplayName  string
	PasswordHash string
	Secret       string
	URL          string
}

type credentialsForm struct {
	Username    string
	DisplayName string
}

// readPassword checks that the two password fields match and hashes them.
func readPassword(r *http.Request) (string, error) {
	password := r.PostFormValue("password")
	if password != r.PostFormValue("confirm") {
		return "", errors.New("the passwords don't match")
	}

	return auth.HashPassword(password)
}

func (w *Web) setupForm(rw http.ResponseWriter, r *http.Request) {
	hasUsers, err := w.Store.HasUsers(r.Context())
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	if hasUsers {
		http.Redirect(rw, r, "/login", http.StatusSeeOther)
		return
	}

	w.render(rw, r, http.StatusOK, "setup", "Set up orca-tasks", credentialsForm{})
}

func (w *Web) setup(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	form := credentialsForm{Username: r.PostFormValue("username"), DisplayName: r.PostFormValue("display_name")}
	ip := "ip:" + w.ClientIP(r)

	hasUsers, err := w.Store.HasUsers(ctx)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	if hasUsers {
		http.Redirect(rw, r, "/login", http.StatusSeeOther)
		return
	}

	if !w.Limiter.Attempt(ctx, ip) {
		w.fail(rw, r, "setup", "Set up orca-tasks", errTooManyAttempts.Error(), form)
		return
	}

	code := strings.TrimSpace(r.PostFormValue("code"))
	if w.SetupCode == "" || subtle.ConstantTimeCompare([]byte(code), []byte(w.SetupCode)) != 1 {
		w.fail(rw, r, "setup", "Set up orca-tasks", "That isn't the setup code from the server's log.", form)

		return
	}

	w.Limiter.Succeeded(ctx, ip)

	username, err := store.NormalizeUsername(form.Username)
	if err != nil {
		w.fail(rw, r, "setup", "Set up orca-tasks", err.Error(), form)
		return
	}

	hash, err := readPassword(r)
	if err != nil {
		w.fail(rw, r, "setup", "Set up orca-tasks", err.Error(), form)
		return
	}

	w.startEnrollment(rw, r, enrollment{
		Purpose:      purposeSetup,
		Username:     username,
		DisplayName:  cmpOr(strings.TrimSpace(form.DisplayName), username),
		PasswordHash: hash,
	})
}

func (w *Web) startEnrollment(rw http.ResponseWriter, r *http.Request, e enrollment) {
	// A password-only server saves the password now; there's no authenticator
	// to set up.
	if w.PasswordOnly {
		w.saveCredentials(rw, r, e, nil)
		return
	}

	key, err := auth.NewEnrollment(TOTPIssuer, e.Username)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	e.Secret, e.URL = key.Secret, key.URL
	w.putSealed(r.Context(), sessionEnroll, e)

	http.Redirect(rw, r, "/enroll", http.StatusSeeOther)
}

// enrollQR is what the enrollment page shows. The QR code is a data URL, which
// html/template only puts in an img src when it's typed as a URL.
type enrollQR struct {
	Secret string
	QRCode template.URL
}

func enrollPage(e *auth.Enrollment) enrollQR {
	return enrollQR{Secret: e.Secret, QRCode: template.URL(e.QRCode)}
}

func (w *Web) enrollForm(rw http.ResponseWriter, r *http.Request) {
	var e enrollment
	if !w.getSealed(r.Context(), sessionEnroll, &e) {
		http.Redirect(rw, r, "/", http.StatusSeeOther)
		return
	}

	key, err := auth.EnrollmentFor(e.Secret, e.URL)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.render(rw, r, http.StatusOK, "enroll", "Set up your authenticator", enrollPage(key))
}

// enroll confirms a code from the new authenticator, then saves the password
// and secret, makes recovery codes, and signs the user in.
func (w *Web) enroll(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var e enrollment
	if !w.getSealed(ctx, sessionEnroll, &e) {
		http.Redirect(rw, r, "/", http.StatusSeeOther)
		return
	}

	key, err := auth.EnrollmentFor(e.Secret, e.URL)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	step, valid := auth.CheckTOTP(e.Secret, r.PostFormValue("code"), w.Now(), 0)
	if !valid {
		w.fail(rw, r, "enroll", "Set up your authenticator", "That code doesn't match. Check the time on your phone and try the next code.", enrollPage(key))
		return
	}

	w.saveCredentials(rw, r, e, &factor{sealed: w.Box.Seal([]byte(e.Secret)), step: step, codes: auth.NewRecoveryCodes()})
}

// factor is a confirmed authenticator to save along with a password.
type factor struct {
	sealed []byte
	step   int64
	codes  []string
}

// saveCredentials finishes setup or a reset link: it saves the password, and
// the authenticator and recovery codes when there is one, then signs the user
// in. Without one, the user has only a password, as in local mode.
func (w *Web) saveCredentials(rw http.ResponseWriter, r *http.Request, e enrollment, f *factor) {
	ctx := r.Context()

	var sealed []byte
	if f != nil {
		sealed = f.sealed
	}

	var user db.Principal

	err := w.Store.InTx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		var err error

		switch e.Purpose {
		case purposeSetup:
			if err := q.LockSetup(ctx); err != nil {
				return err
			}

			hasUsers, err := q.HasUsers(ctx)
			if err != nil {
				return err
			}

			if hasUsers {
				return errAlreadySetUp
			}

			user, err = q.CreateUser(ctx, db.CreateUserParams{
				Username:     e.Username,
				DisplayName:  e.DisplayName,
				SiteAdmin:    true,
				PasswordHash: &e.PasswordHash,
				TotpSecret:   sealed,
			})
			if err != nil {
				return err
			}

			if w.LocalAgent {
				_, err := q.CreateAgent(ctx, db.CreateAgentParams{
					Username:    user.Username + store.AgentSeparator + "agent",
					DisplayName: "Agent",
					OwnerID:     &user.ID,
				})
				if err != nil {
					return err
				}
			}
		case purposeReset:
			// The link may have expired, or been used, since the form.
			if _, err := q.GetResetLink(ctx, e.TokenHash); err != nil {
				return err
			}

			if err := q.SetCredentials(ctx, db.SetCredentialsParams{ID: e.PrincipalID, PasswordHash: &e.PasswordHash, TotpSecret: sealed}); err != nil {
				return err
			}

			if err := q.UseResetLinks(ctx, e.PrincipalID); err != nil {
				return err
			}

			if user, err = q.GetPrincipal(ctx, e.PrincipalID); err != nil {
				return err
			}
		}

		if err := q.DeleteRecoveryCodes(ctx, user.ID); err != nil {
			return err
		}

		if f == nil {
			return nil
		}

		if _, err := q.UseTOTPStep(ctx, db.UseTOTPStepParams{ID: user.ID, Step: f.step}); err != nil {
			return err
		}

		return q.InsertRecoveryCodes(ctx, db.InsertRecoveryCodesParams{PrincipalID: user.ID, CodeHashes: auth.HashRecoveryCodes(user.ID, f.codes)})
	})

	switch {
	case errors.Is(err, errAlreadySetUp):
		w.Sessions.Remove(ctx, sessionEnroll)
		w.redirect(rw, r, "/login", "Someone else finished setup first. Sign in with their account, or ask them for one.")

		return
	case errors.Is(err, pgx.ErrNoRows):
		w.Sessions.Remove(ctx, sessionEnroll)
		w.render(rw, r, http.StatusGone, "message", "Link expired", "The reset link expired or was already used. Ask an administrator for a new one.")

		return
	case err != nil:
		w.serverError(rw, r, err)
		return
	}

	if err := w.signIn(ctx, user); err != nil {
		w.serverError(rw, r, err)
		return
	}

	if f == nil {
		w.redirect(rw, r, "/projects", "You're signed in. Create a project to get started.")
		return
	}

	w.putSealed(ctx, sessionReveal, recoveryReveal(f.codes))
	http.Redirect(rw, r, "/account", http.StatusSeeOther)
}

var errAlreadySetUp = errors.New("setup is already done")

func recoveryReveal(codes []string) reveal {
	return reveal{
		Title: "Your recovery codes",
		Note:  "Each code signs you in once if you lose your authenticator. Save them in a password manager now: this is the only time they're shown.",
		Lines: []string{strings.Join(codes, "\n")},
	}
}

func (w *Web) resetLink(r *http.Request) (db.Principal, []byte, error) {
	hash := auth.HashToken(r.PathValue("token"))

	row, err := w.Store.GetResetLink(r.Context(), hash)

	return row.Principal, hash, err
}

func (w *Web) resetForm(rw http.ResponseWriter, r *http.Request) {
	user, _, err := w.resetLink(r)
	if errors.Is(err, pgx.ErrNoRows) {
		w.render(rw, r, http.StatusNotFound, "message", "Link expired", "The link expired or was already used. Ask an administrator for a new one.")
		return
	}

	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.render(rw, r, http.StatusOK, "reset", "Set your password", credentialsForm{Username: user.Username})
}

func (w *Web) reset(rw http.ResponseWriter, r *http.Request) {
	user, tokenHash, err := w.resetLink(r)
	if errors.Is(err, pgx.ErrNoRows) {
		w.render(rw, r, http.StatusNotFound, "message", "Link expired", "The link expired or was already used. Ask an administrator for a new one.")
		return
	}

	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	hash, err := readPassword(r)
	if err != nil {
		w.fail(rw, r, "reset", "Set your password", err.Error(), credentialsForm{Username: user.Username})
		return
	}

	w.startEnrollment(rw, r, enrollment{
		Purpose:      purposeReset,
		PrincipalID:  user.ID,
		TokenHash:    tokenHash,
		Username:     user.Username,
		DisplayName:  user.DisplayName,
		PasswordHash: hash,
	})
}

func (w *Web) loginForm(rw http.ResponseWriter, r *http.Request) {
	if currentUser(r.Context()) != nil {
		http.Redirect(rw, r, "/projects", http.StatusSeeOther)
		return
	}

	w.render(rw, r, http.StatusOK, "login", "Sign in", nil)
}

// dummyHash is checked against when a username doesn't exist, so a failed
// sign-in takes as long whether or not the user does.
var dummyHash, _ = auth.HashPassword("not anyone's password at all")

func (w *Web) login(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	username := strings.ToLower(strings.TrimSpace(r.PostFormValue("username")))
	keys := []string{"ip:" + w.ClientIP(r), "user:" + username}

	// The attempt is counted before the password is checked, so concurrent
	// guesses can't all get past the limit; a correct password gives it back.
	if !w.Limiter.Attempt(ctx, keys...) {
		w.fail(rw, r, "login", "Sign in", errTooManyAttempts.Error(), nil)
		return
	}

	user, err := w.Store.GetPrincipalByUsername(ctx, username)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		w.serverError(rw, r, err)
		return
	}

	hash := dummyHash
	if err == nil && user.PasswordHash != nil {
		hash = *user.PasswordHash
	}

	valid := auth.CheckPassword(r.PostFormValue("password"), hash)

	if err != nil || !valid || user.Kind != "user" || user.DisabledAt != nil || user.PasswordHash == nil || (user.TotpSecret == nil && !w.PasswordOnly) {
		w.fail(rw, r, "login", "Sign in", "The username or password is wrong, or the account is disabled.", nil)
		return
	}

	w.Limiter.Succeeded(ctx, keys...)

	// A password-only server signs the user in here; there's no second step.
	if w.PasswordOnly {
		next := w.Sessions.PopString(ctx, sessionNext)

		if err := w.signIn(ctx, user); err != nil {
			w.serverError(rw, r, err)
			return
		}

		if !isLocalPath(next) {
			next = "/projects"
		}

		http.Redirect(rw, r, next, http.StatusSeeOther)

		return
	}

	w.Sessions.Put(ctx, sessionPending, user.ID)
	w.Sessions.Put(ctx, sessionPendingVersion, int64(user.SessionVersion))
	w.Sessions.Put(ctx, sessionPendingUntil, w.Now().Add(pendingLifetime).Unix())
	http.Redirect(rw, r, "/login/totp", http.StatusSeeOther)
}

func (w *Web) loginTOTPForm(rw http.ResponseWriter, r *http.Request) {
	if w.Sessions.GetInt64(r.Context(), sessionPending) == 0 {
		http.Redirect(rw, r, "/login", http.StatusSeeOther)
		return
	}

	w.render(rw, r, http.StatusOK, "login_totp", "Two-factor authentication", nil)
}

func (w *Web) loginTOTP(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	id := w.Sessions.GetInt64(ctx, sessionPending)
	if id == 0 {
		http.Redirect(rw, r, "/login", http.StatusSeeOther)
		return
	}

	user, err := w.Store.GetPrincipal(ctx, id)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	// The password step expires, and a password change or a disabled account
	// since then sends the user back to it.
	if w.Now().Unix() > w.Sessions.GetInt64(ctx, sessionPendingUntil) || user.DisabledAt != nil ||
		int64(user.SessionVersion) != w.Sessions.GetInt64(ctx, sessionPendingVersion) {
		w.Sessions.Remove(ctx, sessionPending)
		w.redirect(rw, r, "/login", "Sign in again: the password step expired.")

		return
	}

	if err := w.checkSecondFactor(ctx, r, user, true); err != nil {
		if !codeProblem(err) {
			w.serverError(rw, r, err)
			return
		}

		w.fail(rw, r, "login_totp", "Two-factor authentication", err.Error(), nil)
		return
	}

	next := w.Sessions.PopString(ctx, sessionNext)

	if err := w.signIn(ctx, user); err != nil {
		w.serverError(rw, r, err)
		return
	}

	if !isLocalPath(next) {
		next = "/projects"
	}

	http.Redirect(rw, r, next, http.StatusSeeOther)
}

// checkSecondFactor checks the "code" field against the user's authenticator,
// or against their recovery codes when allowed, and uses it up.
func (w *Web) checkSecondFactor(ctx context.Context, r *http.Request, user db.Principal, allowRecovery bool) error {
	// A password-only server has no second factor to check.
	if w.PasswordOnly {
		return nil
	}

	keys := []string{"ip:" + w.ClientIP(r), "user:" + user.Username}

	if !w.Limiter.Attempt(ctx, keys...) {
		return errTooManyAttempts
	}

	passed, err := w.useSecondFactor(ctx, r, user, allowRecovery)
	if err != nil || passed {
		// Only a wrong code counts; a server error gives the attempt back too.
		w.Limiter.Succeeded(ctx, keys...)
		return err
	}

	return errWrongCode
}

// errWrongCode is a TOTP or recovery code that doesn't match.
var errWrongCode = errors.New("that code doesn't match")

// codeProblem reports whether a second factor failed by the person's mistake,
// which they should see, rather than the server's, which goes to the log.
func codeProblem(err error) bool {
	return errors.Is(err, errWrongCode) || errors.Is(err, errTooManyAttempts)
}

// useSecondFactor checks a code and uses it up, reporting whether it passed.
// An error is the server's, not a wrong code.
func (w *Web) useSecondFactor(ctx context.Context, r *http.Request, user db.Principal, allowRecovery bool) (bool, error) {
	code := strings.TrimSpace(r.PostFormValue("code"))

	if allowRecovery && len(strings.ReplaceAll(code, " ", "")) > 6 {
		n, err := w.Store.UseRecoveryCode(ctx, db.UseRecoveryCodeParams{PrincipalID: user.ID, CodeHash: auth.HashRecoveryCode(user.ID, code)})

		return n == 1, err
	}

	secret, err := w.Box.Open(user.TotpSecret)
	if err != nil {
		return false, fmt.Errorf("reading %s's TOTP secret: %w", user.Username, err)
	}

	step, valid := auth.CheckTOTP(string(secret), code, w.Now(), user.TotpLastStep)
	if valid {
		// The update only matches when the step is newer than the last one
		// used, so two requests racing with the same code can't both pass.
		n, err := w.Store.UseTOTPStep(ctx, db.UseTOTPStepParams{ID: user.ID, Step: step})

		return n == 1, err
	}

	return false, nil
}

func (w *Web) logout(rw http.ResponseWriter, r *http.Request) {
	if err := w.Sessions.Destroy(r.Context()); err != nil {
		w.serverError(rw, r, err)
		return
	}

	http.Redirect(rw, r, "/login", http.StatusSeeOther)
}

// isLocalPath reports whether a path stays on this site.
func isLocalPath(path string) bool {
	return strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "//") && !strings.HasPrefix(path, "/\\")
}

func cmpOr(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}

	return ""
}

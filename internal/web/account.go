package web

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/leightweight/orca-tasks/internal/auth"
	"github.com/leightweight/orca-tasks/internal/store"
	"github.com/leightweight/orca-tasks/internal/store/db"
)

type accountPage struct {
	RecoveryCodes int64
}

func (w *Web) account(rw http.ResponseWriter, r *http.Request) {
	user := currentUser(r.Context())

	n, err := w.Store.CountRecoveryCodes(r.Context(), user.ID)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.render(rw, r, http.StatusOK, "account", "Account", accountPage{RecoveryCodes: n})
}

func (w *Web) updateProfile(rw http.ResponseWriter, r *http.Request) {
	user := currentUser(r.Context())

	displayName := strings.TrimSpace(r.PostFormValue("display_name"))
	if displayName == "" {
		displayName = user.Username
	}

	err := w.Store.UpdateProfile(r.Context(), db.UpdateProfileParams{
		ID:          user.ID,
		DisplayName: displayName,
		Email:       strings.TrimSpace(r.PostFormValue("email")),
	})
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.redirect(rw, r, "/account", "Saved your profile.")
}

func (w *Web) changePassword(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(ctx)

	keys := []string{"ip:" + w.ClientIP(r), "user:" + user.Username}
	if !w.Limiter.Attempt(ctx, keys...) {
		w.redirect(rw, r, "/account", capitalize(errTooManyAttempts.Error())+".")
		return
	}

	if user.PasswordHash == nil || !auth.CheckPassword(r.PostFormValue("current"), *user.PasswordHash) {
		w.redirect(rw, r, "/account", "Your current password is wrong.")
		return
	}

	w.Limiter.Succeeded(ctx, keys...)

	// Check the form before the code, so a typo doesn't use up a code.
	hash, err := readPassword(r)
	if err != nil {
		w.redirect(rw, r, "/account", "The password wasn't changed: "+err.Error()+".")
		return
	}

	if err := w.checkSecondFactor(ctx, r, *user, false); err != nil {
		if !codeProblem(err) {
			w.serverError(rw, r, err)
			return
		}

		w.redirect(rw, r, "/account", "The password wasn't changed: "+err.Error()+".")
		return
	}

	if err := w.Store.SetPassword(ctx, db.SetPasswordParams{ID: user.ID, PasswordHash: &hash}); err != nil {
		w.serverError(rw, r, err)
		return
	}

	// The change bumped the session version, which ends every session; this
	// one carries on under the new version.
	updated, err := w.Store.GetPrincipal(ctx, user.ID)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	if err := w.signIn(ctx, updated); err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.redirect(rw, r, "/account", "Changed your password and signed out your other sessions.")
}

func (w *Web) newRecoveryCodes(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(ctx)

	if err := w.checkSecondFactor(ctx, r, *user, false); err != nil {
		if !codeProblem(err) {
			w.serverError(rw, r, err)
			return
		}

		w.redirect(rw, r, "/account", "No new codes: "+err.Error()+".")
		return
	}

	codes := auth.NewRecoveryCodes()

	err := w.Store.InTx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		if err := q.DeleteRecoveryCodes(ctx, user.ID); err != nil {
			return err
		}

		return q.InsertRecoveryCodes(ctx, db.InsertRecoveryCodesParams{PrincipalID: user.ID, CodeHashes: auth.HashRecoveryCodes(user.ID, codes)})
	})
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.Sessions.Put(ctx, sessionReveal, recoveryReveal(codes))
	http.Redirect(rw, r, "/account", http.StatusSeeOther)
}

type keysPage struct {
	Keys     []db.ListAPIKeysRow
	Agents   []db.Principal
	Projects []db.ListMyProjectsRow
}

func (w *Web) keys(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(ctx)

	keys, err := w.Store.ListAPIKeys(ctx, user.ID)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	agents, err := w.Store.ListAgents(ctx, &user.ID)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	projects, err := w.Store.ListMyProjects(ctx, user.ID)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.render(rw, r, http.StatusOK, "keys", "API keys", keysPage{Keys: keys, Agents: agents, Projects: projects})
}

func (w *Web) createKey(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(ctx)

	if err := r.ParseForm(); err != nil {
		w.redirect(rw, r, "/account/keys", "The form didn't arrive whole. Try again.")
		return
	}

	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == "" {
		w.redirect(rw, r, "/account/keys", "A key needs a name.")
		return
	}

	principal, err := strconv.ParseInt(r.PostFormValue("principal"), 10, 64)
	if err != nil {
		w.redirect(rw, r, "/account/keys", "Choose who the key is for.")
		return
	}

	username := user.Username

	if principal != user.ID {
		agents, err := w.Store.ListAgents(ctx, &user.ID)
		if err != nil {
			w.serverError(rw, r, err)
			return
		}

		i := slices.IndexFunc(agents, func(a db.Principal) bool { return a.ID == principal && a.DisabledAt == nil })
		if i < 0 {
			w.redirect(rw, r, "/account/keys", "You can only create keys for yourself and your active agents.")
			return
		}

		username = agents[i].Username
	}

	var projects []int64

	allProjects := r.PostFormValue("scope") != "some"
	if !allProjects {
		mine, err := w.Store.ListMyProjects(ctx, user.ID)
		if err != nil {
			w.serverError(rw, r, err)
			return
		}

		for _, value := range r.PostForm["project"] {
			id, err := strconv.ParseInt(value, 10, 64)
			if err == nil && slices.ContainsFunc(mine, func(p db.ListMyProjectsRow) bool { return p.Project.ID == id }) {
				projects = append(projects, id)
			}
		}

		if len(projects) == 0 {
			w.redirect(rw, r, "/account/keys", "Choose at least one project, or all of them.")
			return
		}
	}

	var expires *time.Time
	if days, err := strconv.Atoi(r.PostFormValue("expires")); err == nil && days > 0 {
		expires = new(w.Now().AddDate(0, 0, days))
	}

	// The code comes last, so a mistake elsewhere in the form doesn't use it up.
	if err := w.checkSecondFactor(ctx, r, *user, false); err != nil {
		if !codeProblem(err) {
			w.serverError(rw, r, err)
			return
		}

		w.redirect(rw, r, "/account/keys", "No key was created: "+err.Error()+".")
		return
	}

	key, hash := auth.NewAPIKey()

	err = w.Store.InTx(ctx, func(q *db.Queries, _ pgx.Tx) error {
		id, err := q.CreateAPIKey(ctx, db.CreateAPIKeyParams{
			PrincipalID: principal,
			Name:        name,
			KeyHash:     hash,
			ReadOnly:    r.PostFormValue("access") == "read",
			AllProjects: allProjects,
			ExpiresAt:   expires,
		})
		if err != nil || allProjects {
			return err
		}

		return q.AddAPIKeyProjects(ctx, db.AddAPIKeyProjectsParams{ApiKeyID: id, ProjectIds: projects})
	})
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.Sessions.Put(ctx, sessionReveal, reveal{
		Title: "Your new API key for " + username,
		Note:  "Copy it now: this is the only time it's shown. Anyone with it can act as " + username + ".",
		Lines: []string{key},
	})
	http.Redirect(rw, r, "/account/keys", http.StatusSeeOther)
}

func (w *Web) revokeKey(rw http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		w.notFound(rw, r)
		return
	}

	n, err := w.Store.RevokeAPIKey(r.Context(), db.RevokeAPIKeyParams{ID: id, OwnerID: currentUser(r.Context()).ID})
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	if n == 0 {
		w.notFound(rw, r)
		return
	}

	w.redirect(rw, r, "/account/keys", "Revoked the key.")
}

type agentsPage struct {
	Agents []db.Principal
}

func (w *Web) agents(rw http.ResponseWriter, r *http.Request) {
	user := currentUser(r.Context())

	agents, err := w.Store.ListAgents(r.Context(), &user.ID)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.render(rw, r, http.StatusOK, "agents", "Agents", agentsPage{Agents: agents})
}

func (w *Web) createAgent(rw http.ResponseWriter, r *http.Request) {
	user := currentUser(r.Context())

	agent, err := w.Store.CreateAgent(r.Context(), user.ID, r.PostFormValue("username"), r.PostFormValue("display_name"))
	if errors.Is(err, store.ErrInvalidUsername) || errors.Is(err, store.ErrUsernameTaken) {
		w.redirect(rw, r, "/account/agents", capitalize(err.Error())+".")
		return
	}

	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	w.redirect(rw, r, "/account/agents", "Created "+agent.Username+". Give it an API key next.")
}

func (w *Web) changeAgent(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(ctx)

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		w.notFound(rw, r)
		return
	}

	var n int64

	switch r.PathValue("action") {
	case "rename":
		name := strings.TrimSpace(r.PostFormValue("display_name"))
		if name == "" {
			w.redirect(rw, r, "/account/agents", "A display name can't be empty.")
			return
		}

		n, err = w.Store.UpdateAgent(ctx, db.UpdateAgentParams{ID: id, OwnerID: &user.ID, DisplayName: name})
	case "disable", "enable":
		n, err = w.Store.SetAgentDisabled(ctx, db.SetAgentDisabledParams{ID: id, OwnerID: &user.ID, Disabled: r.PathValue("action") == "disable"})
	default:
		w.notFound(rw, r)
		return
	}

	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	if n == 0 {
		w.notFound(rw, r)
		return
	}

	w.redirect(rw, r, "/account/agents", "Saved the agent.")
}

func capitalize(s string) string {
	if s == "" {
		return s
	}

	return strings.ToUpper(s[:1]) + s[1:]
}

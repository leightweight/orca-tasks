package web

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/leightweight/orca-tasks/internal/store"
)

// confirmPage asks before an action that can't be taken back with one click.
// The page has no JavaScript, so the button that starts the action links here,
// and this page's form posts the action.
type confirmPage struct {
	Detail string
	Action string
	Button string
	Cancel string
}

func (w *Web) confirm(rw http.ResponseWriter, r *http.Request, question string, page confirmPage) {
	page.Action = r.URL.Path
	w.render(rw, r, http.StatusOK, "confirm", question, page)
}

func (w *Web) confirmRevokeKey(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		w.notFound(rw, r)
		return
	}

	keys, err := w.Store.ListAPIKeys(ctx, currentUser(ctx).ID)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	for _, k := range keys {
		if k.ApiKey.ID == id && k.ApiKey.RevokedAt == nil {
			w.confirm(rw, r, "Revoke "+k.ApiKey.Name+"?", confirmPage{
				Detail: "Anything that signs in with this key, such as Orca or the CLI on a laptop, stops working right away. " +
					"A revoked key can't be restored; you'd create a new one.",
				Button: "Revoke key",
				Cancel: "/account/keys",
			})

			return
		}
	}

	w.notFound(rw, r)
}

func (w *Web) confirmDisableAgent(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := currentUser(ctx)

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		w.notFound(rw, r)
		return
	}

	agents, err := w.Store.ListAgents(ctx, &user.ID)
	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	for _, a := range agents {
		if a.ID == id && a.DisabledAt == nil {
			w.confirm(rw, r, "Disable "+a.Username+"?", confirmPage{
				Detail: "Its API keys stop working until you enable it again. Its tasks and comments stay.",
				Button: "Disable agent",
				Cancel: "/account/agents",
			})

			return
		}
	}

	w.notFound(rw, r)
}

func (w *Web) confirmRemoveMember(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	me := currentUser(ctx)

	row, ok := w.membership(rw, r)
	if !ok {
		return
	}

	user, err := w.Store.GetPrincipalByUsername(ctx, r.PathValue("username"))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && user.Kind != "user") {
		w.notFound(rw, r)
		return
	}

	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	back := "/projects/" + row.Membership.Key + "/members"

	if user.ID == me.ID {
		w.confirm(rw, r, "Leave "+row.Project.Name+"?", confirmPage{
			Detail: "You and your agents lose access to its issues. Your issues and comments stay, and an administrator can add you back.",
			Button: "Leave project",
			Cancel: back,
		})

		return
	}

	if row.Membership.Role != store.RoleAdmin {
		w.notFound(rw, r)
		return
	}

	w.confirm(rw, r, "Remove "+user.Username+" from "+row.Project.Name+"?", confirmPage{
		Detail: user.Username + " and their agents lose access to its issues. Their issues and comments stay, and adding them back restores their access.",
		Button: "Remove " + user.Username,
		Cancel: back,
	})
}

func (w *Web) confirmUserChange(rw http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		w.notFound(rw, r)
		return
	}

	user, err := w.Store.GetPrincipal(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (user.Kind != "user" || user.ID == currentUser(ctx).ID)) {
		w.notFound(rw, r)
		return
	}

	if err != nil {
		w.serverError(rw, r, err)
		return
	}

	page := confirmPage{Cancel: "/admin/users/" + strconv.FormatInt(user.ID, 10)}

	var question string

	switch r.PathValue("action") {
	case "disable":
		question = "Disable " + user.Username + "?"
		page.Detail = "They're signed out, can't sign in, and their API keys and agents' keys stop working until you enable them again. " +
			"Their tasks, comments, and attachments stay."
		page.Button = "Disable " + user.Username
	case "admin":
		question = "Make " + user.Username + " a site administrator?"
		page.Detail = "They can create and disable users, make reset links for anyone, and add themselves to any project."
		page.Button = "Make administrator"
	case "unadmin":
		question = "Remove " + user.Username + "'s site administration?"
		page.Detail = "They keep their projects but can't manage users or other projects."
		page.Button = "Remove administration"
	default:
		w.notFound(rw, r)
		return
	}

	w.confirm(rw, r, question, page)
}

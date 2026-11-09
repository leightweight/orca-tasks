package server_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/leightweight/orca-tasks/internal/auth"
	"github.com/leightweight/orca-tasks/internal/store"
	"github.com/leightweight/orca-tasks/internal/store/db"
	"github.com/leightweight/orca-tasks/internal/testutil"
)

// signIn signs a new browser in as alice with a TOTP code for the next step.
func signIn(t *testing.T, s *site, password, secret string) *browser {
	t.Helper()

	b := s.browser()
	b.expectRedirect(b.post("/login", url.Values{"username": {"alice"}, "password": {password}}), "/login/totp")
	b.expectRedirect(b.post("/login/totp", url.Values{"code": {b.code(secret)}}), "/")

	return b
}

func TestATOTPCodeWorksOnce(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	_, secret := signUp(t, s)
	code := auth.TOTPCode(secret, s.clock.step())

	first := s.browser()
	first.expectRedirect(first.post("/login", url.Values{"username": {"alice"}, "password": {"correct horse battery"}}), "/login/totp")
	first.expectRedirect(first.post("/login/totp", url.Values{"code": {code}}), "/projects")

	second := s.browser()
	second.expectRedirect(second.post("/login", url.Values{"username": {"alice"}, "password": {"correct horse battery"}}), "/login/totp")

	if r := second.post("/login/totp", url.Values{"code": {code}}); r.status != http.StatusBadRequest {
		t.Errorf("the same code a second time: status %d, want 400", r.status)
	}
}

func TestAPasswordChangeEndsOtherSessions(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	alice, secret := signUp(t, s)
	other := signIn(t, s, "correct horse battery", secret)

	alice.expectRedirect(alice.post("/account/password", url.Values{
		"current": {"correct horse battery"}, "password": {"a new long password"}, "confirm": {"a new long password"}, "code": {alice.code(secret)},
	}), "/account")

	if r := alice.get("/account"); r.status != http.StatusOK {
		t.Errorf("the session that changed the password: status %d, want 200", r.status)
	}

	other.expectRedirect(other.get("/account"), "/login")
}

func TestDisablingAndResettingEndSessions(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	alice, _ := signUp(t, s)

	// alice invites bob, who signs up through the link.
	alice.expectRedirect(alice.post("/admin/users", url.Values{"username": {"bob"}}), "/admin/users")
	link := find(t, linkPattern, alice.get("/admin/users").body)

	bob := s.browser()
	bob.expectRedirect(bob.post(link, url.Values{"password": {"bob's long password"}, "confirm": {"bob's long password"}}), "/enroll")
	bob.enroll()

	// The link worked once.
	if r := s.browser().get(link); r.status != http.StatusNotFound {
		t.Errorf("a used link: status %d, want 404", r.status)
	}

	bobID := find(t, regexpFor(`href="/admin/users/(\d+)">bob<`), alice.get("/admin/users").body)

	// A reset ends bob's session, since it sets new credentials.
	alice.expectRedirect(alice.post("/admin/users/"+bobID+"/reset", nil), "/admin/users")
	reset := find(t, linkPattern, alice.get("/admin/users").body)

	again := s.browser()
	again.expectRedirect(again.post(reset, url.Values{"password": {"bob's newer password"}, "confirm": {"bob's newer password"}}), "/enroll")
	again.enroll()

	bob.expectRedirect(bob.get("/account"), "/login")

	// Disabling ends the session bob just made.
	alice.expectRedirect(alice.post("/admin/users/"+bobID+"/disable", nil), "/admin/users")
	again.expectRedirect(again.get("/account"), "/login")
}

func TestCrossOriginPostsAreRefused(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	alice, _ := signUp(t, s)

	request, err := http.NewRequestWithContext(testutil.Context(t), "POST", s.url+"/projects", strings.NewReader("key=EVIL"))
	if err != nil {
		t.Fatal(err)
	}

	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Origin", "https://evil.example")
	request.Header.Set("Sec-Fetch-Site", "cross-site")

	if r := alice.do(request); r.status != http.StatusForbidden {
		t.Errorf("a cross-site form post: status %d, want 403", r.status)
	}
}

func TestSignInReturnsToThePageYouWanted(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	_, secret := signUp(t, s)

	b := s.browser()
	b.expectRedirect(b.get("/account/keys"), "/login")
	b.expectRedirect(b.post("/login", url.Values{"username": {"alice"}, "password": {"correct horse battery"}}), "/login/totp")
	b.expectRedirect(b.post("/login/totp", url.Values{"code": {b.code(secret)}}), "/account/keys")
}

func TestNavigationMarksTheCurrentPage(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	alice, _ := signUp(t, s)
	alice.expectRedirect(alice.post("/projects", url.Values{"key": {"OPS"}, "name": {"Operations"}}), "/projects/OPS/tasks")
	createIssue(t, s, "Rotate the keys", "To Do", nil)

	for path, current := range map[string]string{
		"/account/keys":         "/account/keys",
		"/account":              "/account",
		"/admin/proxy":          "/admin/proxy",
		"/projects/OPS/members": "/projects/OPS/members",
		"/browse/OPS-1":         "/projects/OPS/tasks",
	} {
		body := alice.get(path).body

		if want := `href="` + current + `" aria-current="page"`; !strings.Contains(body, want) {
			t.Errorf("%s doesn't mark %s as current", path, current)
		}

		if n := strings.Count(body, `aria-current="page"`); n != 1 {
			t.Errorf("%s marks %d links as current, want 1", path, n)
		}
	}
}

func TestFailedFormsShowAnError(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	alice, _ := signUp(t, s)

	// A create form shows the problem with what was typed still in it.
	r := alice.post("/projects", url.Values{"key": {"1BAD"}, "name": {"Operations"}})
	if r.status != http.StatusBadRequest || !strings.Contains(r.body, `<div role="alert" data-variant="danger">`) ||
		!strings.Contains(r.body, `value="Operations"`) {
		t.Errorf("an invalid key: status %d: %.2000s", r.status, r.body)
	}

	// A form on a page of settings redirects back to it with the problem, once.
	alice.expectRedirect(alice.post("/projects", url.Values{"key": {"OPS"}}), "/projects/OPS/tasks")
	alice.get("/projects/OPS/tasks")
	alice.expectRedirect(alice.post("/projects/OPS/key", url.Values{"key": {"1BAD"}}), "/projects/OPS/settings")

	body := alice.get("/projects/OPS/settings").body
	if !strings.Contains(body, `<div role="alert" data-variant="danger">`) || strings.Contains(body, `data-variant="success"`) {
		t.Errorf("an invalid key isn't shown as an error: %.1500s", body)
	}

	if strings.Contains(alice.get("/projects/OPS/settings").body, `<div role="alert"`) {
		t.Error("the error shows again on the next page")
	}
}

func TestRiskyActionsAskFirst(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	alice, _ := signUp(t, s)
	alice.expectRedirect(alice.post("/projects", url.Values{"key": {"OPS"}, "name": {"Operations"}}), "/projects/OPS")

	// Following the Leave link asks, and changes nothing.
	page := alice.get("/projects/OPS/members/alice/remove")
	if page.status != http.StatusOK || !strings.Contains(page.body, "Leave Operations?") ||
		!strings.Contains(page.body, `<form method="post" action="/projects/OPS/members/alice/remove"`) {
		t.Errorf("the leave confirmation: status %d: %.1500s", page.status, page.body)
	}

	if r := alice.get("/projects/OPS/tasks"); r.status != http.StatusOK {
		t.Errorf("asking to leave left the project: status %d", r.status)
	}

	// Nobody is asked about something they can't do.
	for _, path := range []string{"/account/keys/1/revoke", "/account/agents/1/disable", "/projects/OPS/members/nobody/remove"} {
		if r := alice.get(path); r.status != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", path, r.status)
		}
	}
}

func TestChangingYourKeyStaysOnTheProject(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	alice, _ := signUp(t, s)
	alice.expectRedirect(alice.post("/projects", url.Values{"key": {"OPS"}, "name": {"Operations"}}), "/projects/OPS")

	alice.expectRedirect(alice.post("/projects/OPS/key", url.Values{"key": {"ops2"}}), "/projects/OPS2/settings")

	if body := alice.get("/projects/OPS2/settings").body; !strings.Contains(body, "its own key is <code>OPS</code>") {
		t.Errorf("the project page doesn't say the project's own key: %.1500s", body)
	}

	alice.expectRedirect(alice.post("/projects/OPS2/key", url.Values{"key": {"2BAD"}}), "/projects/OPS2")
}

func TestAdministratorsManageAProjectOnItsOwnPage(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	alice, _ := signUp(t, s)
	alice.expectRedirect(alice.post("/projects", url.Values{"key": {"OPS"}, "name": {"Operations"}}), "/projects/OPS")
	alice.expectRedirect(alice.post("/admin/users", url.Values{"username": {"bob"}}), "/admin/users")

	page := "/admin/projects/" + find(t, regexpFor(`href="/admin/projects/(\d+)">OPS<`), alice.get("/admin/projects").body)

	alice.expectRedirect(alice.post(page+"/members", url.Values{"username": {"bob"}}), page)

	body := alice.get(page).body
	if !strings.Contains(body, "<td>bob</td>") || !strings.Contains(body, `href="/admin/projects" aria-current="page"`) {
		t.Errorf("the project's admin page: %.2000s", body)
	}

	// A new member joins as a member unless the form says otherwise.
	if strings.Contains(body, `<option value="admin" selected`) {
		t.Error("the add-member form defaults to admin")
	}

	alice.expectRedirect(alice.post(page+"/delete", url.Values{"confirm": {"nope"}}), page)
}

func TestAUserHasAnAdministrationPage(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	alice, _ := signUp(t, s)
	alice.expectRedirect(alice.post("/admin/users", url.Values{"username": {"bob"}}), "/admin/users")

	users := alice.get("/admin/users").body
	page := "/admin/users/" + find(t, regexpFor(`href="/admin/users/(\d+)">bob<`), users)

	body := alice.get(page).body
	if !strings.Contains(body, "Make a reset link") || !strings.Contains(body, `href="`+page+`/disable"`) || !strings.Contains(body, "invited") {
		t.Errorf("bob's page: %.2000s", body)
	}

	// An administrator's own page has no way to lock them out.
	self := "/admin/users/" + find(t, regexpFor(`href="/admin/users/(\d+)">alice<`), users)
	if body := alice.get(self).body; strings.Contains(body, self+"/disable") {
		t.Error("alice's own page offers to disable her")
	}

	alice.expectRedirect(alice.post(page+"/reset", nil), page)
}

// createIssue adds an issue to alice's OPS project through the store, in a
// status and under a parent if given.
func createIssue(t *testing.T, s *site, summary, status string, parent *db.Issue) db.Issue {
	t.Helper()

	ctx := testutil.Context(t)

	alice, err := s.store.GetPrincipalByUsername(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}

	row, err := s.store.GetMembershipByKey(ctx, db.GetMembershipByKeyParams{PrincipalID: alice.ID, Key: "OPS"})
	if err != nil {
		t.Fatal(err)
	}

	catalog, err := s.store.LoadCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}

	st, err := catalog.Status(status)
	if err != nil {
		t.Fatal(err)
	}

	n := store.NewIssue{
		ProjectID: row.Project.ID, Summary: summary, StatusID: st.ID, TypeID: catalog.IssueTypes[0].ID, Reporter: row.Membership.ID,
	}
	if parent != nil {
		n.Parent = &store.Parent{ID: parent.ID, Number: parent.Number}
	}

	issue, err := s.store.CreateIssue(ctx, n)
	if err != nil {
		t.Fatal(err)
	}

	return issue
}

func TestAProjectListsItsIssues(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	alice, _ := signUp(t, s)
	alice.expectRedirect(alice.post("/projects", url.Values{"key": {"OPS"}, "name": {"Operations"}}), "/projects/OPS")

	epic := createIssue(t, s, "Rotate every credential", "To Do", nil)
	createIssue(t, s, "Rotate the keys", "In Progress", &epic)
	createIssue(t, s, "Renew the certificates", "Done", &epic)

	open := alice.get("/projects/OPS/tasks").body
	if !strings.Contains(open, `href="/browse/OPS-2">OPS-2</a>`) || !strings.Contains(open, "in OPS-1") || strings.Contains(open, "OPS-3") {
		t.Errorf("the open issues: %.3000s", open)
	}

	if all := alice.get("/projects/OPS/tasks?show=all").body; !strings.Contains(all, `href="/browse/OPS-3">OPS-3</a>`) {
		t.Errorf("all issues leaves out the done one: %.3000s", all)
	}
}

func TestAnIssueLinksBackToItsProject(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	alice, _ := signUp(t, s)
	alice.expectRedirect(alice.post("/projects", url.Values{"key": {"OPS"}, "name": {"Operations"}}), "/projects/OPS")
	createIssue(t, s, "Rotate the keys", "To Do", nil)

	if body := alice.get("/browse/OPS-1").body; !strings.Contains(body, `<a class="crumbs" href="/projects/OPS/tasks">Operations</a>`) {
		t.Errorf("the issue page doesn't link to its project: %.1500s", body)
	}
}

func TestRelativeTimesShowTheExactTime(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	alice, _ := signUp(t, s)
	alice.expectRedirect(alice.post("/projects", url.Values{"key": {"OPS"}}), "/projects/OPS")

	issue := createIssue(t, s, "Rotate the keys", "To Do", nil)
	want := `<time datetime="` + issue.CreatedAt.UTC().Format(time.RFC3339) + `" title="` + issue.CreatedAt.UTC().Format("Mon, 2 Jan 2006 15:04 MST") + `">`

	if body := alice.get("/browse/OPS-1").body; !strings.Contains(body, want) {
		t.Errorf("no %s in the issue page: %.2500s", want, body)
	}
}

func TestHomeOpensTheLastProject(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	alice, _ := signUp(t, s)

	alice.expectRedirect(alice.get("/"), "/projects")

	alice.expectRedirect(alice.post("/projects", url.Values{"key": {"OPS"}}), "/projects/OPS/tasks")
	alice.expectRedirect(alice.post("/projects", url.Values{"key": {"WEB"}}), "/projects/WEB/tasks")
	alice.get("/projects/OPS/members")

	alice.expectRedirect(alice.get("/"), "/projects/OPS/tasks")
}

package server_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/leightweight/orca-tasks/internal/auth"
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

	bobID := find(t, regexpFor(`href="/admin/users/(\d+)/disable"`), alice.get("/admin/users").body)

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

	for path, current := range map[string]string{
		"/account/keys": "/account/keys",
		"/account":      "/account",
		"/admin/proxy":  "/admin/proxy",
		"/browse/OPS-9": "/projects",
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

	alice.expectRedirect(alice.post("/projects", url.Values{"key": {"1BAD"}}), "/projects")

	body := alice.get("/projects").body
	if !strings.Contains(body, `<div role="alert" data-variant="danger">`) || strings.Contains(body, `data-variant="success"`) {
		t.Errorf("an invalid key isn't shown as an error: %.1500s", body)
	}

	// The message shows once.
	if strings.Contains(alice.get("/projects").body, `data-variant="danger"`) {
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

	if r := alice.get("/projects/OPS"); r.status != http.StatusOK {
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

	alice.expectRedirect(alice.post("/projects/OPS/key", url.Values{"key": {"ops2"}}), "/projects/OPS2")

	if body := alice.get("/projects/OPS2").body; !strings.Contains(body, "Its own key is <code>OPS</code>") {
		t.Errorf("the project page doesn't say the project's own key: %.1500s", body)
	}

	alice.expectRedirect(alice.post("/projects/OPS2/key", url.Values{"key": {"2BAD"}}), "/projects/OPS2")
}

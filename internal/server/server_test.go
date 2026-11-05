package server_test

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"mime/multipart"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/leightweight/orca-tasks/internal/auth"
	"github.com/leightweight/orca-tasks/internal/server"
	"github.com/leightweight/orca-tasks/internal/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(testutil.RunWithPostgres(m))
}

// clock is a fake clock that tests move forward, so each TOTP code comes from
// a new step.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *clock) step() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(30 * time.Second)

	return c.now
}

type site struct {
	t     *testing.T
	url   string
	clock *clock
	setup string
}

func newSite(t *testing.T) *site {
	t.Helper()

	listener := httptest.NewUnstartedServer(nil)
	base := "http://" + listener.Listener.Addr().String()
	c := &clock{now: time.Now()}

	app, err := server.Build(testutil.Context(t), testutil.Store(t), server.Config{
		BaseURL:               base,
		SecretKey:             "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		DataDir:               t.TempDir(),
		MaxUpload:             1 << 20,
		SuppressCreatedDetail: 0,
		Now:                   c.Now,
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	listener.Config.Handler = app.Handler
	listener.Start()
	t.Cleanup(listener.Close)

	return &site{t: t, url: base, clock: c, setup: app.SetupCode}
}

// browser is a client with its own cookies that doesn't follow redirects.
type browser struct {
	site   *site
	client *http.Client
}

func (s *site) browser() *browser {
	jar, err := cookiejar.New(nil)
	if err != nil {
		s.t.Fatal(err)
	}

	client := *testutil.Client
	client.Jar = jar

	return &browser{site: s, client: &client}
}

type response struct {
	status   int
	location string
	body     string
}

func (b *browser) do(request *http.Request) response {
	b.site.t.Helper()

	r, err := b.client.Do(request)
	if err != nil {
		b.site.t.Fatalf("%s %s: %v", request.Method, request.URL.Path, err)
	}
	defer func() { _ = r.Body.Close() }()

	body, _ := io.ReadAll(r.Body)

	return response{status: r.StatusCode, location: r.Header.Get("Location"), body: string(body)}
}

func (b *browser) get(path string) response {
	b.site.t.Helper()

	request, err := http.NewRequestWithContext(testutil.Context(b.site.t), "GET", b.site.url+path, nil)
	if err != nil {
		b.site.t.Fatal(err)
	}

	return b.do(request)
}

func (b *browser) post(path string, form url.Values) response {
	b.site.t.Helper()

	request, err := http.NewRequestWithContext(testutil.Context(b.site.t), "POST", b.site.url+path, strings.NewReader(form.Encode()))
	if err != nil {
		b.site.t.Fatal(err)
	}

	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	return b.do(request)
}

// expectRedirect checks a response sent the browser on, and returns where.
func (b *browser) expectRedirect(r response, prefix string) {
	b.site.t.Helper()

	if r.status != http.StatusSeeOther || !strings.HasPrefix(r.location, prefix) {
		b.site.t.Fatalf("got status %d to %q, want a redirect to %s: %.300s", r.status, r.location, prefix, r.body)
	}
}

var (
	secretPattern   = regexp.MustCompile(`by hand: <code>([A-Z2-7]+)</code>`)
	keyPattern      = regexp.MustCompile(`otk_[a-z2-7]+`)
	linkPattern     = regexp.MustCompile(`/reset/[a-z2-7]+`)
	recoveryPattern = regexp.MustCompile(`<pre>([a-z2-7]{5}-[a-z2-7]{5})`)
)

func find(t *testing.T, pattern *regexp.Regexp, body string) string {
	t.Helper()

	match := pattern.FindStringSubmatch(body)
	if match == nil {
		t.Fatalf("no %s in %.500s", pattern, body)
	}

	return match[len(match)-1]
}

// enroll finishes the authenticator step and returns the secret.
func (b *browser) enroll() string {
	b.site.t.Helper()

	page := b.get("/enroll").body
	if !strings.Contains(page, `src="data:image/png;base64,`) {
		b.site.t.Errorf("the enrollment page has no QR code: %.500s", page)
	}

	secret := find(b.site.t, secretPattern, page)
	b.expectRedirect(b.post("/enroll", url.Values{"code": {auth.TOTPCode(secret, b.site.clock.step())}}), "/account")

	return secret
}

func (b *browser) code(secret string) string {
	return auth.TOTPCode(secret, b.site.clock.step())
}

func TestEverythingTogether(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	alice := s.browser()

	// A fresh install sends everyone to setup, which needs the logged code.
	alice.expectRedirect(alice.get("/"), "/setup")

	credentials := url.Values{"username": {"alice"}, "display_name": {"Alice"}, "password": {"correct horse battery"}, "confirm": {"correct horse battery"}}

	wrong := url.Values{"code": {"nope"}}
	maps.Copy(wrong, credentials)

	if r := alice.post("/setup", wrong); r.status != http.StatusBadRequest {
		t.Fatalf("a wrong setup code: status %d", r.status)
	}

	setup := url.Values{"code": {s.setup}}
	maps.Copy(setup, credentials)

	alice.expectRedirect(alice.post("/setup", setup), "/enroll")
	aliceSecret := alice.enroll()

	if r := alice.get("/account"); !strings.Contains(r.body, "Your recovery codes") || recoveryPattern.FindString(r.body) == "" {
		t.Fatalf("the account page didn't show recovery codes: %.500s", r.body)
	}

	if r := alice.get("/setup"); r.status != http.StatusSeeOther {
		t.Errorf("setup after the first user: status %d", r.status)
	}

	// A project, an agent, and a key for the agent.
	alice.expectRedirect(alice.post("/projects", url.Values{"key": {"ops"}, "name": {"Operations"}}), "/projects/OPS")
	alice.expectRedirect(alice.post("/account/agents", url.Values{"username": {"alice-claude"}, "display_name": {"Claude"}}), "/account/agents")

	agentID := find(t, regexp.MustCompile(`<option value="(\d+)">alice-claude \(agent\)`), alice.get("/account/keys").body)
	alice.expectRedirect(alice.post("/account/keys", url.Values{
		"name": {"laptop"}, "principal": {agentID}, "access": {"write"}, "scope": {"all"}, "expires": {"0"}, "code": {alice.code(aliceSecret)},
	}), "/account/keys")

	key := find(t, keyPattern, alice.get("/account/keys").body)

	// The agent creates an issue and attaches an image through the API.
	api := func(method, path, contentType string, body io.Reader) response {
		request, err := http.NewRequestWithContext(testutil.Context(t), method, s.url+path, body)
		if err != nil {
			t.Fatal(err)
		}

		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("Content-Type", contentType)
		request.Header.Set("X-Atlassian-Token", "no-check")

		return s.browser().do(request)
	}

	issue := strings.NewReader(`{"fields":{"project":{"key":"OPS"},"summary":"Draw the diagram"}}`)
	if r := api("POST", "/rest/api/2/issue", "application/json", issue); r.status != http.StatusCreated {
		t.Fatalf("creating an issue: %d %s", r.status, r.body)
	}

	var form bytes.Buffer

	parts := multipart.NewWriter(&form)
	part, _ := parts.CreateFormFile("file", "diagram.png")
	_, _ = part.Write([]byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"))
	_ = parts.Close()

	if r := api("POST", "/rest/api/2/issue/OPS-1/attachments", parts.FormDataContentType(), &form); r.status != http.StatusOK {
		t.Fatalf("uploading: %d %s", r.status, r.body)
	}

	// Alice's session reads the issue page and its image.
	page := alice.get("/browse/OPS-1")
	if page.status != http.StatusOK || !strings.Contains(page.body, "Draw the diagram") || !strings.Contains(page.body, "Claude") {
		t.Fatalf("browse: %d %.500s", page.status, page.body)
	}

	image := find(t, regexp.MustCompile(`href="(/secure/attachment/\d+/diagram.png)"`), page.body)
	if r := alice.get(image); r.status != http.StatusOK {
		t.Errorf("downloading with a session: status %d", r.status)
	}

	if r := s.browser().get(image); r.status != http.StatusForbidden {
		t.Errorf("downloading without credentials: status %d", r.status)
	}

	// Alice invites bob, who sets up through the link.
	alice.expectRedirect(alice.post("/admin/users", url.Values{"username": {"bob"}}), "/admin/users")
	link := find(t, linkPattern, alice.get("/admin/users").body)

	bob := s.browser()
	bob.expectRedirect(bob.post(link, url.Values{"password": {"bob's long password"}, "confirm": {"bob's long password"}}), "/enroll")
	bob.enroll()

	recovery := find(t, recoveryPattern, bob.get("/account").body)

	if r := bob.get("/admin/users"); r.status != http.StatusNotFound {
		t.Errorf("a non-admin on the admin page: status %d", r.status)
	}

	if r := bob.get("/browse/OPS-1"); r.status != http.StatusNotFound {
		t.Errorf("a non-member browsing: status %d", r.status)
	}

	// Bob signs back in with a recovery code.
	bob.expectRedirect(bob.post("/logout", nil), "/login")
	bob.expectRedirect(bob.post("/login", url.Values{"username": {"bob"}, "password": {"bob's long password"}}), "/login/totp")
	bob.expectRedirect(bob.post("/login/totp", url.Values{"code": {recovery}}), "/projects")

	// The same recovery code doesn't work twice.
	again := s.browser()
	again.expectRedirect(again.post("/login", url.Values{"username": {"bob"}, "password": {"bob's long password"}}), "/login/totp")

	if r := again.post("/login/totp", url.Values{"code": {recovery}}); r.status != http.StatusBadRequest {
		t.Errorf("a used recovery code: status %d", r.status)
	}

	for _, path := range []string{"/admin/users", "/admin/projects", "/admin/proxy", "/account", "/account/agents", "/projects/OPS"} {
		if r := alice.get(path); r.status != http.StatusOK {
			t.Errorf("%s: status %d", path, r.status)
		}
	}

	if r := s.browser().get("/readyz"); r.status != http.StatusOK {
		t.Errorf("readyz: status %d: %s", r.status, r.body)
	}
}

func TestFailedSignInsAreLimited(t *testing.T) {
	t.Parallel()

	s := newSite(t)
	b := s.browser()

	var last response
	for i := range 12 {
		last = b.post("/login", url.Values{"username": {"carol"}, "password": {fmt.Sprintf("guess %d", i)}})
	}

	if !strings.Contains(last.body, "too many failed attempts") {
		t.Errorf("the twelfth failure wasn't limited: %.300s", last.body)
	}
}

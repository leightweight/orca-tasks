package jira

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leightweight/orca-tasks/internal/attachments"
	"github.com/leightweight/orca-tasks/internal/auth"
	"github.com/leightweight/orca-tasks/internal/store"
	"github.com/leightweight/orca-tasks/internal/store/db"
	"github.com/leightweight/orca-tasks/internal/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(testutil.RunWithPostgres(m))
}

// api is a server with two users. Alice administers OPS, which bob joins, and
// PRIVATE, which bob can't see. Alice has an agent, claude.
type api struct {
	t      *testing.T
	server *httptest.Server
	store  *store.Store
	alice  string
	bob    string
	claude string
}

func newAPI(t *testing.T) *api {
	t.Helper()

	s := testutil.Store(t)
	ctx := testutil.Context(t)

	catalog, err := s.LoadCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}

	files, err := attachments.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	server := &Server{
		Store:     s,
		Catalog:   catalog,
		Files:     files,
		MaxUpload: 1 << 20,
		Log:       slog.New(slog.DiscardHandler),
		ClientIP:  func(*http.Request) string { return "192.0.2.1" },
		Now:       time.Now,
	}

	mux := http.NewServeMux()
	server.Register(mux)

	a := &api{t: t, server: httptest.NewServer(mux), store: s}
	t.Cleanup(a.server.Close)
	server.BaseURL = a.server.URL

	alice := a.user("alice")
	bob := a.user("bob")

	claude, err := s.CreateAgent(ctx, alice.ID, "claude", "Claude")
	if err != nil {
		t.Fatal(err)
	}

	ops, err := s.CreateProject(ctx, alice.ID, "OPS", "Operations")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := s.CreateProject(ctx, alice.ID, "PRIVATE", ""); err != nil {
		t.Fatal(err)
	}

	if _, err := s.AddMember(ctx, ops.ID, bob.ID, store.RoleMember); err != nil {
		t.Fatal(err)
	}

	a.alice = a.key(alice.ID, false, nil)
	a.bob = a.key(bob.ID, false, nil)
	a.claude = a.key(claude.ID, false, nil)

	return a
}

func (a *api) user(username string) db.Principal {
	a.t.Helper()

	hash := "unused"
	user, err := a.store.CreateUser(testutil.Context(a.t), db.CreateUserParams{
		Username: username, DisplayName: strings.ToUpper(username[:1]) + username[1:], PasswordHash: &hash, TotpSecret: []byte("sealed"),
	})
	if err != nil {
		a.t.Fatal(err)
	}

	return user
}

func (a *api) key(principal int64, readOnly bool, projects []int64) string {
	a.t.Helper()

	key, hash := auth.NewAPIKey()
	ctx := testutil.Context(a.t)

	keyID, err := a.store.CreateAPIKey(ctx, db.CreateAPIKeyParams{
		PrincipalID: principal, Name: "test", KeyHash: hash, ReadOnly: readOnly, AllProjects: projects == nil,
	})
	if err != nil {
		a.t.Fatal(err)
	}

	if projects != nil {
		if err := a.store.AddAPIKeyProjects(ctx, db.AddAPIKeyProjectsParams{ApiKeyID: keyID, ProjectIds: projects}); err != nil {
			a.t.Fatal(err)
		}
	}

	return key
}

// call sends a request with a bearer key and decodes a JSON response into
// into, returning the status code.
func (a *api) call(key, method, path, body string, into any) int {
	a.t.Helper()

	request, err := http.NewRequestWithContext(testutil.Context(a.t), method, a.server.URL+path, strings.NewReader(body))
	if err != nil {
		a.t.Fatal(err)
	}

	request.Header.Set("Content-Type", "application/json")
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}

	return a.do(request, into)
}

func (a *api) do(request *http.Request, into any) int {
	a.t.Helper()

	response, err := testutil.Client.Do(request)
	if err != nil {
		a.t.Fatalf("%s %s: %v", request.Method, request.URL.Path, err)
	}
	defer func() { _ = response.Body.Close() }()

	if into != nil && response.StatusCode < 300 {
		if err := json.NewDecoder(response.Body).Decode(into); err != nil {
			a.t.Fatalf("decoding %s %s: %v", request.Method, request.URL.Path, err)
		}
	}

	return response.StatusCode
}

func (a *api) mustCall(key, method, path, body string, into any) {
	a.t.Helper()

	if status := a.call(key, method, path, body, into); status >= 300 {
		a.t.Fatalf("%s %s: status %d", method, path, status)
	}
}

func (a *api) create(key, project, summary string) string {
	a.t.Helper()

	var created struct {
		Key string `json:"key"`
	}

	a.mustCall(key, "POST", "/rest/api/2/issue", fmt.Sprintf(`{"fields":{"project":{"key":%q},"summary":%q}}`, project, summary), &created)

	return created.Key
}

type searchResult struct {
	Total  int  `json:"total"`
	IsLast bool `json:"isLast"`
	Issues []struct {
		ID     string `json:"id"`
		Key    string `json:"key"`
		Fields struct {
			Summary     string   `json:"summary"`
			Description string   `json:"description"`
			Labels      []string `json:"labels"`
			Status      struct {
				Name string `json:"name"`
			} `json:"status"`
			Priority *struct {
				Name string `json:"name"`
			} `json:"priority"`
			Assignee *struct {
				Name   string `json:"name"`
				Active bool   `json:"active"`
			} `json:"assignee"`
			Reporter *struct {
				Name string `json:"name"`
			} `json:"reporter"`
			Project struct {
				Key string `json:"key"`
			} `json:"project"`
			Attachment []struct {
				ID       string `json:"id"`
				Filename string `json:"filename"`
				Content  string `json:"content"`
				MimeType string `json:"mimeType"`
			} `json:"attachment"`
		} `json:"fields"`
		RenderedFields *struct {
			Description string `json:"description"`
		} `json:"renderedFields"`
	} `json:"issues"`
}

func (a *api) search(key, query string) searchResult {
	a.t.Helper()

	var result searchResult

	body, err := json.Marshal(map[string]any{"jql": query, "maxResults": 50})
	if err != nil {
		a.t.Fatal(err)
	}

	a.mustCall(key, "POST", "/rest/api/2/search", string(body), &result)

	return result
}

func TestAuthentication(t *testing.T) {
	t.Parallel()

	a := newAPI(t)

	var me struct {
		Name        string `json:"name"`
		DisplayName string `json:"displayName"`
	}

	a.mustCall(a.claude, "GET", "/rest/api/2/myself", "", &me)
	if me.Name != "alice/claude" || me.DisplayName != "Claude" {
		t.Errorf("myself = %+v", me)
	}

	// Orca deletes a site that answers 401, so bad credentials get 403.
	for _, key := range []string{"", "otk_wrong", "not-a-key"} {
		if status := a.call(key, "GET", "/rest/api/2/myself", "", nil); status != http.StatusForbidden {
			t.Errorf("key %q: status %d, want 403", key, status)
		}
	}

	request, err := http.NewRequestWithContext(testutil.Context(t), "GET", a.server.URL+"/rest/api/2/myself", nil)
	if err != nil {
		t.Fatal(err)
	}

	request.SetBasicAuth("alice", a.alice)
	if status := a.do(request, nil); status != http.StatusOK {
		t.Errorf("basic auth: status %d", status)
	}

	request.SetBasicAuth("bob", a.alice)
	if status := a.do(request, nil); status != http.StatusForbidden {
		t.Errorf("basic auth with another user's key: status %d, want 403", status)
	}
}

func TestMembershipLimitsWhatYouSee(t *testing.T) {
	t.Parallel()

	a := newAPI(t)
	a.create(a.alice, "OPS", "Shared")
	secret := a.create(a.alice, "PRIVATE", "Secret")

	if got := a.search(a.bob, "").Total; got != 1 {
		t.Errorf("bob sees %d issues, want 1", got)
	}

	if status := a.call(a.bob, "GET", "/rest/api/2/issue/"+secret, "", nil); status != http.StatusNotFound {
		t.Errorf("bob reading %s: status %d, want 404", secret, status)
	}

	status := a.call(a.bob, "POST", "/rest/api/2/issue", `{"fields":{"project":{"key":"PRIVATE"},"summary":"x"}}`, nil)
	if status != http.StatusBadRequest {
		t.Errorf("bob creating in PRIVATE: status %d, want 400", status)
	}

	var projects []struct {
		Key string `json:"key"`
	}

	a.mustCall(a.bob, "GET", "/rest/api/2/project", "", &projects)

	if len(projects) != 1 || projects[0].Key != "OPS" {
		t.Errorf("bob's projects = %+v", projects)
	}

	// Alice can't assign an issue to bob in a project bob isn't in.
	status = a.call(a.alice, "PUT", "/rest/api/2/issue/"+secret+"/assignee", `{"name":"bob"}`, nil)
	if status != http.StatusBadRequest {
		t.Errorf("assigning to a non-member: status %d, want 400", status)
	}
}

func TestKeyScopes(t *testing.T) {
	t.Parallel()

	a := newAPI(t)
	a.create(a.alice, "OPS", "Shared")
	a.create(a.alice, "PRIVATE", "Secret")

	alice, err := a.store.GetPrincipalByUsername(testutil.Context(t), "alice")
	if err != nil {
		t.Fatal(err)
	}

	row, err := a.store.GetMembershipByKey(testutil.Context(t), db.GetMembershipByKeyParams{PrincipalID: alice.ID, Key: "OPS"})
	if err != nil {
		t.Fatal(err)
	}

	limited := a.key(alice.ID, false, []int64{row.Project.ID})
	if got := a.search(limited, ""); got.Total != 1 || got.Issues[0].Fields.Project.Key != "OPS" {
		t.Errorf("a key limited to OPS sees %+v", got)
	}

	readOnly := a.key(alice.ID, true, nil)
	if got := a.search(readOnly, "").Total; got != 2 {
		t.Errorf("a read-only key's search found %d issues, want 2", got)
	}

	if status := a.call(readOnly, "POST", "/rest/api/2/issue/OPS-1/comment", `{"body":"x"}`, nil); status != http.StatusForbidden {
		t.Errorf("a read-only key commenting: status %d, want 403", status)
	}
}

func TestCreateUpdateAndTransition(t *testing.T) {
	t.Parallel()

	a := newAPI(t)

	key := a.create(a.claude, "OPS", "Rotate the keys")
	if key != "OPS-1" {
		t.Fatalf("created %s, want OPS-1", key)
	}

	a.mustCall(a.alice, "PUT", "/rest/api/2/issue/"+key,
		`{"fields":{"priority":{"name":"high"},"assignee":{"name":"bob"},"description":"Use **the** vault."},"update":{"labels":[{"add":"security"}]}}`, nil)

	var transitions struct {
		Transitions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"transitions"`
	}

	a.mustCall(a.bob, "GET", "/rest/api/2/issue/"+key+"/transitions", "", &transitions)

	var done string

	for _, tr := range transitions.Transitions {
		if tr.Name == "Done" {
			done = tr.ID
		}
	}

	move := fmt.Sprintf(`{"transition":{"id":%q},"update":{"comment":[{"add":{"body":"Rotated."}}]}}`, done)
	a.mustCall(a.bob, "POST", "/rest/api/2/issue/"+key+"/transitions", move, nil)

	result := a.search(a.alice, "assignee = bob AND statusCategory = Done")
	if result.Total != 1 {
		t.Fatalf("search found %d issues, want 1", result.Total)
	}

	issue := result.Issues[0].Fields
	if issue.Priority == nil || issue.Priority.Name != "High" || issue.Reporter.Name != "alice/claude" || len(issue.Labels) != 1 {
		t.Errorf("issue = %+v", issue)
	}

	var comments struct {
		Comments []struct {
			Body   string `json:"body"`
			Author struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"comments"`
	}

	a.mustCall(a.alice, "GET", "/rest/api/2/issue/"+key+"/comment", "", &comments)

	if len(comments.Comments) != 1 || comments.Comments[0].Author.Name != "bob" {
		t.Errorf("comments = %+v", comments)
	}

	unknownPriority := `{"fields":{"project":{"key":"OPS"},"summary":"x","priority":{"name":"Urgent"}}}`
	if status := a.call(a.alice, "POST", "/rest/api/2/issue", unknownPriority, nil); status != http.StatusBadRequest {
		t.Errorf("an unknown priority: status %d, want 400", status)
	}
}

func TestRemovedAssigneeIsInactive(t *testing.T) {
	t.Parallel()

	a := newAPI(t)
	key := a.create(a.alice, "OPS", "Rotate the keys")
	a.mustCall(a.alice, "PUT", "/rest/api/2/issue/"+key+"/assignee", `{"name":"bob"}`, nil)

	ctx := testutil.Context(t)

	bob, err := a.store.GetPrincipalByUsername(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}

	row, err := a.store.GetMembershipByKey(ctx, db.GetMembershipByKeyParams{PrincipalID: bob.ID, Key: "OPS"})
	if err != nil {
		t.Fatal(err)
	}

	if err := a.store.RemoveMember(ctx, row.Project.ID, bob.ID); err != nil {
		t.Fatal(err)
	}

	result := a.search(a.alice, "")
	if assignee := result.Issues[0].Fields.Assignee; assignee == nil || assignee.Name != "bob" || assignee.Active {
		t.Errorf("assignee = %+v, want bob, inactive", assignee)
	}

	if status := a.call(a.bob, "GET", "/rest/api/2/issue/"+key, "", nil); status != http.StatusNotFound {
		t.Errorf("bob after leaving: status %d, want 404", status)
	}
}

func TestSearchPagesAndOrders(t *testing.T) {
	t.Parallel()

	a := newAPI(t)
	for i := range 3 {
		a.create(a.alice, "OPS", fmt.Sprintf("Task %d", i))
	}

	var page searchResult
	a.mustCall(a.alice, "GET", "/rest/api/2/search?jql=ORDER%20BY%20key%20ASC&maxResults=2&startAt=1", "", &page)

	if page.Total != 3 || len(page.Issues) != 2 || page.Issues[0].Key != "OPS-2" || !page.IsLast {
		t.Errorf("page = total %d, %d issues, first %s, last %v", page.Total, len(page.Issues), page.Issues[0].Key, page.IsLast)
	}

	if status := a.call(a.alice, "GET", "/rest/api/2/search?jql=project%20%3D", "", nil); status != http.StatusBadRequest {
		t.Errorf("bad JQL: status %d, want 400", status)
	}
}

func upload(t *testing.T, a *api, key, issue, filename, content string) (int, []byte) {
	t.Helper()

	return uploadFiles(t, a, key, issue, map[string]string{filename: content})
}

// uploadFiles sends several files in one request.
func uploadFiles(t *testing.T, a *api, key, issue string, files map[string]string) (int, []byte) {
	t.Helper()

	var body bytes.Buffer

	form := multipart.NewWriter(&body)

	for filename, content := range files {
		part, err := form.CreateFormFile("file", filename)
		if err != nil {
			t.Fatal(err)
		}

		_, _ = part.Write([]byte(content))
	}

	_ = form.Close()

	request, err := http.NewRequestWithContext(testutil.Context(t), "POST", a.server.URL+"/rest/api/2/issue/"+issue+"/attachments", &body)
	if err != nil {
		t.Fatal(err)
	}

	request.Header.Set("Content-Type", form.FormDataContentType())
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("X-Atlassian-Token", "no-check")

	response, err := testutil.Client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()

	data, _ := io.ReadAll(response.Body)

	return response.StatusCode, data
}

func TestAttachments(t *testing.T) {
	t.Parallel()

	a := newAPI(t)
	key := a.create(a.alice, "OPS", "Draw the diagram")

	png := "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"
	if status, body := upload(t, a, a.claude, key, "../diagram.png", png); status != http.StatusOK {
		t.Fatalf("upload: status %d: %s", status, body)
	}

	if status, _ := upload(t, a, a.bob, key, "page.html", "<script>alert(1)</script>"); status != http.StatusOK {
		t.Fatalf("upload: status %d", status)
	}

	var issue struct {
		Fields struct {
			Attachment []struct {
				ID       string `json:"id"`
				Filename string `json:"filename"`
				Content  string `json:"content"`
				MimeType string `json:"mimeType"`
			} `json:"attachment"`
		} `json:"fields"`
	}

	a.mustCall(a.alice, "GET", "/rest/api/2/issue/"+key+"?expand=renderedFields", "", &issue)

	files := issue.Fields.Attachment
	if len(files) != 2 || files[0].Filename != "diagram.png" || files[0].MimeType != "image/png" {
		t.Fatalf("attachments = %+v", files)
	}

	for i, want := range []string{"inline", "attachment"} {
		request, err := http.NewRequestWithContext(testutil.Context(t), "GET", files[i].Content, nil)
		if err != nil {
			t.Fatal(err)
		}

		request.Header.Set("Authorization", "Bearer "+a.alice)

		response, err := testutil.Client.Do(request)
		if err != nil {
			t.Fatal(err)
		}

		_ = response.Body.Close()

		if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Disposition"), want) ||
			response.Header.Get("Content-Security-Policy") != "sandbox" {
			t.Errorf("%s: status %d, headers %v", files[i].Filename, response.StatusCode, response.Header)
		}
	}

	// Only the uploader's account or a project administrator can delete. Claude
	// uploaded the diagram for alice, so bob, a member, can't delete it, and
	// claude, acting for alice, the administrator, can delete bob's page.
	if status := a.call(a.bob, "DELETE", "/rest/api/2/attachment/"+files[0].ID, "", nil); status != http.StatusForbidden {
		t.Errorf("bob deleting claude's upload: status %d, want 403", status)
	}

	a.mustCall(a.claude, "DELETE", "/rest/api/2/attachment/"+files[1].ID, "", nil)
	a.mustCall(a.alice, "DELETE", "/rest/api/2/attachment/"+files[0].ID, "", nil)

	if status := a.call(a.alice, "GET", "/rest/api/2/attachment/"+files[0].ID, "", nil); status != http.StatusNotFound {
		t.Errorf("a deleted attachment: status %d, want 404", status)
	}
}

func TestBoardsAndCreateMeta(t *testing.T) {
	t.Parallel()

	a := newAPI(t)

	var boards struct {
		Values []struct {
			ID int `json:"id"`
		} `json:"values"`
	}

	a.mustCall(a.bob, "GET", "/rest/agile/1.0/board?projectKeyOrId=OPS", "", &boards)

	if len(boards.Values) != 1 {
		t.Fatalf("boards = %+v", boards)
	}

	var config struct {
		ColumnConfig struct {
			Columns []struct {
				Name string `json:"name"`
			} `json:"columns"`
		} `json:"columnConfig"`
	}

	a.mustCall(a.bob, "GET", fmt.Sprintf("/rest/agile/1.0/board/%d/configuration", boards.Values[0].ID), "", &config)

	if len(config.ColumnConfig.Columns) != 6 || config.ColumnConfig.Columns[0].Name != "To Do" {
		t.Errorf("columns = %+v", config.ColumnConfig.Columns)
	}

	var meta struct {
		Values []struct {
			FieldID       string `json:"fieldId"`
			AllowedValues []struct {
				ID string `json:"id"`
			} `json:"allowedValues"`
		} `json:"values"`
	}

	a.mustCall(a.bob, "GET", "/rest/api/2/issue/createmeta/OPS/issuetypes/10001", "", &meta)

	for _, field := range meta.Values {
		if field.FieldID == "assignee" && len(field.AllowedValues) != 2 {
			t.Errorf("assignees = %+v, want alice and bob, and no agents", field.AllowedValues)
		}
	}
}

func TestAgentsAreNotAssignees(t *testing.T) {
	t.Parallel()

	a := newAPI(t)
	key := a.create(a.claude, "OPS", "Rotate the keys")

	if status := a.call(a.alice, "PUT", "/rest/api/2/issue/"+key+"/assignee", `{"name":"alice/claude"}`, nil); status != http.StatusBadRequest {
		t.Errorf("assigning an agent: status %d, want 400", status)
	}

	// An agent's automatic assignment goes to its owner, and its
	// currentUser() matches what it reported and what its owner is assigned.
	a.mustCall(a.claude, "PUT", "/rest/api/2/issue/"+key+"/assignee", `{"name":"-1"}`, nil)

	result := a.search(a.claude, "assignee = currentUser() AND reporter = currentUser()")
	if result.Total != 1 || result.Issues[0].Fields.Assignee == nil || result.Issues[0].Fields.Assignee.Name != "alice" {
		t.Errorf("search = %+v, want OPS-1 assigned to alice", result)
	}

	var people []struct {
		Name string `json:"name"`
	}

	a.mustCall(a.alice, "GET", "/rest/api/2/user/search?username=.", "", &people)

	for _, p := range people {
		if strings.Contains(p.Name, "/") {
			t.Errorf("user search listed the agent %s", p.Name)
		}
	}
}

func TestOrderByKeyAndPriority(t *testing.T) {
	t.Parallel()

	a := newAPI(t)

	for i := 1; i <= 11; i++ {
		a.create(a.alice, "OPS", fmt.Sprintf("Task %d", i))
	}

	for key, priority := range map[string]string{"OPS-2": "Low", "OPS-3": "Highest", "OPS-10": "Medium"} {
		a.mustCall(a.alice, "PUT", "/rest/api/2/issue/"+key, fmt.Sprintf(`{"fields":{"priority":{"name":%q}}}`, priority), nil)
	}

	keys := func(query string) []string {
		issues := a.search(a.alice, query).Issues

		got := make([]string, 0, len(issues))
		for _, issue := range issues {
			got = append(got, issue.Key)
		}

		return got
	}

	if got := keys("ORDER BY key ASC"); len(got) != 11 || got[1] != "OPS-2" || got[9] != "OPS-10" {
		t.Errorf("ORDER BY key = %v, want OPS-1, OPS-2, ..., OPS-10, OPS-11", got)
	}

	if got := keys("priority IS NOT EMPTY ORDER BY priority DESC"); !slices.Equal(got, []string{"OPS-3", "OPS-10", "OPS-2"}) {
		t.Errorf("ORDER BY priority DESC = %v, want Highest, Medium, Low", got)
	}
}

func TestUnknownJQLFieldsFail(t *testing.T) {
	t.Parallel()

	a := newAPI(t)
	a.create(a.alice, "OPS", "Rotate the keys")

	if status := a.call(a.alice, "POST", "/rest/api/2/search", `{"jql":"stauts != Done"}`, nil); status != http.StatusBadRequest {
		t.Errorf("a misspelled field: status %d, want 400", status)
	}

	// Orca's sprint preset still matches nothing, rather than failing.
	if got := a.search(a.alice, "statusCategory != Done AND sprint in openSprints()").Total; got != 0 {
		t.Errorf("sprint in openSprints() matched %d issues", got)
	}

	if got := a.search(a.alice, "issue = OPS-1").Total; got != 1 {
		t.Errorf("issue = OPS-1 matched %d issues, want 1", got)
	}
}

func TestErrorsIsAnObject(t *testing.T) {
	t.Parallel()

	a := newAPI(t)

	request, err := http.NewRequestWithContext(testutil.Context(t), "GET", a.server.URL+"/rest/api/2/issue/OPS-99", nil)
	if err != nil {
		t.Fatal(err)
	}

	request.Header.Set("Authorization", "Bearer "+a.alice)

	response, err := testutil.Client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()

	var body map[string]json.RawMessage
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}

	if string(body["errors"]) != "{}" {
		t.Errorf("errors = %s, want {}", body["errors"])
	}
}

func TestUploadsOverTheRequestCapGet413(t *testing.T) {
	t.Parallel()

	a := newAPI(t)
	key := a.create(a.alice, "OPS", "Attach the logs")

	// Each file is under the 1 MiB file limit, but together they pass the
	// request's cap of one file's limit plus 1 MiB.
	part := strings.Repeat("x", 900<<10)
	files := map[string]string{"a.log": part, "b.log": part, "c.log": part}

	if status, body := uploadFiles(t, a, a.alice, key, files); status != http.StatusRequestEntityTooLarge {
		t.Errorf("status %d, want 413: %s", status, body)
	}
}

func TestMaxResultsIsClampedToTheCap(t *testing.T) {
	t.Parallel()

	a := newAPI(t)

	var page struct {
		MaxResults int `json:"maxResults"`
	}

	a.mustCall(a.alice, "POST", "/rest/api/2/search", `{"jql":"","maxResults":500}`, &page)

	if page.MaxResults != maxPageSize {
		t.Errorf("maxResults = %d, want the cap, %d", page.MaxResults, maxPageSize)
	}
}

package web

import (
	"net/url"
	"testing"

	"github.com/leightweight/orca-tasks/internal/jira"
)

func TestFilterJQL(t *testing.T) {
	t.Parallel()

	for query, want := range map[string]string{
		"":                                  "statusCategory != Done ORDER BY updated DESC",
		"show=all&sort=key":                 "ORDER BY key ASC",
		"show=done&assignee=me":             "statusCategory = Done AND assignee = currentUser() ORDER BY updated DESC",
		"show=all&assignee=none&type=Bug":   `assignee IS EMPTY AND type = "Bug" ORDER BY updated DESC`,
		`show=all&q=say+"hi"+\+now`:         `text ~ "say \"hi\" \\ now" ORDER BY updated DESC`,
		"show=all&labels=tls&sort=priority": `labels = "tls" ORDER BY priority DESC, updated DESC`,
	} {
		filters, err := url.ParseQuery(query)
		if err != nil {
			t.Fatal(err)
		}

		got := filterJQL(filters)
		if got != want {
			t.Errorf("filterJQL(%q) = %q, want %q", query, got, want)
		}

		if _, err := jira.ParseJQL(got); err != nil {
			t.Errorf("filterJQL(%q) made JQL that doesn't parse: %v", query, err)
		}
	}
}

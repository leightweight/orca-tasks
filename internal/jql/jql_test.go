package jql

import (
	"testing"
	"time"
)

type fakeRecord struct {
	fields map[string][]string
	times  map[string]time.Time
}

func (r fakeRecord) Field(name string) []string { return r.fields[name] }

func (r fakeRecord) Timestamp(name string) (time.Time, bool) {
	value, ok := r.times[name]
	return value, ok
}

func testContext() Context {
	return Context{CurrentUsers: []string{"alice"}, Now: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)}
}

func openRecord() fakeRecord {
	return fakeRecord{
		fields: map[string][]string{
			"key":            {"ORCA-1"},
			"summary":        {"Fix the flaky import"},
			"text":           {"Fix the flaky import", "affects nightly runs"},
			"project":        {"ORCA", "Orca"},
			"status":         {"In Progress"},
			"statuscategory": {"indeterminate", "In Progress"},
			"type":           {"Bug"},
			"assignee":       {"alice", "Alice Example"},
			"reporter":       {"alice"},
			"labels":         {"backend", "flaky"},
			"priority":       {"High"},
		},
		times: map[string]time.Time{
			"created": time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
			"updated": time.Date(2026, 8, 24, 9, 0, 0, 0, time.UTC),
		},
	}
}

func doneRecord() fakeRecord {
	r := openRecord()
	r.fields["key"] = []string{"ORCA-2"}
	r.fields["status"] = []string{"Done"}
	r.fields["statuscategory"] = []string{"done", "Done"}
	r.fields["resolution"] = []string{"Done"}

	return r
}

// TestOrcaPresets covers the queries Orca builds itself (buildJiraPresetJql),
// plus the older resolution-based spelling of the same presets.
func TestOrcaPresets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		query string
		open  bool
		done  bool
	}{
		{"assigned", "assignee = currentUser() AND statusCategory != Done ORDER BY updated DESC", true, false},
		{"reported", "reporter = currentUser() AND statusCategory != Done ORDER BY updated DESC", true, false},
		{"done", "assignee = currentUser() AND statusCategory = Done ORDER BY updated DESC", false, true},
		{"all", "statusCategory != Done ORDER BY updated DESC", true, false},
		{"status picker", `statusCategory != Done AND status in ("In Progress", "In \"Review\"") ORDER BY updated DESC`, true, false},
		{"current sprint", "statusCategory != Done AND sprint in openSprints() ORDER BY updated DESC", false, false},
		{"text search", `text ~ "flaky*"`, true, true},
		{"key search", `key = "ORCA-1"`, true, false},
		{"legacy assigned", "assignee = currentUser() AND resolution = Unresolved ORDER BY updated DESC", true, false},
		{"legacy done", "assignee = currentUser() AND resolution IS NOT EMPTY ORDER BY updated DESC", false, true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			query, err := Parse(test.query)
			if err != nil {
				t.Fatalf("Parse(%q): %v", test.query, err)
			}

			if got := query.Match(openRecord(), testContext()); got != test.open {
				t.Errorf("open issue: got %v, want %v", got, test.open)
			}

			if got := query.Match(doneRecord(), testContext()); got != test.done {
				t.Errorf("done issue: got %v, want %v", got, test.done)
			}
		})
	}
}

func TestOrderBy(t *testing.T) {
	t.Parallel()

	query, err := Parse("statusCategory != Done ORDER BY updated DESC, key ASC")
	if err != nil {
		t.Fatal(err)
	}

	terms := query.Terms()
	if len(terms) != 2 {
		t.Fatalf("got %d order terms, want 2", len(terms))
	}

	if terms[0].Field != "updated" || !terms[0].Descending() {
		t.Errorf("first term = %+v, want updated DESC", terms[0])
	}

	if terms[1].Field != "key" || terms[1].Descending() {
		t.Errorf("second term = %+v, want key ASC", terms[1])
	}
}

func TestAdHocQueries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		query string
		want  bool
	}{
		{`project = ORCA`, true},
		{`project = "Orca"`, true},
		{`project = OTHER`, false},
		{`project != OTHER`, true},
		{`status = "In Progress"`, true},
		{`status IN ("To Do", "In Progress")`, true},
		{`status NOT IN ("To Do", "Done")`, true},
		{`labels = backend`, true},
		{`label IN (frontend, flaky)`, true},
		{`priority = High AND type = Bug`, true},
		{`priority = Low OR type = Bug`, true},
		{`NOT (type = Bug)`, false},
		{`assignee IS NOT EMPTY`, true},
		{`resolution IS EMPTY`, true},
		{`summary ~ flaky`, true},
		{`summary ~ "nonexistent"`, false},
		{`summary !~ nonexistent`, true},
		{`updated >= -7d`, true},
		{`updated >= -1d`, false},
		{`created < 2026-08-15`, true},
		{`created > 2026-08-15`, false},
		{`created > "2026-08-01T08:00:00Z"`, true},
		{`(project = ORCA OR project = OTHER) AND type = Bug`, true},
		{`assignee = "Alice Example"`, true},
		{`Project = orca and Type = bug`, true},
		{`issuetype = Bug`, true},
		{`key = ORCA-1`, true},
		{``, true},
	}

	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			t.Parallel()

			query, err := Parse(test.query)
			if err != nil {
				t.Fatalf("Parse(%q): %v", test.query, err)
			}

			if got := query.Match(openRecord(), testContext()); got != test.want {
				t.Errorf("match = %v, want %v", got, test.want)
			}
		})
	}
}

func TestInvalidQueries(t *testing.T) {
	t.Parallel()

	for _, query := range []string{
		`project =`,
		`project == = ORCA`,
		`(project = ORCA`,
		`project = "unterminated`,
		`ORDER BY`,
	} {
		t.Run(query, func(t *testing.T) {
			t.Parallel()

			if _, err := Parse(query); err == nil {
				t.Errorf("Parse(%q) succeeded, want an error", query)
			}
		})
	}
}

func TestSort(t *testing.T) {
	t.Parallel()

	older, newer := openRecord(), doneRecord()
	older.times["updated"] = time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	records := []fakeRecord{older, newer}
	Sort(records, nil)

	if got := records[0].fields["key"][0]; got != "ORCA-2" {
		t.Errorf("default order put %s first, want the most recently updated, ORCA-2", got)
	}

	query, err := Parse("ORDER BY key ASC")
	if err != nil {
		t.Fatal(err)
	}

	Sort(records, query.Terms())

	if got := records[0].fields["key"][0]; got != "ORCA-1" {
		t.Errorf("key ASC put %s first, want ORCA-1", got)
	}
}

func TestCurrentUserMatchesAnAgentsOwner(t *testing.T) {
	t.Parallel()

	ctx := Context{CurrentUsers: []string{"alice/claude", "alice"}}

	query, err := Parse("assignee = currentUser() AND reporter IN (currentUser())")
	if err != nil {
		t.Fatal(err)
	}

	if !query.Match(openRecord(), ctx) {
		t.Error("currentUser() for an agent should match its owner")
	}
}

func TestEmptyValues(t *testing.T) {
	t.Parallel()

	unassigned := openRecord()
	delete(unassigned.fields, "assignee")

	tests := []struct {
		query    string
		assigned bool
		empty    bool
	}{
		{`assignee = EMPTY`, false, true},
		{`assignee = null`, false, true},
		{`assignee != EMPTY`, true, false},
		{`assignee in (EMPTY, currentUser())`, true, true},
		{`assignee not in (EMPTY)`, true, false},
		{`assignee = "EMPTY"`, false, false},
		{`assignee IS EMPTY`, false, true},
	}

	for _, test := range tests {
		t.Run(test.query, func(t *testing.T) {
			t.Parallel()

			query, err := Parse(test.query)
			if err != nil {
				t.Fatal(err)
			}

			if got := query.Match(openRecord(), testContext()); got != test.assigned {
				t.Errorf("assigned issue: %v, want %v", got, test.assigned)
			}

			if got := query.Match(unassigned, testContext()); got != test.empty {
				t.Errorf("unassigned issue: %v, want %v", got, test.empty)
			}
		})
	}
}

func TestISNeedsEmpty(t *testing.T) {
	t.Parallel()

	for _, query := range []string{`assignee IS currentUser()`, `status IS NOT Done`} {
		if _, err := Parse(query); err == nil {
			t.Errorf("Parse(%q) succeeded, want an error", query)
		}
	}
}

func TestValidateRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	known := func(field string) bool { return field == "status" || field == "key" || field == "updated" }

	for query, ok := range map[string]bool{
		`status != Done ORDER BY updated`: true,
		`issue = ORCA-1`:                  true,
		`stauts != Done`:                  false,
		`status = Done ORDER BY rank`:     false,
	} {
		parsed, err := Parse(query)
		if err != nil {
			t.Fatal(err)
		}

		if err := parsed.Validate(known); (err == nil) != ok {
			t.Errorf("Validate(%q) = %v, want ok %v", query, err, ok)
		}
	}
}

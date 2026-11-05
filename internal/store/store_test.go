package store_test

import (
	"os"
	"testing"

	"github.com/leightweight/orca-tasks/internal/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(testutil.RunWithPostgres(m))
}

func TestCatalogIsSeeded(t *testing.T) {
	t.Parallel()

	s := testutil.Store(t)

	statuses, err := s.ListStatuses(testutil.Context(t))
	if err != nil {
		t.Fatal(err)
	}

	if len(statuses) != 6 || statuses[0].Name != "To Do" {
		t.Errorf("statuses = %+v, want six starting with To Do", statuses)
	}

	types, err := s.ListIssueTypes(testutil.Context(t))
	if err != nil {
		t.Fatal(err)
	}

	if len(types) != 5 || types[0].Name != "Task" {
		t.Errorf("issue types = %+v, want five starting with Task", types)
	}
}

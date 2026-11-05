package store

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/leightweight/orca-tasks/internal/store/db"
)

// Status categories, Jira's three.
const (
	CategoryNew           = "new"
	CategoryIndeterminate = "indeterminate"
	CategoryDone          = "done"
)

// Catalog is the statuses, issue types, and priorities, which a migration
// seeds and nothing changes, so the server loads them once.
type Catalog struct {
	Statuses   []db.Status
	IssueTypes []db.IssueType
	Priorities []db.Priority
}

// LoadCatalog reads the catalog.
func (s *Store) LoadCatalog(ctx context.Context) (*Catalog, error) {
	statuses, err := s.ListStatuses(ctx)
	if err != nil {
		return nil, err
	}

	types, err := s.ListIssueTypes(ctx)
	if err != nil {
		return nil, err
	}

	priorities, err := s.ListPriorities(ctx)
	if err != nil {
		return nil, err
	}

	return &Catalog{Statuses: statuses, IssueTypes: types, Priorities: priorities}, nil
}

// Status finds a status by ID or name, ignoring case.
func (c *Catalog) Status(idOrName string) (db.Status, error) {
	return find(c.Statuses, "status", idOrName, func(s db.Status) (int32, string) { return s.ID, s.Name })
}

// StatusByID returns the status with an ID.
func (c *Catalog) StatusByID(id int32) db.Status {
	s, _ := c.Status(strconv.Itoa(int(id)))
	return s
}

// IssueType finds an issue type by ID or name, ignoring case.
func (c *Catalog) IssueType(idOrName string) (db.IssueType, error) {
	return find(c.IssueTypes, "issue type", idOrName, func(t db.IssueType) (int32, string) { return t.ID, t.Name })
}

// IssueTypeByID returns the issue type with an ID.
func (c *Catalog) IssueTypeByID(id int32) db.IssueType {
	t, _ := c.IssueType(strconv.Itoa(int(id)))
	return t
}

// Priority finds a priority by ID or name, ignoring case.
func (c *Catalog) Priority(idOrName string) (db.Priority, error) {
	return find(c.Priorities, "priority", idOrName, func(p db.Priority) (int32, string) { return p.ID, p.Name })
}

// PriorityByID returns the priority with an ID, or nil for no priority.
func (c *Catalog) PriorityByID(id *int32) *db.Priority {
	if id == nil {
		return nil
	}

	p, err := c.Priority(strconv.Itoa(int(*id)))
	if err != nil {
		return nil
	}

	return &p
}

// UnknownError is a name that isn't in the catalog. Its message lists the
// names that are.
type UnknownError struct {
	Kind, Name string
	Valid      []string
}

func (e *UnknownError) Error() string {
	quoted := make([]string, len(e.Valid))
	for i, name := range e.Valid {
		quoted[i] = strconv.Quote(name)
	}

	return fmt.Sprintf("unknown %s %q; use one of %s", e.Kind, e.Name, strings.Join(quoted, ", "))
}

func find[T any](entries []T, kind, idOrName string, key func(T) (int32, string)) (T, error) {
	idOrName = strings.TrimSpace(idOrName)

	i := slices.IndexFunc(entries, func(entry T) bool {
		id, name := key(entry)
		return strconv.Itoa(int(id)) == idOrName || strings.EqualFold(name, idOrName)
	})
	if i >= 0 {
		return entries[i], nil
	}

	var zero T

	valid := make([]string, len(entries))
	for i, entry := range entries {
		_, valid[i] = key(entry)
	}

	return zero, &UnknownError{Kind: kind, Name: idOrName, Valid: valid}
}

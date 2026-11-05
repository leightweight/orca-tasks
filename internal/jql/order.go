package jql

import (
	"slices"
	"strings"
)

// Comparer is a Record that orders some fields itself, such as issue keys by
// number or priorities by rank, where comparing their text would be wrong.
type Comparer interface {
	// Compare orders the record against another on a field, reporting false
	// for a field it leaves to the default text comparison.
	Compare(field string, other Record) (int, bool)
}

// Sort orders records by an ORDER BY clause, most recently updated first when
// there is none. It is stable, so records that compare equal keep their order.
func Sort[R Record](records []R, terms []*OrderTerm) {
	if len(terms) == 0 {
		terms = []*OrderTerm{{Field: FieldUpdated, Direction: "DESC"}}
	}

	slices.SortStableFunc(records, func(left, right R) int {
		for _, term := range terms {
			if cmp := compareField(left, right, CanonicalField(term.Field)); cmp != 0 {
				if term.Descending() {
					return -cmp
				}

				return cmp
			}
		}

		return 0
	})
}

func compareField(left, right Record, field string) int {
	if c, ok := left.(Comparer); ok {
		if cmp, handled := c.Compare(field, right); handled {
			return cmp
		}
	}

	if leftTime, ok := left.Timestamp(field); ok {
		rightTime, _ := right.Timestamp(field)
		return leftTime.Compare(rightTime)
	}

	return strings.Compare(
		strings.ToLower(strings.Join(left.Field(field), " ")),
		strings.ToLower(strings.Join(right.Field(field), " ")),
	)
}

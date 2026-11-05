package jql

import (
	"slices"
	"strings"
)

// Sort orders records by an ORDER BY clause, most recently updated first when
// there is none. It is stable, so records that compare equal keep their order.
func Sort[R Record](records []R, terms []*OrderTerm) {
	if len(terms) == 0 {
		terms = []*OrderTerm{{Field: FieldUpdated, Direction: "DESC"}}
	}

	slices.SortStableFunc(records, func(left, right R) int {
		for _, term := range terms {
			field := CanonicalField(term.Field)

			var cmp int
			if leftTime, ok := left.Timestamp(field); ok {
				rightTime, _ := right.Timestamp(field)
				cmp = leftTime.Compare(rightTime)
			} else {
				cmp = strings.Compare(
					strings.ToLower(strings.Join(left.Field(field), " ")),
					strings.ToLower(strings.Join(right.Field(field), " ")),
				)
			}

			if cmp != 0 {
				if term.Descending() {
					return -cmp
				}

				return cmp
			}
		}

		return 0
	})
}

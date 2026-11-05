// Package jql parses and evaluates the subset of Jira Query Language that Orca
// and the CLI send.
package jql

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/participle/v2"
	"github.com/alecthomas/participle/v2/lexer"
)

// The grammar is participle struct tags rather than a hand-written parser. The
// only logic of our own is Jira's field and operator semantics.
//
// Rule order matters: the multi-word and keyword rules come before Ident, so
// `IS NOT`, `NOT IN`, and `ORDER BY` lex as single tokens instead of names.
var jqlLexer = lexer.MustSimple([]lexer.SimpleRule{
	{Name: "Whitespace", Pattern: `\s+`},
	{Name: "String", Pattern: `"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'`},
	{Name: "OrderBy", Pattern: `(?i)\bORDER\s+BY\b`},
	{Name: "WordOp", Pattern: `(?i)\bIS\s+NOT\b|\bIS\b|\bNOT\s+IN\b|\bIN\b`},
	{Name: "Logical", Pattern: `(?i)\b(?:AND|OR|NOT)\b`},
	{Name: "SymOp", Pattern: `!=|<=|>=|!~|=|<|>|~`},
	{Name: "Ident", Pattern: `[\p{L}\p{N}_.*+:/@-]+`},
	{Name: "Punct", Pattern: `[(),]`},
})

var parser = participle.MustBuild[Query](
	participle.Lexer(jqlLexer),
	participle.Elide("Whitespace"),
	// Literals in the grammar, such as "AND" and "DESC", match in any case.
	participle.CaseInsensitive("Logical", "Ident", "WordOp", "OrderBy"),
	participle.UseLookahead(participle.MaxLookahead),
)

// Query is a parsed JQL statement. A nil Where matches every issue.
type Query struct {
	Where *orExpr  `parser:"@@?"`
	Order *orderBy `parser:"@@?"`
}

type orderBy struct {
	Terms []*OrderTerm `parser:"OrderBy @@ (',' @@)*"`
}

// OrderTerm is one field of an ORDER BY clause.
type OrderTerm struct {
	Field     string `parser:"@Ident"`
	Direction string `parser:"@('ASC' | 'DESC')?"`
}

// Descending reports whether the term sorts in descending order.
func (t *OrderTerm) Descending() bool { return strings.EqualFold(t.Direction, "DESC") }

type orExpr struct {
	Operands []*andExpr `parser:"@@ ('OR' @@)*"`
}

type andExpr struct {
	Operands []*notExpr `parser:"@@ ('AND' @@)*"`
}

type notExpr struct {
	Negated bool     `parser:"@'NOT'?"`
	Primary *primary `parser:"@@"`
}

type primary struct {
	Group     *orExpr    `parser:"'(' @@ ')'"`
	Condition *condition `parser:"| @@"`
}

// condition is one `field op value` comparison. Whether the value suits the
// operator is checked during evaluation, which keeps the grammar small.
type condition struct {
	Field  string   `parser:"@(Ident | String)"`
	Op     string   `parser:"@(SymOp | WordOp)"`
	List   []*value `parser:"( '(' @@ (',' @@)* ')'"`
	Single *value   `parser:"| @@ )"`
}

// value is a literal or a function call such as currentUser().
type value struct {
	Function *functionCall `parser:"  @@"`
	Literal  string        `parser:"| @(String | Ident)"`
}

type functionCall struct {
	Name string   `parser:"@Ident '('"`
	Args []string `parser:"(@(Ident | String) (',' @(Ident | String))*)? ')'"`
}

// Parse compiles a JQL string. An empty query matches everything.
func Parse(input string) (*Query, error) {
	if strings.TrimSpace(input) == "" {
		return &Query{}, nil
	}

	query, err := parser.ParseString("", input)
	if err != nil {
		return nil, err
	}

	// IS and IS NOT only take EMPTY or NULL; anything else would quietly
	// behave like IS EMPTY.
	for _, c := range query.conditions() {
		if op := strings.ToLower(strings.Join(strings.Fields(c.Op), " ")); (op == "is" || op == "is not") && !isEmptyValue(c.Single) {
			return nil, fmt.Errorf("%s %s needs EMPTY or NULL", c.Field, strings.ToUpper(op))
		}
	}

	return query, nil
}

// Validate checks that every field the query compares or orders by is one
// known reports true for, so a typo such as stauts fails instead of matching
// everything.
func (q *Query) Validate(known func(field string) bool) error {
	if q == nil {
		return nil
	}

	for _, c := range q.conditions() {
		if !known(CanonicalField(c.Field)) {
			return fmt.Errorf("unknown field %q", unquote(c.Field))
		}
	}

	for _, t := range q.Terms() {
		if !known(CanonicalField(t.Field)) {
			return fmt.Errorf("unknown field %q in ORDER BY", t.Field)
		}
	}

	return nil
}

// conditions lists every comparison in the WHERE clause.
func (q *Query) conditions() []*condition {
	var found []*condition

	var walk func(e *orExpr)
	walk = func(e *orExpr) {
		if e == nil {
			return
		}

		for _, and := range e.Operands {
			for _, not := range and.Operands {
				if not.Primary.Group != nil {
					walk(not.Primary.Group)
				} else {
					found = append(found, not.Primary.Condition)
				}
			}
		}
	}

	walk(q.Where)

	return found
}

// Record is the view of an issue the evaluator compares against.
type Record interface {
	// Field returns every value a field carries: each label, or both a user's
	// name and display name, so that either matches.
	Field(name string) []string

	// Timestamp resolves the date fields for range comparisons.
	Timestamp(name string) (time.Time, bool)
}

// Context is what JQL functions resolve against.
type Context struct {
	// CurrentUsers are the names currentUser() matches: the caller, and for an
	// agent, its owner too.
	CurrentUsers []string
	Now          time.Time
}

// Match reports whether a record satisfies the query's WHERE clause.
func (q *Query) Match(r Record, ctx Context) bool {
	if q == nil || q.Where == nil {
		return true
	}

	return q.Where.eval(r, ctx)
}

// Terms is the ORDER BY clause, or nil.
func (q *Query) Terms() []*OrderTerm {
	if q == nil || q.Order == nil {
		return nil
	}

	return q.Order.Terms
}

func (e *orExpr) eval(r Record, ctx Context) bool {
	for _, operand := range e.Operands {
		if operand.eval(r, ctx) {
			return true
		}
	}

	return false
}

func (e *andExpr) eval(r Record, ctx Context) bool {
	for _, operand := range e.Operands {
		if !operand.eval(r, ctx) {
			return false
		}
	}

	return true
}

func (e *notExpr) eval(r Record, ctx Context) bool {
	return e.Primary.eval(r, ctx) != e.Negated
}

func (p *primary) eval(r Record, ctx Context) bool {
	if p.Group != nil {
		return p.Group.eval(r, ctx)
	}

	return p.Condition.eval(r, ctx)
}

func (c *condition) eval(r Record, ctx Context) bool {
	field := CanonicalField(c.Field)
	op := strings.ToLower(strings.Join(strings.Fields(c.Op), " "))
	values := c.values()

	// `resolution = Unresolved` is Jira's spelling of "has no resolution".
	if field == "resolution" && len(values) == 1 {
		switch strings.ToLower(values[0].text()) {
		case "unresolved", "empty", "null":
			switch op {
			case "=":
				return isEmpty(r, field)
			case "!=":
				return !isEmpty(r, field)
			}
		}
	}

	switch op {
	case "is":
		return isEmpty(r, field)
	case "is not":
		return !isEmpty(r, field)
	case "=", "in":
		return matchesAny(r, field, values, ctx, equalsFold)
	case "!=", "not in":
		return !matchesAny(r, field, values, ctx, equalsFold)
	case "~":
		return matchesAny(r, field, values, ctx, containsWildcard)
	case "!~":
		return !matchesAny(r, field, values, ctx, containsWildcard)
	case "<", "<=", ">", ">=":
		return compare(r, field, op, values, ctx)
	default:
		return false
	}
}

func (c *condition) values() []*value {
	if len(c.List) > 0 {
		return c.List
	}

	if c.Single != nil {
		return []*value{c.Single}
	}

	return nil
}

// isEmptyValue reports whether a value means "no value": an unquoted EMPTY or
// NULL, or the functions empty() and null(). A quoted "EMPTY" is the text.
func isEmptyValue(v *value) bool {
	if v == nil {
		return false
	}

	name := v.Literal
	if v.Function != nil {
		name = v.Function.Name
	}

	return strings.EqualFold(name, "empty") || strings.EqualFold(name, "null")
}

// text is the comparable string a value contributes.
func (v *value) text() string {
	if v == nil {
		return ""
	}

	if v.Function != nil {
		return v.Function.Name
	}

	return unquote(v.Literal)
}

// unquote strips the quotes participle leaves on String tokens.
func unquote(text string) string {
	if len(text) >= 2 {
		if quote := text[0]; (quote == '"' || quote == '\'') && text[len(text)-1] == quote {
			return strings.NewReplacer(`\`+string(quote), string(quote), `\\`, `\`).Replace(text[1 : len(text)-1])
		}
	}

	return text
}

func isEmpty(r Record, field string) bool {
	for _, value := range r.Field(field) {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}

	return true
}

// resolve expands a value to the strings it stands for. Functions we know
// nothing about, such as openSprints(), resolve to nothing and so match
// nothing.
func resolve(v *value, ctx Context) []string {
	if v.Function == nil {
		return []string{v.text()}
	}

	switch strings.ToLower(v.Function.Name) {
	case "currentuser":
		return ctx.CurrentUsers
	case "empty", "null":
		return []string{""}
	}

	return nil
}

func matchesAny(r Record, field string, values []*value, ctx Context, match func(actual, expected string) bool) bool {
	actual := r.Field(field)

	for _, v := range values {
		// EMPTY and NULL match a field with no value, as in
		// assignee in (EMPTY, currentUser()).
		if isEmptyValue(v) {
			if isEmpty(r, field) {
				return true
			}

			continue
		}

		for _, expected := range resolve(v, ctx) {
			for _, candidate := range actual {
				if match(candidate, expected) {
					return true
				}
			}
		}
	}

	return false
}

func equalsFold(actual, expected string) bool {
	return strings.EqualFold(strings.TrimSpace(actual), strings.TrimSpace(expected))
}

// containsWildcard implements `~`. Jira's is word-based; a substring match is
// friendlier for a typed search and is what Orca's `text ~ "foo*"` expects.
func containsWildcard(actual, expected string) bool {
	actual = strings.ToLower(strings.TrimSpace(actual))
	expected = strings.ToLower(strings.TrimSpace(strings.Trim(expected, "*")))

	return expected == "" || strings.Contains(actual, expected)
}

func compare(r Record, field, op string, values []*value, ctx Context) bool {
	if len(values) == 0 {
		return false
	}

	expected := resolve(values[0], ctx)
	if len(expected) == 0 {
		return false
	}

	expectedText := expected[0]

	ordered := func(cmp int) bool {
		switch op {
		case "<":
			return cmp < 0
		case "<=":
			return cmp <= 0
		case ">":
			return cmp > 0
		default:
			return cmp >= 0
		}
	}

	if actual, isTime := r.Timestamp(field); isTime {
		expected, parsed := parseMoment(expectedText, ctx)
		return parsed && ordered(actual.Compare(expected))
	}

	for _, candidate := range r.Field(field) {
		if ordered(strings.Compare(strings.ToLower(candidate), strings.ToLower(expectedText))) {
			return true
		}
	}

	return false
}

var relativeMoment = regexp.MustCompile(`^([+-]?\d+)([mhdw])$`)

var momentUnits = map[string]time.Duration{
	"m": time.Minute,
	"h": time.Hour,
	"d": 24 * time.Hour,
	"w": 7 * 24 * time.Hour,
}

var momentLayouts = []string{
	time.RFC3339,
	"2006-01-02 15:04",
	time.DateOnly,
	"2006/01/02",
}

// parseMoment accepts absolute dates and Jira's relative offsets, like -7d.
func parseMoment(text string, ctx Context) (time.Time, bool) {
	text = strings.TrimSpace(text)

	switch strings.ToLower(text) {
	case "":
		return time.Time{}, false
	case "now", "currentlogindate":
		return ctx.Now, true
	case "startofday":
		return startOfDay(ctx.Now), true
	case "startofweek":
		day := startOfDay(ctx.Now)
		return day.AddDate(0, 0, -int(day.Weekday())), true
	case "startofmonth":
		day := startOfDay(ctx.Now)
		return day.AddDate(0, 0, -day.Day()+1), true
	}

	if match := relativeMoment.FindStringSubmatch(strings.ToLower(text)); match != nil {
		amount, err := strconv.Atoi(match[1])
		if err != nil {
			return time.Time{}, false
		}

		return ctx.Now.Add(time.Duration(amount) * momentUnits[match[2]]), true
	}

	for _, layout := range momentLayouts {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed, true
		}
	}

	return time.Time{}, false
}

func startOfDay(t time.Time) time.Time {
	year, month, day := t.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, t.Location())
}

// FieldUpdated is the canonical name of the last-updated time, the default sort.
const FieldUpdated = "updated"

// CanonicalField folds Jira's field aliases onto the names a record answers to.
func CanonicalField(name string) string {
	name = strings.ToLower(strings.TrimSpace(unquote(name)))

	switch name {
	case "issuetype", "issue_type", "type":
		return "type"
	case "issuekey", "key", "issue":
		return "key"
	case "createddate", "created":
		return "created"
	case "updateddate", "lastupdated", FieldUpdated:
		return FieldUpdated
	case "title", "summary":
		return "summary"
	case "label", "labels":
		return "labels"
	default:
		return name
	}
}

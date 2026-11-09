package jira

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// The shapes below hold only the fields Orca's Jira mappers read.

// errorResponse is Jira's error envelope. Orca builds its message from
// errorMessages and errors, and falls back to the bare status text without them.
type errorResponse struct {
	ErrorMessages []string          `json:"errorMessages"`
	Errors        map[string]string `json:"errors"`
}

type userPayload struct {
	Name         string `json:"name"`
	Key          string `json:"key"`
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress,omitempty"`
	Active       bool   `json:"active"`
}

type statusCategoryPayload struct {
	ID        int    `json:"id"`
	Key       string `json:"key"`
	Name      string `json:"name"`
	ColorName string `json:"colorName"`
}

type statusPayload struct {
	ID             string                `json:"id"`
	Name           string                `json:"name"`
	StatusCategory statusCategoryPayload `json:"statusCategory"`
}

type projectPayload struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

type issueTypePayload struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Subtask     bool   `json:"subtask"`
}

type priorityPayload struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type resolutionPayload struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type issueFields struct {
	Summary     string             `json:"summary"`
	Description string             `json:"description,omitempty"`
	Project     projectPayload     `json:"project"`
	IssueType   issueTypePayload   `json:"issuetype"`
	Status      statusPayload      `json:"status"`
	Assignee    *userPayload       `json:"assignee"`
	Reporter    *userPayload       `json:"reporter"`
	Priority    *priorityPayload   `json:"priority"`
	Labels      []string           `json:"labels"`
	Created     string             `json:"created"`
	Updated     string             `json:"updated"`
	Resolution  *resolutionPayload `json:"resolution"`

	// Parent is the issue this one belongs to, such as an epic. Orca shows its
	// key, summary, and type.
	Parent *parentPayload `json:"parent,omitempty"`

	Attachment []attachmentPayload `json:"attachment"`
}

type parentPayload struct {
	ID     string       `json:"id"`
	Key    string       `json:"key"`
	Self   string       `json:"self"`
	Fields parentFields `json:"fields"`
}

type parentFields struct {
	Summary   string           `json:"summary"`
	Status    statusPayload    `json:"status"`
	IssueType issueTypePayload `json:"issuetype"`
}

type issuePayload struct {
	ID             string          `json:"id"`
	Key            string          `json:"key"`
	Self           string          `json:"self"`
	Fields         issueFields     `json:"fields"`
	RenderedFields *renderedFields `json:"renderedFields,omitempty"`
}

// renderedFields is the HTML Orca reads to find the attachments a description
// shows.
type renderedFields struct {
	Description string `json:"description"`
}

// attachmentPayload is an attachment. Orca downloads content for the images a
// rendered description links to.
type attachmentPayload struct {
	Self     string       `json:"self"`
	ID       string       `json:"id"`
	Filename string       `json:"filename"`
	Author   *userPayload `json:"author,omitempty"`
	Created  string       `json:"created"`
	Size     int64        `json:"size"`
	MimeType string       `json:"mimeType"`
	Content  string       `json:"content"`
}

type searchResponse struct {
	StartAt    int            `json:"startAt"`
	MaxResults int            `json:"maxResults"`
	Total      int            `json:"total"`
	IsLast     bool           `json:"isLast"`
	Issues     []issuePayload `json:"issues"`
}

type commentPayload struct {
	ID      string       `json:"id"`
	Body    string       `json:"body"`
	Author  *userPayload `json:"author,omitempty"`
	Created string       `json:"created"`
	Updated string       `json:"updated"`
}

// pagedResponse always reports isLast, so Orca's paging loop stops after one
// request.
type pagedResponse[T any] struct {
	StartAt    int  `json:"startAt"`
	MaxResults int  `json:"maxResults"`
	Total      int  `json:"total"`
	IsLast     bool `json:"isLast"`
	Values     []T  `json:"values,omitempty"`
	IssueTypes []T  `json:"issueTypes,omitempty"`
	Comments   []T  `json:"comments,omitempty"`
}

func onePage[T any](items []T) pagedResponse[T] {
	return pagedResponse[T]{MaxResults: len(items), Total: len(items), IsLast: true}
}

type transitionPayload struct {
	ID   string        `json:"id"`
	Name string        `json:"name"`
	To   statusPayload `json:"to"`
}

// createField is Jira's createmeta field descriptor.
type createField struct {
	FieldID       string             `json:"fieldId"`
	Key           string             `json:"key"`
	Name          string             `json:"name"`
	Required      bool               `json:"required"`
	Schema        createFieldSchema  `json:"schema"`
	AllowedValues []createFieldValue `json:"allowedValues,omitempty"`
}

type createFieldSchema struct {
	Type  string `json:"type"`
	Items string `json:"items,omitempty"`
}

type createFieldValue struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

type filterPayload struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	JQL  string `json:"jql"`
}

type boardPayload struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type boardColumn struct {
	Name     string        `json:"name"`
	Statuses []boardStatus `json:"statuses"`
}

type boardStatus struct {
	ID string `json:"id"`
}

type boardConfiguration struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	ColumnConfig struct {
		Columns []boardColumn `json:"columns"`
	} `json:"columnConfig"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	if payload == nil {
		w.WriteHeader(status)
		return
	}

	w.Header().Set("Content-Type", "application/json;charset=UTF-8")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("writing a response", "error", err)
	}
}

// writeError never sends a 401. Orca reads a 401 as proof the stored token is
// bad and deletes the whole site from its settings, so an ordinary mistake
// here would quietly disconnect it.
func writeError(w http.ResponseWriter, status int, message string) {
	if status == http.StatusUnauthorized {
		status = http.StatusForbidden
	}

	// errors is an object, never null, as Jira sends it; a client that reads it
	// with Object.entries would throw on null.
	writeJSON(w, status, errorResponse{ErrorMessages: []string{message}, Errors: map[string]string{}})
}

// decodeString reads a JSON string, accepting the ADF document a Cloud client
// sends in its place.
func decodeString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}

	var document struct {
		Content []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return ""
	}

	paragraphs := make([]string, 0, len(document.Content))

	for _, block := range document.Content {
		var line strings.Builder
		for _, span := range block.Content {
			line.WriteString(span.Text)
		}

		paragraphs = append(paragraphs, line.String())
	}

	return strings.Join(paragraphs, "\n\n")
}

// decodeReference reads one of Jira's {id|key|name|value} reference objects,
// or a bare string. JSON null reads as empty.
func decodeReference(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}

	var reference struct {
		ID        string `json:"id"`
		Key       string `json:"key"`
		Name      string `json:"name"`
		Value     string `json:"value"`
		AccountID string `json:"accountId"`
	}
	if err := json.Unmarshal(raw, &reference); err != nil {
		return ""
	}

	return firstNonEmpty(reference.Name, reference.Key, reference.ID, reference.Value, reference.AccountID)
}

func decodeStrings(raw json.RawMessage) []string {
	var values []string
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &values)
	}

	return values
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}

	return ""
}

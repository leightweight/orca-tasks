package jira

import (
	"strings"
	"testing"
)

func TestRenderMarkdownDropsUnsafeContent(t *testing.T) {
	t.Parallel()

	got := renderMarkdown("Use **the** vault.\n\n<script>alert(1)</script>\n\n[click](javascript:alert(2)) <img src=x onerror=alert(3)>")

	if !strings.Contains(got, "<strong>the</strong>") {
		t.Errorf("Markdown wasn't rendered: %q", got)
	}

	for _, unsafe := range []string{"<script", "javascript:", "onerror"} {
		if strings.Contains(got, unsafe) {
			t.Errorf("rendered %q, which contains %q", got, unsafe)
		}
	}
}

package client

import (
	"net/http"
	"testing"
)

func TestUploadsHaveNoOverallDeadline(t *testing.T) {
	t.Parallel()

	for name, c := range map[string]*Client{"url": New("http://localhost", "otk_x"), "socket": NewSocket("/tmp/agent.sock")} {
		transport, _ := c.Uploads.Transport.(*http.Transport)

		if c.Uploads.Timeout != 0 || c.HTTP.Timeout != callTimeout || transport == nil || transport.ResponseHeaderTimeout != responseTimeout {
			t.Errorf("%s client: uploads timeout %v, calls timeout %v, response wait %v", name, c.Uploads.Timeout, c.HTTP.Timeout, transport)
		}
	}
}

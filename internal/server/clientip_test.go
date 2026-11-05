package server

import (
	"context"
	"net/http"
	"testing"
)

func TestClientIPs(t *testing.T) {
	t.Parallel()

	ips, err := NewClientIPs("private, 203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name, remote, forwarded, want string
	}{
		{"direct client", "198.51.100.1:5000", "", "198.51.100.1"},
		{"direct client spoofing", "198.51.100.1:5000", "1.2.3.4", "198.51.100.1"},
		{"behind an ingress", "10.0.0.5:5000", "198.51.100.1", "198.51.100.1"},
		{"behind two proxies", "10.0.0.5:5000", "1.2.3.4, 198.51.100.1, 203.0.113.7", "198.51.100.1"},
		{"loopback", "127.0.0.1:5000", "198.51.100.1", "198.51.100.1"},
		{"proxy without the header", "10.0.0.5:5000", "", "10.0.0.5"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			r, err := http.NewRequestWithContext(context.Background(), "GET", "/", nil)
			if err != nil {
				t.Fatal(err)
			}

			r.RemoteAddr = test.remote
			if test.forwarded != "" {
				r.Header.Set("X-Forwarded-For", test.forwarded)
			}

			if got := ips.Of(r); got != test.want {
				t.Errorf("Of = %q, want %q", got, test.want)
			}
		})
	}

	if _, err := NewClientIPs("not-an-address"); err == nil {
		t.Error("a bad trusted proxy was accepted")
	}
}

func TestLimiter(t *testing.T) {
	t.Parallel()

	l, err := NewLimiter()
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()

	for range maxFailures {
		if !l.Allowed(ctx, "ip:a", "user:a") {
			t.Fatal("blocked before the limit")
		}

		l.Failed(ctx, "ip:a")
	}

	if l.Allowed(ctx, "user:b", "ip:a") {
		t.Error("allowed past the limit")
	}

	if !l.Allowed(ctx, "ip:b") {
		t.Error("another IP was blocked")
	}
}

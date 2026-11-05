package attachments

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
)

var pngHeader = "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR"

func TestReceiveAndKeep(t *testing.T) {
	t.Parallel()

	d, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	u, err := d.Receive(strings.NewReader(pngHeader), "text/html", 100)
	if err != nil {
		t.Fatal(err)
	}

	if u.MIMEType != "image/png" || u.Size != int64(len(pngHeader)) {
		t.Errorf("upload = %s, %d bytes; want image/png", u.MIMEType, u.Size)
	}

	if err := d.Keep(u, 42); err != nil {
		t.Fatal(err)
	}

	f, err := d.Open(42)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()

	if data, _ := io.ReadAll(f); string(data) != pngHeader {
		t.Error("the kept file has different contents")
	}

	if _, err := d.Receive(strings.NewReader(strings.Repeat("x", 101)), "", 100); !errors.Is(err, ErrTooLarge) {
		t.Errorf("an oversized upload: %v", err)
	}
}

func TestMIMEType(t *testing.T) {
	t.Parallel()

	tests := []struct{ head, declared, want string }{
		{pngHeader, "", "image/png"},
		{"<svg xmlns='http://www.w3.org/2000/svg'></svg>", "image/svg+xml", "image/svg+xml"},
		{"<html><script>alert(1)</script></html>", "image/png", "text/html"},
		{"plain words", "application/pdf; charset=binary", "application/pdf"},
	}

	for _, test := range tests {
		if got := mimeType([]byte(test.head), test.declared); got != test.want {
			t.Errorf("mimeType(%.10q, %q) = %q, want %q", test.head, test.declared, got, test.want)
		}
	}
}

func TestSweep(t *testing.T) {
	t.Parallel()

	d, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	for _, id := range []int64{1, 2} {
		u, err := d.Receive(strings.NewReader("x"), "", 10)
		if err != nil {
			t.Fatal(err)
		}

		if err := d.Keep(u, id); err != nil {
			t.Fatal(err)
		}
	}

	onlyOne := func(context.Context, []int64) ([]int64, error) { return []int64{1}, nil }
	if err := d.Sweep(context.Background(), onlyOne, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}

	if _, err := d.Open(2); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the orphan is still there: %v", err)
	}

	if _, err := d.Open(1); err != nil {
		t.Errorf("the kept file is gone: %v", err)
	}
}

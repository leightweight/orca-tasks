// Package attachments keeps attachment contents as files in a directory, one
// per attachment, named by its ID. Their metadata is in the database.
package attachments

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Dir is the attachment directory.
type Dir struct {
	path string
	tmp  string
}

// Open prepares a directory, creating it if it's missing.
func Open(path string) (*Dir, error) {
	d := &Dir{path: path, tmp: filepath.Join(path, "tmp")}
	if err := os.MkdirAll(d.tmp, 0o750); err != nil {
		return nil, fmt.Errorf("creating the attachment directory: %w", err)
	}

	return d, nil
}

// Writable reports whether the directory accepts files, for readiness checks.
func (d *Dir) Writable() error {
	f, err := os.CreateTemp(d.tmp, "ready-")
	if err != nil {
		return err
	}

	_ = f.Close()

	return os.Remove(f.Name())
}

// Upload is a file written to the temporary directory, waiting for its
// database row before it's moved into place.
type Upload struct {
	path     string
	Size     int64
	SHA256   []byte
	MIMEType string
}

// ErrTooLarge means an upload was over the limit.
var ErrTooLarge = errors.New("the file is over the upload limit")

// Receive writes an upload to a temporary file, measuring and hashing it and
// sniffing its type. It fails with ErrTooLarge past max bytes.
func (d *Dir) Receive(r io.Reader, declaredType string, maxBytes int64) (*Upload, error) {
	f, err := os.CreateTemp(d.tmp, "upload-")
	if err != nil {
		return nil, err
	}

	upload := &Upload{path: f.Name()}
	ok := false

	defer func() {
		_ = f.Close()

		if !ok {
			_ = os.Remove(upload.path)
		}
	}()

	hash := sha256.New()
	head := &prefix{limit: 512}

	n, err := io.Copy(io.MultiWriter(f, hash, head), io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}

	if n > maxBytes {
		return nil, ErrTooLarge
	}

	if err := f.Close(); err != nil {
		return nil, err
	}

	upload.Size = n
	upload.SHA256 = hash.Sum(nil)
	upload.MIMEType = mimeType(head.data, declaredType)
	ok = true

	return upload, nil
}

// Discard removes an upload that won't be kept.
func (d *Dir) Discard(u *Upload) { _ = os.Remove(u.path) }

// Keep moves an upload into place under its attachment ID.
func (d *Dir) Keep(u *Upload, id int64) error {
	return os.Rename(u.path, d.file(id))
}

// Open opens an attachment's contents.
func (d *Dir) Open(id int64) (*os.File, error) {
	return os.Open(d.file(id))
}

// Remove deletes an attachment's contents. A missing file isn't an error.
func (d *Dir) Remove(id int64) error {
	if err := os.Remove(d.file(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return nil
}

func (d *Dir) file(id int64) string {
	return filepath.Join(d.path, strconv.FormatInt(id, 10))
}

// Sweep deletes files with no database row, which deleting a project or a
// failed upload leaves behind, and temporary files older than a day. existing
// reports which of a batch of IDs still have rows.
func (d *Dir) Sweep(ctx context.Context, existing func(ctx context.Context, ids []int64) ([]int64, error), log *slog.Logger) error {
	entries, err := os.ReadDir(d.path)
	if err != nil {
		return err
	}

	ids := make([]int64, 0, len(entries))

	for _, e := range entries {
		if id, err := strconv.ParseInt(e.Name(), 10, 64); err == nil && !e.IsDir() {
			ids = append(ids, id)
		}
	}

	const batch = 1000

	for start := 0; start < len(ids); start += batch {
		chunk := ids[start:min(start+batch, len(ids))]

		kept, err := existing(ctx, chunk)
		if err != nil {
			return err
		}

		keep := make(map[int64]bool, len(kept))
		for _, id := range kept {
			keep[id] = true
		}

		for _, id := range chunk {
			if !keep[id] {
				log.Info("removing an orphaned attachment file", "id", id)

				if err := d.Remove(id); err != nil {
					return err
				}
			}
		}
	}

	return d.sweepTemporary(time.Now().Add(-24 * time.Hour))
}

func (d *Dir) sweepTemporary(before time.Time) error {
	entries, err := os.ReadDir(d.tmp)
	if err != nil {
		return err
	}

	for _, e := range entries {
		info, err := e.Info()
		if err == nil && info.ModTime().Before(before) {
			_ = os.Remove(filepath.Join(d.tmp, e.Name()))
		}
	}

	return nil
}

// InlineTypes are the image types served inline. Anything else downloads, so
// an uploaded HTML or SVG file can't run script on the server's origin.
var InlineTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// mimeType trusts the content over the declared type for images, so a file
// can't claim to be a PNG to be shown inline. Other files keep their declared
// type, since sniffing can't tell most document types apart.
func mimeType(head []byte, declared string) string {
	sniffed := http.DetectContentType(head)
	if InlineTypes[sniffed] {
		return sniffed
	}

	declared = strings.TrimSpace(strings.Split(declared, ";")[0])
	if declared == "" || InlineTypes[declared] {
		return strings.Split(sniffed, ";")[0]
	}

	return declared
}

// prefix keeps the first bytes written to it.
type prefix struct {
	limit int
	data  []byte
}

func (p *prefix) Write(b []byte) (int, error) {
	if room := p.limit - len(p.data); room > 0 {
		p.data = append(p.data, b[:min(room, len(b))]...)
	}

	return len(b), nil
}

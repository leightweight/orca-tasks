package attachments

import (
	"context"
	"io"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/leightweight/orca-tasks/internal/store"
	"github.com/leightweight/orca-tasks/internal/store/db"
)

// Save stores an uploaded file and records it on an issue as uploaded by the
// actor's membership. The file goes to a temporary name first and is renamed
// into place once the row commits, so a failure leaves neither behind. A file
// over maxBytes fails with ErrTooLarge.
func (d *Dir) Save(ctx context.Context, s *store.Store, actor int64, issue db.Issue, filename string, r io.Reader, declaredType string,
	maxBytes int64,
) (db.Attachment, error) {
	upload, err := d.Receive(r, declaredType, maxBytes)
	if err != nil {
		return db.Attachment{}, err
	}

	attachment, err := s.AddAttachment(ctx, actor, issue, db.InsertAttachmentParams{
		Filename: CleanFilename(filename),
		MimeType: upload.MIMEType,
		Size:     upload.Size,
		Sha256:   upload.SHA256,
	})
	if err != nil {
		d.Discard(upload)
		return db.Attachment{}, err
	}

	if err := d.Keep(upload, attachment.ID); err != nil {
		d.Discard(upload)
		// The sweep would remove the row's missing file anyway; the row goes
		// now so the issue doesn't list an attachment that isn't there.
		_ = s.DeleteAttachment(ctx, attachment.ID)

		return db.Attachment{}, err
	}

	return attachment, nil
}

// MaxFilename is the longest filename kept, in bytes.
const MaxFilename = 255

// CleanFilename keeps the last path element and drops control characters, so
// a filename is safe to show and to put in a header.
func CleanFilename(name string) string {
	name = path.Base(strings.ReplaceAll(strings.ToValidUTF8(name, ""), `\`, "/"))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '"' {
			return -1
		}

		return r
	}, name)

	if name == "" || name == "." || name == "/" {
		name = "attachment"
	}

	// Keep the last 255 bytes, which hold the extension, starting on a
	// character so a multibyte one isn't split.
	if len(name) > MaxFilename {
		start := len(name) - MaxFilename
		for start < len(name) && !utf8.RuneStart(name[start]) {
			start++
		}

		name = name[start:]
	}

	return name
}

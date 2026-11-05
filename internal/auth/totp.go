package auth

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"image/png"
	"strings"
	"time"

	"github.com/pquerna/otp"
	"github.com/pquerna/otp/totp"
)

// totpPeriod is the length of one TOTP step.
const totpPeriod = 30

// Enrollment is a new TOTP secret and what the user scans to add it.
type Enrollment struct {
	Secret string
	URL    string

	// QRCode is a PNG data URL for an <img> tag.
	QRCode string
}

// NewEnrollment generates a TOTP secret for an account.
func NewEnrollment(issuer, account string) (*Enrollment, error) {
	key, err := totp.Generate(totp.GenerateOpts{Issuer: issuer, AccountName: account})
	if err != nil {
		return nil, err
	}

	return EnrollmentFor(key.Secret(), key.URL())
}

// EnrollmentFor rebuilds an enrollment for a secret already generated, so the
// QR code can be shown again after a failed confirmation.
func EnrollmentFor(secret, url string) (*Enrollment, error) {
	key, err := otp.NewKeyFromURL(url)
	if err != nil {
		return nil, err
	}

	image, err := key.Image(200, 200)
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, image); err != nil {
		return nil, err
	}

	return &Enrollment{
		Secret: secret,
		URL:    url,
		QRCode: "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()),
	}, nil
}

// CheckTOTP validates a code against a secret at a time. It accepts the step
// before and after now for clock drift, but only a step later than lastStep,
// so a code can't be used twice. It returns the step to record.
func CheckTOTP(secret, code string, now time.Time, lastStep int64) (int64, bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != 6 {
		return 0, false
	}

	current := now.Unix() / totpPeriod

	for _, step := range []int64{current - 1, current, current + 1} {
		if step <= lastStep {
			continue
		}

		expected, err := totp.GenerateCodeCustom(secret, time.Unix(step*totpPeriod, 0), totp.ValidateOpts{
			Period:    totpPeriod,
			Digits:    otp.DigitsSix,
			Algorithm: otp.AlgorithmSHA1,
		})
		if err != nil {
			return 0, false
		}

		if subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return step, true
		}
	}

	return 0, false
}

// TOTPCode returns the code for a secret at a time. Tests use it to sign in.
func TOTPCode(secret string, at time.Time) string {
	code, err := totp.GenerateCode(secret, at)
	if err != nil {
		panic(fmt.Sprintf("generating a TOTP code: %v", err))
	}

	return code
}

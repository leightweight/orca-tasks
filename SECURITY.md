# Security policy

## Reporting a vulnerability

Report vulnerabilities through GitHub's [private vulnerability reporting](https://github.com/leightweight/orca-tasks/security/advisories/new), not in public issues.
Include the version, how to reproduce the problem, and its impact.

## Supported versions

Only the latest release gets security fixes.

## Security model

- Every user signs in with a password, hashed with Argon2id, and a TOTP code.
  Nobody can turn TOTP off.
- TOTP secrets are encrypted with the server's secret key.
  API keys, recovery codes, and reset links are stored as SHA-256 hashes.
- Each principal sees only the projects it's a member of.
  Site administrators see counts across projects, but read issues only through their own memberships.
- API keys can be read-only or limited to some projects, and they work only while their user is enabled.
- Failed sign-ins and API key checks are limited per client address and per username.
  The client address comes from `X-Forwarded-For` only when the connection is from a trusted proxy.
- The web interface rejects cross-origin form posts and sends a strict content security policy.
  API routes don't accept session cookies, apart from attachment downloads.
- Attachments other than PNG, JPEG, GIF, and WebP images download rather than display, so an uploaded HTML or SVG file can't run script on the server's origin.
- The release workflow signs the image and the Helm chart with cosign.

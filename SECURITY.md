# Security Policy

## Supported Versions

Security fixes are applied to the latest released framy version. Before
`v1.0.0`, upgrading to the newest pre-release or stable tag may be required.

## Reporting A Vulnerability

Please do not open a public issue for a suspected vulnerability or include
API keys, credentials, database dumps, Telegram identifiers, tokens, generated
files of other people, or production logs in an issue.

framy holds people's provider keys and spends money on their behalf. Each of
these counts as a vulnerability:

- a way to read a stored key, or more of it than its last four characters;
- a way to make a run on somebody else's key or connection;
- a way to open another person's private input or result;
- a way to spend the free allowance past its limits, or framy's own key outside
  the free allowance.

Use GitHub's private vulnerability reporting for `FreshLabDev/framy`. If it is
not available, contact an Asterfield maintainer privately through an existing
trusted channel and include only the minimum reproduction details needed to
investigate.

We will acknowledge a report, assess affected versions, coordinate a fix, and
publish an advisory when disclosure is safe.

## Operator Responsibilities

- Keep `.env`, file secrets, PostgreSQL dumps and the R2 credentials outside
  Git.
- Keep the key-encryption key (KEK) apart from the database backups. A dump and
  its KEK together give away every stored key.
- Use a unique strong password for the `framy_core` role.
- Give the R2 API token access to framy's two buckets only. Keep the bucket
  `framy` private; files are served through presigned links.
- Do not publish the PostgreSQL port or the service port to the internet. The
  service is reached only through the Cloudflare Tunnel.

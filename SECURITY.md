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

Report it privately through GitHub:
[Security → Report a vulnerability](https://github.com/FreshLabDev/framy/security/advisories/new)
on `FreshLabDev/framy`. Only you and the maintainers see the report. If the
form does not open for you, open an issue titled "Security contact" with no
details in it, and a maintainer will reach you privately. Include only the
minimum reproduction details needed to investigate.

We will acknowledge a report, assess affected versions, coordinate a fix, and
publish an advisory when disclosure is safe.

## Operator Responsibilities

- Keep `.env`, file secrets, PostgreSQL dumps and the R2 credentials outside
  Git.
- Keep the key-encryption key (KEK) apart from the database backups. A dump and
  its KEK together give away every stored key.
- Use a unique strong password for the `framy_core` role.
- Give the R2 API token access to the bucket `framy` alone, and the clips'
  token, when `R2_INCOMING_BUCKET` is set, to that bucket alone. A token that
  also reaches a bucket of backups can delete the data and its copy together.
  Keep the bucket `framy` private; files are served through presigned links.
- Do not publish the PostgreSQL port or the service port to the internet. The
  service is reached only through the Cloudflare Tunnel.

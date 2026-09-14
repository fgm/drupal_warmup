# Security Policy

## Supported Versions

Fixes go into the latest release only.
Until 1.0 that means the newest `v0.x` tag and the binaries published with it:
there are no maintenance branches, so an earlier release is superseded, never patched.

| Version               |     Supported      |
|-----------------------|:------------------:|
| latest `v0.x` release | :white_check_mark: |
| earlier releases      |        :x:         |
| `main` at HEAD        |        :x:         |

## Verifying a release

Release binaries are built on GitHub Actions by
[this repository's release workflow](https://github.com/fgm/drupal_warmup/actions/workflows/release.yml)
and signed through [Sigstore](https://www.sigstore.dev)
against that workflow's OIDC identity.

**There is no signing key to obtain, and none to steal.**
Signing uses a short-lived certificate bound to the workflow identity,
rather than a long-lived private key,
so no secret is held on the release machine or anywhere else.
What the signature establishes is the repository, workflow and commit that produced the artifact,
recorded in Sigstore's public transparency log.

To verify a download, with the [GitHub CLI](https://cli.github.com):

```console
$ gh attestation verify drupal_warmup_0.1.0_linux_amd64.tar.gz --owner fgm
```

It fails if the archive was altered,
or was not produced by this repository's workflow.

`checksums.txt`, published beside the archives, is not a substitute:
it detects a corrupted download but not a substituted one,
because it is a list, and the attestation is what says who wrote the list.

Each release also carries one SPDX SBOM per archive,
generated from the binary's own build graph rather than from `go.mod`.

## Dependencies

Dependencies are chosen conservatively.
The Go standard library and `golang.org/x/...` are used without review;
any other module is vetted before it is added,
so the runtime graph stays small (see `go.mod`).

They are obtained through Go modules.
`go.sum` records a cryptographic checksum for every module,
the `go` toolchain verifies it on each build,
and the release pipeline runs `go mod verify` before building.

They are tracked in `go.mod` and `go.sum`, both committed.
[Dependabot](.github/dependabot.yml) proposes updates on a schedule,
and the version floors are kept at the versions actually in use.
The SBOM above lists what a given release actually shipped.

## Credentials

The warmer sends whatever credentials it is given
to whatever `--base` names, with every request.

- `--base` must be the host you mean.
  There is no allow-list beyond it:
  an enumerated URL on any other host is refused,
  which is also why `--lax` never lifts that refusal.
- `--bapass` and `--drpass` on the command line are visible to `ps`
  and land in shell history.
  Set `DRUPAL_WARMUP_BAPASS` and `DRUPAL_WARMUP_DRPASS` instead;
  the flag wins where both are present.
- An `http://` base, or an `http://` sitemap entry fetched under `--lax`,
  sends those credentials in clear.
- The Xdebug trigger of the `debug` command is not a secret,
  but it does ask the server to start a debugger session:
  only point it at a site you control.

## Security assessment

The most likely and impactful problems for a tool that fetches URLs with credentials,
and how the design addresses each:

- **Credential disclosure through the process table or shell history.** The most likely.
  `--bapass` and `--drpass` on the command line are visible to `ps` and land in shell history.
  Addressed by the `DRUPAL_WARMUP_BAPASS` and `DRUPAL_WARMUP_DRPASS`
  environment variables and the documented use of a `.env` file.
- **Credentials sent to an unintended host.**
  A sitemap or list could name URLs on another host,
  and sending the login or basic auth there would leak it.
  Addressed by refusing every enumerated URL that is not under `--base`,
  a refusal `--lax` does not lift.
- **Cleartext transmission.**
  An `http://` base, or an out-of-scope entry fetched under `--lax --follow`,
  sends credentials unencrypted.
  Addressed by documentation: the operator chooses `https`.
- **Sensitive data reaching logs.**
  Session cookies or authorization headers could be logged.
  Addressed by logging cookie names only, never their values,
  enforced by CodeQL's clear-text-logging check in CI.
- **Supply-chain compromise.**
  A malicious dependency or GitHub Action.
  Addressed by a small dependency graph verified through `go.sum`
  and `go mod verify`, SHA-pinned actions, Dependabot, OpenSSF Scorecard,
  and Sigstore-signed release attestations.
- **Unintended debugger activation.**
  The `debug` command's trigger cookie starts an Xdebug session on the origin.
  Addressed by documenting it as a tool for a site you control.

## Reporting a Vulnerability

To report a vulnerability:

- please do NOT use the issue system on this repo
- use the contact form on https://osinet.fr/contact
- the first response on that form should be within 1 day Monday to Friday

If a vulnerability is:

- accepted: we can work together on a fix, and you will be credited (unless you prefer not to be) on the fix
- considered not to be an actual security issue: you will get a suggestion to open it,
  as an issue on the github issue system for the repo
- rejected: as you prefer, it can be either reported as an issue,
  and closed with an explanation about why it is not an actual issue; or remain private.
  The former is usually better.

## Published advisories

Once a reported vulnerability is fixed, it is published as a
[GitHub Security Advisory](https://github.com/fgm/drupal_warmup/security/advisories)
on this repository.
The advisory records the affected versions, the fix, and the reporter's credit,
and feeds the public GitHub Advisory Database and OSV.
The fix is also listed under a `Security` heading in the
[CHANGELOG](CHANGELOG.md).

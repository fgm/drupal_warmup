# Security Policy

## Supported Versions

Fixes go into the latest release only.
Until 1.0 that means the newest `v0.x` tag and the binaries published with it:
there are no maintenance branches,
so an earlier release is superseded, never patched.

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

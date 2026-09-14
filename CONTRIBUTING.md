# Contributing to drupal_warmup

Contributions are welcome as issues and pull requests.
The maintainer, named below, reviews and decides on them.

## Reporting

- Bugs and feature requests: open a GitHub issue.
- Security vulnerabilities: do not open an issue or a pull request.
  Follow [SECURITY.md](SECURITY.md).

## Making a change

The default branch is protected, so every change lands through a pull request.

1. Fork the repository, or branch it if you have write access.
2. Make the change, with tests.
3. Run `make`. It must pass.
4. Open a pull request against `main`.

## Requirements for acceptance

A contribution is accepted when it meets all of these:

- **It builds and its tests pass** under `make`,
  which runs the modernizer (`go fix -diff`) and `staticcheck -checks=all` for linting,
  the test suite under the race detector, and the build.
- **New behaviour comes with tests.**
  Coverage is kept at 100% of the internal packages;
  a change that lowers it needs a stated reason.
- **The code is `gofmt`-clean** and passes `staticcheck -checks=all`.
- **Commit messages follow Conventional Commits**
  (`feat:`, `fix:`, `chore:`, `test:`, and so on),
  and state intent rather than restate the diff.
- **The contribution is licensed under GPL-3.0**, the project licence.
  Opening a pull request means your work is offered under it.

The maintainer may request changes, or decline a contribution
that falls outside the project's scope.

## Maintainers

drupal_warmup is maintained by Frédéric G. MARAND
([@fgm](https://github.com/fgm)),
currently the sole member with access to sensitive resources:
repository administration, the CI/CD pipelines, and release signing.

The one role, maintainer, is responsible for the project as a whole:
reviewing and merging every change through a pull request,
cutting and signing releases,
triaging issues and security reports,
and keeping dependencies current.

# Contributing

## Development

```bash
go test -race ./...
go vet ./...
```

The GitHub client is tested against a local fake of the API, so tests need no
network or token. To try a change for real, point `--config` at a scratch
repos file and set `GH_TOKEN` (or be logged in with `gh`).

## Changelog

Every user-visible change needs a changelog fragment created with
[changie](https://changie.dev):

```bash
go install github.com/miniscruff/changie@latest
changie new
```

Commit the generated `.changes/unreleased/*.yaml` with your change. CI fails
pull requests without a fragment unless they carry the `skip-changelog` label.
Dependabot PRs get a `Dependencies` fragment (a patch bump) pushed to their
branch automatically by `.github/workflows/dependabot-changie.yml`.
Do not edit `CHANGELOG.md` by hand; changie generates it.

## Releasing

Releases are cut from `main`:

```bash
changie batch auto     # or: changie batch v1.2.3
changie merge
git add .changes CHANGELOG.md
git commit -m "Release $(changie latest)"
git tag "$(changie latest)"
git push origin main "$(changie latest)"
```

The tag triggers `.github/workflows/release.yml`, which runs GoReleaser with
`.changes/<version>.md` as the release notes, publishes binaries to GitHub
Releases, and updates the Homebrew cask in `moexx399/homebrew-tap`.

One-time setup: create the `moexx399/homebrew-tap` repository and add a
`HOMEBREW_TAP_GITHUB_TOKEN` secret (a fine-grained token with contents
read/write on that repository) to this repository.

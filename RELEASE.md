# Releasing bserver

## How Releases Work

When you push a Git tag starting with `v`, the GitHub Actions release workflow
automatically:

1. Runs `go vet` and `go test` to verify the build
2. Cross-compiles binaries for four platforms:
   - `linux/amd64`
   - `linux/arm64`
   - `darwin/amd64` (Intel Mac)
   - `darwin/arm64` (Apple Silicon Mac)
3. Creates a GitHub Release with the binaries attached
4. Generates release notes from commits since the last tag

The version string (e.g., `v1.0.0`) is stamped into the binary via
`-ldflags "-X main.Version=..."`, so `bserver -version` reports the
release version.

## Creating a Release

1. Make sure all changes are committed and pushed to `main`:

   ```sh
   git status          # should be clean
   git push origin main
   ```

2. Tag the release with a semantic version:

   ```sh
   git tag v1.0.0
   git push origin v1.0.0
   ```

3. The release workflow runs automatically. Check progress at:

   `https://github.com/stgnet/bserver/actions/workflows/release.yml`

4. Once complete, the release appears at:

   `https://github.com/stgnet/bserver/releases`

## Version Numbering

Use [semantic versioning](https://semver.org/) — `vMAJOR.MINOR.PATCH`:

- **MAJOR** — breaking changes (config format, removed flags, changed defaults)
- **MINOR** — new features (new flags, new YAML capabilities)
- **PATCH** — bug fixes, documentation updates

## Local Builds with Version

The Makefile stamps the version from `git describe`:

```sh
make build
./bserver -version    # e.g., "bserver v1.0.0" or "bserver v1.0.0-3-gabcdef"
```

To set a specific version manually:

```sh
go build -ldflags "-X main.Version=v1.0.0" -o bserver
```

## Pre-release / Release Candidates

Tag with a pre-release suffix. The workflow handles these the same way:

```sh
git tag v1.1.0-rc1
git push origin v1.1.0-rc1
```

## Deleting a Release

If something went wrong:

```sh
# Delete the GitHub release
gh release delete v1.0.0 --yes

# Delete the remote tag
git push origin --delete v1.0.0

# Delete the local tag
git tag -d v1.0.0
```

Then fix the issue, re-tag, and push again.

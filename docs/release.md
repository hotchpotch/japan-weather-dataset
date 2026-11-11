# Release

A release is a tag `vX.Y.Z`. `.github/workflows/release.yml` then runs the
tests, builds the image `ghcr.io/hotchpotch/japan-weather-dataset:vX.Y.Z`,
pushes it with the workflow's short-lived `GITHUB_TOKEN` and creates a
GitHub Release whose notes come from `docs/releases/`. It also moves the
image tag `latest` to the new version. The update workflows (`month.yml`,
`monthly.yml`) run `:latest` (`JWD_IMAGE`), so a release takes effect with
their next run. A published version is never replaced, and a version tag is
never moved or reused; only `latest` moves.

## Release notes

- `docs/releases/HEAD.md` is the draft for the next release. When a change
  affects the dataset, the updates or the operation, add a short entry to it
  in the same commit.
- `docs/releases/vX.Y.Z.md` is the final text of a release.
- `scripts/release-notes.sh vX.Y.Z` prints the notes: the draft when it has
  entries, otherwise the file of the tag.

## Checks before a release

```sh
gofmt -l cmd internal          # no output
go vet ./...
go test ./...
go test -race ./...
for f in scripts/*.sh; do bash -n "$f"; done
docker build --build-arg VERSION=X.Y.Z -t jwd:test .
docker run --rm jwd:test jwd version   # X.Y.Z
docker run --rm jwd:test hf version
```

CI (`.github/workflows/ci.yml`) runs the same on every push to `main` and
on pull requests.

## Steps

1. Set `version` in `cmd/jwd/main.go` to `X.Y.Z` (it is in the User-Agent
   sent to JMA).
2. Move the entries of `docs/releases/HEAD.md` to `docs/releases/vX.Y.Z.md`
   (heading `# vX.Y.Z`) and reset `HEAD.md` to `# HEAD`.
3. Commit ("Prepare X.Y.Z release"), push `main` (or merge a pull request)
   and wait for CI.
4. Tag the commit that CI passed on, and push the tag:

   ```sh
   git fetch origin main
   git show --stat origin/main
   git tag -a vX.Y.Z origin/main -m "Release vX.Y.Z"
   git push origin vX.Y.Z
   ```

   The workflow fails without changing anything when the tag does not
   match `version`, `docs/releases/vX.Y.Z.md` is missing or the image
   exists already.
5. Check that `:latest` points to the new version
   (`docker run --rm ghcr.io/hotchpotch/japan-weather-dataset:latest jwd version`,
   or the package page). The workflows use it from their next run.
6. Run the workflows by hand and check the uploads:
   `gh workflow run month`, and `gh workflow run monthly -f dry_run=true`
   (a real monthly run refetches about two months; run it only when that
   is intended).

## Rolling back

If a release breaks the updates, point `latest` back to the previous
version (a version tag is never changed):

```sh
docker buildx imagetools create \
  -t ghcr.io/hotchpotch/japan-weather-dataset:latest \
  ghcr.io/hotchpotch/japan-weather-dataset:vX.Y.Z
```

(with `docker login ghcr.io` and a token that can write packages), or set
`JWD_IMAGE` in the workflows to `:vX.Y.Z` until a fixed release.

## Image access

The image is private while the repository is. The workflows pull it with
their `GITHUB_TOKEN` (`packages: read`). Another machine needs
`docker login ghcr.io` with a token that can read packages: a classic
token with only `read:packages` (fine-grained tokens cannot read GitHub
Packages). Once the repository is public, make the package public in its
settings and the login is not needed any more.

## Pinned dependencies

- Workflows reference actions by an exact release tag;
  `.github/workflows/actions.lock` (GitHub's workflow lockfile, generated
  by `gh actions-lock`) pins each of them, and the actions they use in
  turn, to a commit, and GitHub runs only the locked commits, even when a
  tag is moved. After changing an action, run `gh actions-lock` and commit
  the lockfile; `verify-lockfile.yml` fails otherwise. Install the extension with
  `gh extension install github/gh-actions-lock --pin v0.4.0`.
- Base images in the `Dockerfile` are pinned by digest; the `hf` CLI by
  version and date (`uv tool install --exclude-newer`, also in the scripts
  and README). Dependabot opens pull requests for security updates of the
  actions and Go modules only; refresh the base images and the `hf` CLI by
  hand, in a release.

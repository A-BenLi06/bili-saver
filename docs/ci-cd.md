# CI and releases

GitHub Actions validates every branch push and pull request. The same CI workflow
runs again against each release tag before any assets or images are published.

## Continuous integration

`CI` runs formatting checks, module checksum verification, builds, `go vet`, and
all internal tests on Linux and Windows. Linux also runs the race detector. Both
systems build, extract, and execute a release archive. A separate container job
builds the core image and checks its version and ffmpeg. Fork pull requests use
read-only permissions and do not receive publishing credentials.

The Go toolchain is read from `go.mod`. Build and test commands explicitly target
`./internal/...` and `./cmd/...`, avoiding runtime data and scratch directories.
Dependencies use `https://goproxy.cn,direct`, matching the project's development
environment. The optional teriteri frontend is not part of these core releases;
its existing Docker Compose source build remains available.

## Publishing a version

Push the workflow commit to GitHub first. After CI passes on the intended commit,
create and push a version tag, for example from PowerShell:

```powershell
git tag v0.2.0
git push origin v0.2.0
```

Use `vMAJOR.MINOR.PATCH` for stable releases and a SemVer prerelease such as
`v0.2.0-rc.1` for previews. Invalid versions, leading-zero numeric identifiers,
and `+build` metadata are rejected before publishing. Tags select the exact
source revision; the workflow never creates or moves tags.

After validation, the workflow builds six CGO-free archives:

| Operating system | Architectures | Format |
| --- | --- | --- |
| Linux | amd64, arm64 | `.tar.gz` |
| macOS (`darwin`) | amd64, arm64 | `.tar.gz` |
| Windows | amd64, arm64 | `.zip` |

Each archive contains the executable, `config.example.yaml`, and `README.md`.
The injected CLI version matches the tag without `v`. Install ffmpeg separately
for binary installations. A single `checksums.txt` covers all six archives.

The core container is published to `ghcr.io/<owner>/<repository>:<tag>` for
`linux/amd64` and `linux/arm64`, with ffmpeg included. Repository names are
lowercased automatically. Stable releases also update `:latest`; prereleases
leave it untouched. Stable tags should be published in ascending version order
because each successful stable run updates `:latest` and the latest Release.

The GitHub Release is published after the image succeeds. Assets are uploaded
to a draft before the Release becomes visible. Failed runs can be rerun from
the Actions page; existing assets are replaced for that same tag. Publishing
to GHCR and GitHub Releases is not atomic: a late failure can leave a published
image and a draft Release until the run is retried.

## Repository settings

- Enable GitHub Actions and allow the SHA-pinned actions in these workflows.
- Publishing uses the automatic `GITHUB_TOKEN`: only the image job requests
  `packages: write`, and only the release job requests `contents: write`.
  No personal access token or Docker Hub credentials are required.
- If the GHCR package already exists, grant this repository Actions write access
  in the package settings. For anonymous image pulls, set package visibility to
  public after its first publication; repository visibility alone is insufficient.
- To require CI before merging, add `Go (ubuntu-latest)`, `Go (windows-latest)`,
  and `Container smoke test` as required checks in the branch ruleset after their
  first run. Workflow files do not change repository protection settings.

Third-party actions are pinned to commit SHAs. Dependabot checks GitHub Actions
weekly and groups update pull requests.

## Local verification

With Go from `go.mod` and Python 3.9 or newer in PATH:

```powershell
$env:GOPROXY = 'https://goproxy.cn,direct'
go build ./internal/... ./cmd/...
go vet ./internal/... ./cmd/...
go test -count=1 -timeout 5m ./internal/...
python scripts/release.py --version 0.2.0-rc.1 --os windows --arch amd64
```

Archives are written to ignored `dist/`. Other targets use the same script with
`--os linux|darwin|windows` and `--arch amd64|arm64`. Build the core image with:

```powershell
$goVersion = (Get-Content go.mod | Where-Object { $_ -match '^go ' }).Split(' ')[1]
docker build --build-arg "GO_VERSION=$goVersion" --build-arg VERSION=0.2.0-rc.1 -f deploy/Dockerfile -t bili-saver:local .
docker run --rm bili-saver:local version
```

The Docker build context permits only `go.mod`, `go.sum`, `cmd/`, and `internal/`.
Local configuration, cookies, databases, media, Git metadata, and scratch files
are excluded. These workflows publish release artifacts; they do not deploy or
restart an application server.

References: [GitHub Go workflows](https://docs.github.com/en/actions/tutorials/build-and-test-code/go)
and [Docker multi-platform builds](https://docs.docker.com/build/ci/github-actions/multi-platform/).

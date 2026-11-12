# Walkthrough

## 2026-11-12T14:47:19+08:00 — Configure CI/CD

The requested delivery scope is GitHub Releases and GHCR images, without server
deployment. Existing application changes in the working tree remain independent.

Changes and rationale:

- Added reusable GitHub Actions CI for branch pushes, pull requests, manual runs,
  and release validation. It checks gofmt, module integrity, build, vet, and all
  internal tests on Linux/Windows, with Linux race detection and native archive
  execution. Container CI checks the CLI version and bundled ffmpeg.
- Read the Go version from go.mod, use the project's Go proxy, and target maintained
  cmd/internal packages so local data and scratch trees are not scanned.
- Added strict version-tag releases: six CGO-free Linux/macOS/Windows amd64/arm64
  archives, SHA-256 checksums, and a linux/amd64 + linux/arm64 GHCR core image.
  Prereleases do not move latest. A draft Release is published after the image
  succeeds, and failed publishing runs can be retried for the same tag.
- Added standard-library Python packaging with an injected CLI version, public
  config example and README, including explicit Unix executable permissions even
  when packaging from Windows.
- Updated the core Dockerfile for native cross-compilation, a supplied Go version,
  readonly modules and injected release versions. Added a source-only Docker
  context allowlist to exclude local credentials, archive data and scratch files.
- Pinned Actions to verified upstream commit SHAs and added weekly Dependabot
  updates. Documented release commands, GHCR settings, required-check setup,
  local verification, and the fact that image/Release publication is not atomic.

Validation:

- actionlint v1.7.12 passed both workflows; all 12 embedded shell scripts passed
  bash syntax checks. YAML and Python syntax checks passed.
- Executed the actual release metadata code for stable/prerelease tags, lowercase
  registry names, latest isolation, and invalid version rejection.
- Current worktree: build/vet and all internal tests passed on Windows. gofmt
  reported no changes. Final native archive extraction/execution used the actual
  workflow Python code and returned `bili-saver 0.0.0-ci`.
- Isolated clean HEAD plus CI/CD files: module verification, build, vet and all
  internal tests passed. Built all six release archives; verified PE/ELF/Mach-O
  architecture headers, archive contents, Unix executable permissions and SHA-256.
- An additional cross-build of the unrelated pending gopsutil feature failed on
  Linux because its existing go.mod/go.sum omit go-sysconf requirements/checksums.
  This is recorded in CLAIMS.md for core-agent; no dependency or application
  changes from that feature are included in this CI/CD commit.
- Docker is not installed locally, so container builds, Linux race execution,
  hosted runner execution, GHCR push and GitHub Release publication were not run.
  The workflows provide these checks after the commit is pushed. No remote
  repository settings, tags, running service, configuration or data were changed.

The current user explicitly requires a commit after completed changes. Only the
CI/CD files and this agent's claim/handoff entries are staged, preserving earlier
uncommitted collaboration work.

## 2026-11-12T15:40:16+08:00 — Preserve application baseline for archive hardening

The user requested the full reliability plan. The working tree contains previously completed but uncommitted application features (personal accounts/quotas, comment continuation, scheduling and performance changes). Preserve these application sources in an explicit baseline commit before implementing hardening so later changes can be reviewed and rolled back independently. Unrelated scratch files and runtime data/configuration are not part of this baseline. Windows maintained-source build, vet and internal tests passed during the audit; Linux dependency closure is a known pending issue to fix in this work.

## 2026-11-12T15:50:08+08:00 — Enforce archive observation correctness

Implemented per-CID, epoch-idempotent danmaku reconciliation with matching missing-set confirmation and atomic acknowledgement; fallback list.so never authorizes disappearance. Added durable collection status and failure reporting. Snapshot errors now propagate before parsing, CC responses are captured, and new raw bodies use bounded hash sharding with file sync. Added checksummed ordered migrations and future-version refusal while preserving legacy repair compatibility. Regression tests cover cross-CID false confirmation, replay, degraded coverage, snapshot failures and future databases. No production database or service was changed.

Validation: maintained-source build and vet, all internal tests, and store/biliapi/collector race tests passed. The new regressions now pass against the fixed implementation.

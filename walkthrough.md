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

## 2026-11-12T16:09:19+08:00 — Introduce portable immutable media identities

Implemented v17 assets, current page projections with retained history, frozen legacy bindings and separate local visibility. New downloads use content hashes and policy-specific working paths; old media is indexed in place only for unambiguous bindings. Added root-relative path resolution, checksum verification, CID-scoped CC/plugin/XML outputs and correct P2 STRM URLs. Rebuilding includes downloaded gone videos. Range responses are validated and ignored Range restarts are writable. Historical snapshots remain consumable after hash sharding. Validation: maintained-source build/vet and all internal tests passed; seven relevant packages passed race checks. New tests cover portable historical keys, symlink boundaries, same-size corruption, deduplication and the original download/export regressions. Production data/config/service remain unchanged.

## 2026-11-12T16:29:54+08:00 — Make runtime configuration transactional and observable

Added v18 database-backed runtime settings, immutable runtime snapshots and serialized publication across readers. Configuration and default-library changes persist together; stale revisions and environment-controlled edits are rejected. Startup YAML is preserved, with strict parsing, JSON environment arrays and atomic explicit-file saves. Fixed ID-based library rename and validation. Settings UI sends only changed fields and displays pending restart state and correct persistence information.

Validation: full maintained-source build/vet/internal suite passed; config/server/app/jobs/collector/downloader race suites passed. Browser plugin was not available, so temporary Playwright with installed Chrome verified the actual isolated service: settings save/reload, desired versus running concurrency, library rename identity, 1440x980 and 390x844 screenshots, no mobile document overflow, and no console/page errors. Both inline scripts passed node --check. QA artifacts are outside the repository in Temp/bili-hardening-qa-20261112; production service and data were not changed.

## 2026-11-12T16:56:29+08:00 — Add exclusive ownership and verified archive recovery

Added an OS-backed archive writer lock, deterministic application cleanup and bounded HTTP/plugin shutdown. Added read-only check/catalog commands, consistent SQLite backup, private file manifests, checksum verification, staged restore into new destinations and portable relocation with the source retained. Restored outputs regenerate node-local paths and disable automatic plugin/site startup pending configuration review. Recovery tests cover corruption rejection, existing-directory refusal, read-only checking and active-writer exclusion. Maintained-source build/vet and all internal tests passed.

During validation one build command accidentally replaced the ignored root executable. It was rebuilt from the preserved f5776af application baseline in an isolated worktree; no production process was restarted and no archive/configuration data changed. Subsequent runnable builds target Temp explicitly. Old binaries predate the advisory lock, so the first upgrade and maintenance operations require the old service to be stopped. Full backups contain private node state; content catalogs deliberately exclude credentials, sessions, local paths, jobs and quotas.

## 2026-11-12T17:01:49+08:00 — Isolate installation credentials and plugin capabilities

App startup now rotates the recognized legacy console seed while preserving custom accounts, persists generated playback/JWT credentials in an atomically synced private file, and shares live credentials between playback adapters. Password changes persist through the private vault and invalidate old playback tokens; no fixed signing fallback remains. Plugins receive a random process-local read-only archive capability, not the administrator token. Startup relative filesystem paths resolve against the config file directory. Removed the default-password shortcut from login and updated test fixtures to use immutable runtime configuration.

Validation: maintained-source build/vet, all internal tests and credential/app/server/adapter race suites passed. Tests verify durable seed rotation, custom-user preservation, password-dependent signing keys and denial of plugin administration. The next upgraded startup will write installation credentials; this implementation session has not opened or changed the production archive. Protect credentials.json and private backups with the data directory's Windows ACL or Unix owner permissions.

## 2026-11-12T17:09:34+08:00 — Persist deliveries and fail closed on queue state failures

Added schema v19 immutable archive events and per-target delivery state with bounded retry backoff. Download projection and event insertion are one SQLite transaction. Archive producers report persistence errors; the owned delivery worker replays plugin and media refresh work after restart, including explicit/personal downloads. Plugin envelopes include event IDs; append summary/relation operations have atomic receipts and replay-content checks, while process or operation failures remain retryable and audited.

Queue startup now refuses failed recovery. Initial and terminal database state changes retry transient failures; prolonged failure stops execution for durable recovery rather than silently losing jobs. A cooperative no-progress watchdog cancels stalled handlers without an immediate retry loop. Shutdown owns delivery and plugin work. Full maintained-source build/vet/internal tests passed. Relevant store/plugin race tests passed; a watchdog context-capture race found in the first run was corrected, then jobs/app race suites passed. Added regression coverage for transactional event rollback, reopen/replay, duplicate derived output, unavailable startup, terminal-write recovery and stalled jobs.

## 2026-11-12T17:18:25+08:00 — Own library projections and preserve source provenance

Schema v20 adds output ownership/intents, stable show groups/episodes and source tombstones with retained collection memberships. Rebuilds stage each library, propagate metadata/artwork/subtitle failures, reject unsafe/overlapping roots and publish atomic leaves. Stale cleanup only unlinks unchanged recorded outputs; manual/unmanaged conflicts are preserved and reported. First-generation exact matches can be adopted; pre-ledger stale files and former roots remain untouched. Current-CID media selection prevents stale P1 export. Shows include owner identity and retain names/numbers across older additions; collection layout now reflects all retained collection memberships.

Copy-mode media is linked cheaply in staging and copied only when publishing changed files. Media deliveries in a batch share one rebuild. Content checks still read managed regular files to validate ownership; large copy libraries trade verification I/O for integrity. Regression tests cover stale leaves versus user notes, manual changes, stable numbering, collection deletion provenance, protected roots and symlink ancestors. Maintained-source build/vet, all internal tests and media/store/app/server/metrics race checks passed. An existing socket-counter test exposed a scheduling assumption under load; it now waits for server StateIdle (after Write returns) before asserting counters, rather than assuming client receipt means the server counter is already updated.

## 2026-11-12T17:50:09+08:00 — Close portable I/O, resume validation and private recovery gaps

Added escaped SQLite file URIs for literal spaces/Unicode/# paths, checksum preflight before repair DDL, full snapshot reuse validation, current-CID playback selection and visible optional-component persistence errors. Stream staging now records selected-stream identity plus strong ETag/size, validates If-Range/Content-Range, restarts unvalidated partials and syncs data before checkpointing. Fixed zero-offset partial responses appending to stale bytes. Waiting personal downloads are nudged in the same download/event transaction.

Private writes now share an atomic synced utility with protected Windows owner/SYSTEM DACLs or Unix owner permissions; credentials, explicit config saves and cookies use it, and backup staging is private. Queue stall policy is configurable and exposed with restart feedback. Added admin-only delivery audit API, source-disappearance review candidates without repair, startup settings failure refusal and cleanup on CLI errors. Completed Linux/macOS dependency closure without changing pinned feature dependencies.

The actual restore smoke exposed missing startup-only YAML preferences. Configuration-aware backup/relocate now capture a private effective startup snapshot plus database settings and active external cookie/plugin files. Restore reads the snapshot without environment overrides during copying, rewrites node paths and preserves preferences; older data-only backups default automatic downloads/media output to disabled. Source configuration and data are retained.

Final validation: maintained-source readonly build, vet, every internal test and full Windows race suite passed. Six CGO-free Windows/Linux/macOS amd64/arm64 builds passed, with executable headers verified. Embedded JavaScript syntax passed. Temporary Chrome/Playwright tested 1440x980 and 390x844 settings/save/reload, running-versus-desired state, stable library rename, timeout control, no overflow and no console/page errors. API smoke verified default-password rejection, secret redaction, shared playback password persistence, JWT revocation and active-writer exclusion. Actual CLI check/export/backup/restore/relocate/catalog tests verified read-only checking, gone-video export, original P2 bytes, Unicode destinations, preserved hashes and source data, corruption rejection, restored-service credentials/settings/media and startup preference retention.

QA artifacts are in Temp/bili-hardening-final-20261112 and temporary binaries in Temp/bili-hardening-builds. Only scoped temporary services were stopped. No production archive, configuration, disappearance status or running service was changed; the earlier executable build mishap and baseline restoration remain recorded above. Docker and native Linux/macOS execution were not available/run; cross-builds and prepared CI cover those targets. No remote push, release or deployment was performed. Unrelated pre-existing raw documentation/performance/scratch files remain outside these commits. See docs/archive-recovery.md for first-upgrade and recovery behavior.

## 2026-11-12T23:16:16+08:00 — Add setup, core export and migration guides

Added a settings entry to an embedded responsive three-step guide. Configuration links lead to the existing account, Bilibili, collection, download and library controls. Admin-only guide metadata shows the current archive/config paths; migration commands use editable source/backup/destination paths and PowerShell literal quoting, and never execute filesystem operations from the page.

Added an admin-only core ZIP export with versioned NDJSON origin records for videos (including gone state), historical parts, comments, danmaku, statistics, original subtitle indexes, tags and official conclusions. Credentials/accounts/settings, plugin-derived records and node media paths are excluded. Actual media/raw/subtitle files remain explicitly outside this structured export; the migration guide uses the existing full private backup/restore/check path. A consistent SQLite read snapshot and bounded-memory row encoding build a private temporary ZIP before response headers. Only one export runs at a time; the read transaction is released before download and temporary files are removed afterward.

Validation: maintained-source build/vet/full internal tests and server race checks passed. Regression tests inspect the ZIP, retained gone records, origin-only tags, excluded private state and anonymous/plugin denial. Browser plugin not available: temporary Playwright/Chrome verified all steps, actual ZIP response, five migration commands, single-quote escaping, desktop/mobile screenshots and no document overflow or page errors. Artifacts: Temp/bili-guide-20261112. Production configuration/data/service were not changed.

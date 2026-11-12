# Archive recovery and portability

Stop the archive service before maintenance. Older versions do not participate in the new writer lock. Keep the original directory and a verified backup before upgrading.

```powershell
bili-saver -config config.yaml check
bili-saver -config config.yaml backup D:\Backups\bili-20261112
bili-saver restore D:\Backups\bili-20261112 D:\Archives\restored
bili-saver -config config.yaml relocate D:\Archives\portable
bili-saver -config config.yaml catalog > catalog.json
```

Check opens SQLite read-only without migration and verifies original snapshot digests, registered asset hashes and referenced media/subtitle files. It reports unresolved historical part ambiguities for metadata refresh and large disappearance batches as review candidates; it does not alter disappearance marks or delete content. A large batch is not by itself evidence of a false disappearance. Some old files have no recorded digest, so checking their existence is weaker than verifying a registered content hash.

Backup uses SQLite's consistent backup API and verifies a SHA-256 manifest of raw, media, derived and installed plugin files. It includes private node database state, generated credentials, a private startup configuration snapshot, active cookie state and installed plugins. Explicit external cookie/plugin locations are captured by the CLI configuration-aware commands. Store it as private, with filesystem access restricted to the owner. The startup snapshot captures effective values; keep the original YAML separately if you need its comments/format. Output libraries, logs, ffmpeg/application binaries, hosted frontend builds and incomplete download staging are regenerable and excluded. Manifest checks verify copied backup bytes; run check to compare content against recorded archive digests. Symlinks in archive source directories are refused, avoiding accidental traversal into unrelated directories.

Restore and relocate require a new destination, verify a staging copy before publication and retain their source. Local path fields and library roots are rebuilt in the copied database. The generated destination config.yaml uses loopback, disables plugins and hosted sites, preserves startup preferences and retains runtime preferences in SQLite. A legacy data-only backup without a startup snapshot defaults automatic downloads and media output to disabled. Old node paths, log targets and ffmpeg overrides are regenerated or cleared. Review the config before starting with `-config <new-root>\config.yaml`. Restore is intended for this schema generation; a newer schema is rejected. Library outputs can be regenerated with export-nfo.

Catalog format v1 exports content identity, digest, size, origin and observed provenance. It excludes private account state, sessions, cookies, runtime settings, absolute paths and task queues. It is a foundation for future exchange, not a network or trust protocol. No import or P2P service is enabled.


The first upgraded application startup automatically applies schema migrations and rotates the recognized legacy console seed into the private credentials file; custom passwords remain valid. Stop the old service before this first upgrade. No production database was migrated as part of implementation testing.

Library rebuilds only remove unchanged files recorded in their output ledger. Exact existing matches may be adopted. Manual edits, untracked historical files and outputs in former roots are retained for review. Symlink/hardlink outputs share original bytes, so consumers must treat them as read-only; choose copy mode for consumers that rewrite video files. Copy-library ownership checks incur content-reading I/O.

Plugin processes remain trusted local code. API capabilities limit their HTTP access, while their operating-system file access is unchanged. Delivery is at least once; external plugin side effects must deduplicate by event_id. Derived append operations use durable core receipts and reject changed output on replay.

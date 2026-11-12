# Archive recovery and portability

Stop the archive service before maintenance. Older versions do not participate in the new writer lock. Keep the original directory and a verified backup before upgrading.

```powershell
bili-saver -config config.yaml check
bili-saver -config config.yaml backup D:\Backups\bili-20261112
bili-saver restore D:\Backups\bili-20261112 D:\Archives\restored
bili-saver -config config.yaml relocate D:\Archives\portable
bili-saver -config config.yaml catalog > catalog.json
```

Check opens SQLite read-only without migration and verifies original snapshot digests, registered asset hashes and referenced media/subtitle files. It reports historical part ambiguities for metadata refresh; it does not alter disappearance marks or delete content. Some old files have no recorded digest, so checking their existence is weaker than verifying a registered content hash.

Backup uses SQLite's consistent backup API and verifies a SHA-256 manifest of raw, media, derived and installed plugin files. It includes private node database state and default root cookie/credential files. Store it as private, with filesystem access restricted to the owner. External cookie files and the startup YAML outside the data root must be backed up separately. Output libraries and incomplete download staging are regenerable and excluded. Symlinks in archive source directories are refused, avoiding accidental traversal into unrelated directories.

Restore and relocate require a new destination, verify a staging copy before publication and retain their source. Local path fields and library roots are rebuilt in the copied database. The generated destination config.yaml uses loopback, disables plugins and hosted sites, and preserves runtime preferences in SQLite. Review the config before starting with `-config <new-root>\config.yaml`. Restore is intended for this schema generation; a newer schema is rejected. Library outputs can be regenerated with export-nfo.

Catalog format v1 exports content identity, digest, size, origin and observed provenance. It excludes private account state, sessions, cookies, runtime settings, absolute paths and task queues. It is a foundation for future exchange, not a network or trust protocol. No import or P2P service is enabled.

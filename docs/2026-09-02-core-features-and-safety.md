# Sep 2, 2026: Core features and safety

**Features**
- Backup: chosen folders to Drive. Deleting on the computer keeps the Drive copy.
- Sync: one folder the same on every computer.
- Restore: folders, files, old versions. Never overwrites local files.

**Safety**
- Re-check a Drive file before overwriting or deleting it.
- Both sides changed: keep both (`name (conflict …)`).
- Removed files go to trash first.
- Big delete in one pass: stop and ask.
- Every upload reserves its Drive ID, so retries never make duplicates.

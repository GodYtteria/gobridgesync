# Benchmarks

Measured numbers only, on a Fedora 44 laptop (Ryzen 7 PRO 5850U, Wi-Fi), real Google Drive unless noted. Network speed changes from minute to minute, so each live row says what the raw link did at the time.

## Streaming folder (Linux)

| What | v0.2 / first build | v0.3.1-rc5 | How it was measured |
|---|---|---|---|
| Start reading at a random spot in a 2.6 GB file (not cached) | 3-7 s | 0.97-1.14 s | live, 128 KB read after seek |
| Same, part already cached | n/a | 0 ms | live |
| Read a file from start to end (not cached) | 3.6-7 MB/s | 13.3 MB/s (raw link 8.8 MB/s) | live, 20 s sequential read; parallel read-ahead beats one stream |
| First byte after a seek, 4 MB/s link | 2 s | 65 ms | simulated link in a test |
| First byte after a seek, 8 MB/s link | 1 s | 33 ms | simulated link in a test |
| App memory while streaming | n/a | 471 MB | live, RSS; RAM cache is 512 MB-1 GB depending on free RAM and empties after 2 minutes idle |
| App CPU while streaming | n/a | 3.9% | live, average |
| Upload 300 small files written into the folder | 266 s | 67 s | live |

## Working in the streaming folder

| Tool or action | First build | v0.3.1-rc5 |
|---|---|---|
| `git init`, several commits, `git status`, `rm -r` the repo | failed (folder lost after a second, conflict copies of `.git/index`) | works, no conflict copies |
| `rsync -a` twice | every file copied again | second run copies nothing |
| `cp -p`, `touch -d` | times lost | times kept on Drive |
| Editor-style save (write temp file, rename over the old one) | n/a | one upload, no duplicate |
| SQLite database (200 inserts, reopen) | n/a | works |
| mkdir, append, rename, 40 MB file, copy | read-only | all on Drive with identical hash |

## Install and updates

| What | Result |
|---|---|
| Install with `install.sh` (Linux, Bukluran edition) | 5 s |
| Automatic update, staging v0.3.0-rc17 to v0.3.1-rc5 | applied 2 minutes after start, signature checked, folder mounted again |

## Quality

| What | v0.3.0-rc3 | v0.3.1-rc5 |
|---|---|---|
| Automated tests (run with the race detector) | 156 | 236 |
| Bugs found by code review or live tests | n/a | each fixed together with a test that fails without the fix |

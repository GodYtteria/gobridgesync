# Streaming progress

Only measured numbers are listed. "n/a" means not measured yet.

| Item | Before | After | Source |
|---|---|---|---|
| Seek, first byte, 4 MB/s link (simulated) | 2 s (whole 8 MiB chunk) | 65 ms | throttled fetcher test |
| Seek, first byte, 8 MB/s link (simulated) | 1 s | 33 ms | throttled fetcher test |
| Seek on real Drive, 2.6 GB file | 3-7 s | 1.2-1.7 s | live, rc4 |
| Cold sequential read on real Drive | 3.6-7 MB/s | 8-10 MB/s on a good link (0.3-2 MB/s when the link was congested by uploads) | live, rc4 |
| Write through the mount, live | n/a | mkdir, append, rename, 40 MB file, copy: all on Drive with identical hash | live, rc4 |
| RAM chunk cache | fixed 128 MB | 512 MB to 1 GB by free RAM, a quarter of free RAM below 2 GB, idle chunks released after 2 min | unit tests |
| Parallel chunk downloads while scrubbing | unbounded | at most 8 | unit test |
| Tests (race) | 156 | 162 | go test |
| Writes through the mount | read-only | saved to Drive after close, conflict and recovered copies, rename/mkdir/rmdir/delete | unit + real FUSE tests |

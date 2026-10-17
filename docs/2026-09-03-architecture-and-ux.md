# Sep 3, 2026: Architecture and UX

**Architecture**
- Go. One binary per OS: Linux, Windows, macOS, FreeBSD.
- Local web UI on `127.0.0.1` + tray icon.
- Local Drive index updated from Drive's change feed; no full re-list each check.
- Low CPU first; caches sized to free RAM.

**UX**
- Inbox: drop files, they get sorted (Pictures, Videos, Documents, Models, Installers, …).
- Streaming folder (Linux): whole Drive as a folder; files download when opened. For big AI models and video.
- Short, plain messages. Every problem says what to do.

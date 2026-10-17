# Sep 4, 2026: Security and releases

**Security**
- Google sign-in with your own OAuth client (public) or the organization's (Bukluran).
- Token in the system keyring.
- UI only on `127.0.0.1`, per-run token, other sites refused.
- Signed updates; bad signature refused.

**Bukluran edition:** same code, Bukluran's Google client, sign in with `@bukluran.com`, whole Drive in `GoogleDrive`.

**Releases**
1. Private staging repo.
2. Release candidates `vX.Y.Z-rcN` as pre-releases for testers.
3. Stable `vX.Y.Z` promoted from a tested candidate; auto-update installs it.

<p align="center"><img src="docs/banner.svg" alt="GoBridgeSync: back up and sync your computers with Google Drive" width="100%"></p>

<p align="center">
  <a href="https://github.com/GodYtteria/gobridgesync/actions/workflows/ci.yml"><img src="https://github.com/GodYtteria/gobridgesync/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <img src="https://img.shields.io/badge/platform-Linux%20%7C%20Windows%20%7C%20macOS-0284c7" alt="Linux, Windows, macOS">
  <img src="https://img.shields.io/badge/license-Apache--2.0-65a30d" alt="Apache-2.0">
</p>

| | |
|---|---|
| <img src="docs/icons/backup.svg" alt="" width="28"> | **Backup** your folders to Google Drive. Deleting a file on your computer keeps the Drive copy. |
| <img src="docs/icons/sync.svg" alt="" width="28"> | **Sync** `~/GoBridgeSync` across every computer signed in with the same Google account. |
| <img src="docs/icons/backup.svg" alt="" width="28"> | **Restore** folders, files, or older versions from Drive. Restores never overwrite your files. |
| <img src="docs/icons/alert.svg" alt="" width="28"> | **Alerts** when a backup fails or Drive is almost full. |
| <img src="docs/icons/lock.svg" alt="" width="28"> | **Private.** Only touches files it created. Your sign-in stays in your system keyring. |
| <img src="docs/icons/free.svg" alt="" width="28"> | **Free and open source.** |

<p align="center"><img src="docs/screenshot-dark.png" alt="GoBridgeSync web interface" width="720"></p>

## Install

**Linux and macOS**

```sh
curl -fsSL https://raw.githubusercontent.com/GodYtteria/gobridgesync/main/install.sh | sh
```

**Windows** (PowerShell)

```powershell
irm https://raw.githubusercontent.com/GodYtteria/gobridgesync/main/install.ps1 | iex
```

GoBridgeSync shows a tray icon and opens at <http://127.0.0.1:47811/>. Downloads are checked against the release SHA-256 checksums.

Works on any Linux distribution (x86-64 and ARM64). The tray icon appears on KDE, Ubuntu, Cinnamon, and XFCE; on plain GNOME install the AppIndicator extension. Without a tray, use the app menu or the web address.

## Settings

Change these from the tray menu or the web interface.

| Setting | Default | Choices |
|---|---|---|
| Start at login | On | On, Off |
| Sync | Every 5 minutes | Instantly when files change, every 1, 5, 15, or 60 minutes, or Manual only |
| Skip these | None | Names like `*.tmp` or `node_modules`, or folders like `~/Documents/SD-Backup` |
| Speed limit | Unlimited | 10 MB/s, 5 MB/s, 1 MB/s, 512 KB/s |
| Storage limit | 90% | All, 90%, 75%, 50% of your Drive storage |
| Sync across computers | On | On, Off |
| Install updates automatically | On | On, Off |

**Data use:** without internet, GoBridgeSync uses almost no data and tries again later. Each check reads the list of your backed-up files, so if you have many files or a limited data plan, choose a longer interval or **Manual only**.

## Connect Google Drive

Use your own free Google OAuth client. One time, about 5 minutes. The app shows the same guide.

1. **[Create a project](https://console.cloud.google.com/projectcreate)** named `GoBridgeSync`. Keep the defaults and skip any billing offer.
   Seeing "Google Cloud access blocked"? Turn on [2-Step Verification](https://myaccount.google.com/signinoptions/two-step-verification), sign out, and sign in again.
2. **[Enable the Google Drive API](https://console.cloud.google.com/apis/library/drive.googleapis.com)**.
3. **[Set up the consent screen](https://console.cloud.google.com/auth/overview)**: app name `GoBridgeSync`, audience **External**. Then add your Google account under **[Audience → Test users](https://console.cloud.google.com/auth/audience)**. Keep it in **Testing**.
4. **[Create an OAuth client](https://console.cloud.google.com/auth/clients/create)**: type **Desktop app**, leave "AI-powered agent" off, and **Download JSON**.
5. In GoBridgeSync, choose the JSON and click **Sign in with Google**. At "Google hasn't verified this app", choose **Continue**.

## Good to know

- Files uploaded on drive.google.com into the sync folder are not seen by the app.
- Google allows 750 GB of uploads per user per day. Rate limits are retried automatically.
- Removed files go to `~/.local/share/gobridgesync/trash/`, not straight to deletion.
- Google keeps older versions of a file for 30 days; restore them from **Restore → Versions**.
- Skipped files are never deleted from Drive or other computers.
- Quitting from the tray asks for confirmation, so backups don't stop by accident.
- Updates are checked daily. Each release is signed, and GoBridgeSync refuses any update whose signature doesn't match the key built into the app.

## Build from source

```sh
git clone https://github.com/GodYtteria/gobridgesync.git
cd gobridgesync
go test ./...
./contrib/install-linux.sh
```

`./build.sh` builds release packages for every platform into `dist/`. Organizations with Google Workspace can build the internal edition, which syncs the whole Drive: `go build -ldflags "-X main.edition=internal"`.

## License

[Apache-2.0](LICENSE). The GoBridgeSync name and logo are not covered; see [NOTICE](NOTICE).

<p align="center">Developed by <b>GodYtteria</b> × <b>Bukluran ng Kalinga</b></p>

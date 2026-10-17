<p align="center"><img src="docs/banner.svg" alt="GoBridgeSync: back up and sync your computers with Google Drive" width="100%"></p>

<p align="center">
  <a href="https://github.com/GodYtteria/gobridgesync-desktop/actions/workflows/ci.yml"><img src="https://github.com/GodYtteria/gobridgesync-desktop/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <img src="https://img.shields.io/badge/platform-Linux%20%7C%20Windows%20%7C%20macOS-0284c7" alt="Linux, Windows, macOS">
  <img src="https://img.shields.io/badge/license-Apache--2.0-65a30d" alt="Apache-2.0">
</p>

GoBridgeSync keeps your files safe in Google Drive and the same on all your computers. Free and open source.

- **Backup:** your folders are copied to Google Drive. Deleting a file on your computer keeps the Drive copy.
- **Sync:** the `GoBridgeSync` folder is the same on every computer signed in with your Google account.
- **Restore:** get back folders, files, or older versions from Drive. A restore never overwrites your files.
- **Inbox:** drop files in, and they are sorted into Pictures, Videos, Documents, and more.
- **Streaming folder** (Linux): your whole Drive as a folder, without filling your disk.

<p align="center"><img src="docs/screenshot-dark.png" alt="GoBridgeSync window" width="720"></p>

## Install

**Windows:** press **Start**, type **PowerShell**, open it, paste this line (right-click pastes), and press **Enter**:

```powershell
irm https://raw.githubusercontent.com/GodYtteria/gobridgesync-desktop/main/install.ps1 | iex
```

**macOS and Linux:** open **Terminal** (macOS: **Cmd + Space**, type Terminal; Linux: **Ctrl + Alt + T**), paste this line, and press **Enter**:

```sh
curl -fsSL https://raw.githubusercontent.com/GodYtteria/gobridgesync-desktop/main/install.sh | sh
```

GoBridgeSync opens in your browser. The first time, it walks you through connecting Google Drive (about 5 minutes, see below). After that it starts with your computer and updates itself. Open it any time from the tray icon or <http://127.0.0.1:47811/>.

<details>
<summary><b>Connect Google Drive (first time only)</b></summary>

You use your own free Google key, so your files never pass through anyone else. The app shows the same steps.

1. **[Create a project](https://console.cloud.google.com/projectcreate)** named `GoBridgeSync`. Keep the defaults and skip any billing offer. Seeing "Google Cloud access blocked"? Turn on [2-Step Verification](https://myaccount.google.com/signinoptions/two-step-verification), then sign in again.
2. **[Turn on the Google Drive API](https://console.cloud.google.com/apis/library/drive.googleapis.com)**.
3. **[Set up the consent screen](https://console.cloud.google.com/auth/overview)**: name `GoBridgeSync`, audience **External**. Add your own Google account under **[Audience → Test users](https://console.cloud.google.com/auth/audience)**. Leave it in **Testing**.
4. **[Create an OAuth client](https://console.cloud.google.com/auth/clients/create)**: type **Desktop app**, then **Download JSON**.
5. In GoBridgeSync, choose that JSON file and click **Sign in with Google**. When Google says "Google hasn't verified this app", choose **Continue** (it is your own app).

</details>

## Inbox

Drop anything into the **Inbox** folder. About 10 seconds later it is moved into the right folder, and Inbox is empty again.

<p align="center"><img src="docs/inbox-example.svg" alt="Files dropped into Inbox are moved into Pictures, Videos, Documents, Models, Installers, Music, Folders, and Other" width="720"></p>

1. Open Inbox: `GoBridgeSync\Inbox` in your user folder (Windows), `~/GoBridgeSync/Inbox` (macOS, Linux), or **GoBridgeSync → Inbox** on [drive.google.com](https://drive.google.com) from your phone.
2. Drag files or whole folders in.
3. Wait a few seconds. Each move shows in the app's **Activity** list.

Inbox needs **Sync across computers** turned on (it is on by default). Nothing is ever overwritten: a second `photo.jpg` becomes `photo (2).jpg`.

<details>
<summary><b>Where each file goes</b></summary>

| Folder | Files |
|---|---|
| Pictures/`YYYY-MM`, Videos/`YYYY-MM` | photos and videos, by the month the file was last changed |
| Music, Documents, Ebooks | audio; PDF, Office, text, Google Docs; e-books |
| Archives, Installers | zip, tar, 7z; exe, msi, dmg, rpm, deb, AppImage, iso |
| Models, Code, Design, 3D, Fonts | AI models; single source files; psd, fig, blend; stl, obj; ttf, otf |
| Folders | folders dropped into Inbox, moved whole |
| Other/`EXT` | anything else, one folder per file type |

</details>

## Streaming folder (Linux)

Your whole Google Drive as the folder `~/GoogleDrive-Stream`. Files download only when you open them, so it hardly uses disk space. Edit, rename, and delete there like any folder; changes go back to Drive on their own.

1. Install FUSE once: Fedora `sudo dnf install fuse3`, Ubuntu `sudo apt install fuse3`.
2. In GoBridgeSync **Settings**, turn on **Streaming folder**.
3. Open `~/GoogleDrive-Stream`.

<details>
<summary><b>Keep files on this computer, tips</b></summary>

Pinned files are downloaded fully and kept, so they open instantly and work offline:

```sh
gobridgesync stream-pin Videos/2026-10      # a folder or a file
gobridgesync stream-pins                    # list
gobridgesync stream-unpin Videos/2026-10
```

- Video editing: pin the project's videos before you start; unpin them after.
- Coding: pin the project you are working on. `git`, `rsync`, editors, and SQLite work in the folder.
- A part of a file that is not downloaded yet takes about a second to open, then reads as fast as your internet.
- Deleting more than 5,000 files within 10 minutes there is refused. Deleted files go to the Google Drive trash.
- Windows and macOS: coming later. Use the sync folder there.

</details>

## More

<details>
<summary><b>Settings</b></summary>

Change these from the tray menu or the app.

| Setting | Default | Choices |
|---|---|---|
| Start at login | On | On, Off |
| Sync | Every 5 minutes | When files change, every 1, 5, 15, or 60 minutes, or Manual only |
| Don't back up | None | Folders you pick, or names like `*.tmp` or `node_modules` |
| Speed limit | Unlimited | 10 MB/s, 5 MB/s, 1 MB/s, 512 KB/s |
| Storage limit | 90% | All, 90%, 75%, 50% of your Drive |
| Sync across computers | On | On, Off |
| Install updates automatically | On | On, Off |
| Developer mode | Off | On, Off (adds the **Add developer folders** preset) |
| Streaming folder (Linux) | Off | On, Off, and a cache size |

On a small data plan, pick a longer sync interval or **Manual only**.

</details>

<details>
<summary><b>What syncs</b></summary>

| Item | Synced |
|---|---|
| Files and folders in My Drive | Yes, both ways |
| Google Docs, Sheets, Slides | As link files (`.gdoc`, `.gsheet`) that open in the browser |
| Shared with me, shortcuts, shared drives, trash | No |
| Names your computer cannot use (for example `:` on Windows) | Yes, with `_` in their place on your computer; the Drive name stays |

</details>

<details>
<summary><b>Safety and good to know</b></summary>

- If one pass would delete more than 5,000 files (or over half of them), GoBridgeSync stops and asks you to **Allow** it.
- Before changing or deleting a Drive file, it checks the file did not change on Drive in the meantime. If both sides changed, both are kept (`name (conflict …)`).
- Files removed on your computer by a sync are kept in GoBridgeSync's trash for 30 days. Google keeps older versions for 30 days (**Restore → Versions**).
- Coding: with **Developer mode** on, **Don't back up → Add developer folders** skips `node_modules`, `.venv`, `build`, `dist`, `target`. Work on a project from one computer at a time.
- Updates are signed; an update with a wrong signature is refused.
- Logs: tray → **Open log file**.
- Test versions: add `GBS_CHANNEL=staging` to the install line (`... | GBS_CHANNEL=staging sh`, or `$env:GBS_CHANNEL='staging'` first on Windows) to get pre-releases.

</details>

<details>
<summary><b>Uninstall</b></summary>

Quit GoBridgeSync from the tray, then run `gobridgesync uninstall` (add `-purge` to also remove sign-in and settings). On Windows: `%LOCALAPPDATA%\GoBridgeSync\gobridgesync.exe uninstall`. Your files and your Google Drive are not touched.

</details>

<details>
<summary><b>Build from source</b></summary>

```sh
git clone https://github.com/GodYtteria/gobridgesync-desktop.git
cd gobridgesync-desktop
go test ./...
./contrib/install-linux.sh
```

`./build.sh` builds packages for every platform into `dist/`. Speed and test numbers: [docs/BENCHMARKS.md](docs/BENCHMARKS.md).

</details>

## License

[Apache-2.0](LICENSE). The GoBridgeSync name and logo are not covered; see [NOTICE](NOTICE).

<p align="center">Developed by <b>GodYtteria</b> × <b>Bukluran ng Kalinga</b></p>

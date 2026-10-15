//go:build linux || windows || (darwin && cgo)

package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"fyne.io/systray"
)

//go:embed assets/tray
var trayIcons embed.FS

func trayFile(name string) []byte {
	b, err := trayIcons.ReadFile("assets/tray/" + name)
	if err != nil {
		return nil
	}
	return b
}

func gnomePanel() bool {
	return strings.Contains(strings.ToUpper(os.Getenv("XDG_CURRENT_DESKTOP")), "GNOME")
}

func setTrayIcon(badge string) {
	active := trayActive(badge)
	switch runtime.GOOS {
	case "windows":
		systray.SetIcon(trayFile(map[bool]string{true: "color.ico", false: "gray.ico"}[active]))
	case "darwin":
		systray.SetTemplateIcon(trayFile("template.png"), trayFile(map[bool]string{true: "color.png", false: "gray.png"}[active]))
	default:
		if gnomePanel() {
			systray.SetIcon(trayFile(map[bool]string{true: "mono-on.png", false: "mono-off.png"}[active]))
		} else {
			systray.SetIcon(trayFile(map[bool]string{true: "color.png", false: "gray.png"}[active]))
		}
	}
}

const trayFolderSlots = 12

var trayLimits = []int{100, 90, 75, 50}

var trayIntervals = []int{instantSync, 1, 5, 15, 60, manualSync}

func intervalLabel(m int) string {
	switch m {
	case manualSync:
		return "Manual only"
	case instantSync:
		return "Instantly"
	case 1:
		return "Every minute"
	case 60:
		return "Every hour"
	}
	return fmt.Sprintf("Every %d minutes", m)
}

func setChecked(item *systray.MenuItem, on bool) {
	if on {
		item.Check()
	} else {
		item.Uncheck()
	}
}

func setEnabled(item *systray.MenuItem, on bool) {
	if on {
		item.Enable()
	} else {
		item.Disable()
	}
}

func limitLabel(pct int) string {
	if pct == 100 {
		return "All available"
	}
	return fmt.Sprintf("%d%%", pct)
}

func runTray(ctx context.Context, quit context.CancelFunc, r *Runner, url string) error {
	onReady := func() {
		setTrayIcon("sync")
		systray.SetTitle("")
		systray.SetTooltip(releaseName())

		status := systray.AddMenuItem("Starting…", "")
		status.Disable()
		storage := systray.AddMenuItem("Drive storage: checking…", "")
		storage.Disable()
		systray.AddSeparator()

		running := systray.AddMenuItemCheckbox("Back up is on", "Turn backup and sync on or off", true)
		everyMenu := systray.AddMenuItem("Sync: every 5 minutes", "")
		everyItems := make([]*systray.MenuItem, len(trayIntervals))
		for i, m := range trayIntervals {
			everyItems[i] = everyMenu.AddSubMenuItemCheckbox(intervalLabel(m), "", false)
		}
		syncFolder := systray.AddMenuItemCheckbox("Sync across computers", "", false)
		limitMenu := systray.AddMenuItem("Storage limit", "")
		limits := make([]*systray.MenuItem, len(trayLimits))
		for i, pct := range trayLimits {
			limits[i] = limitMenu.AddSubMenuItemCheckbox(limitLabel(pct), "", false)
		}
		foldersMenu := systray.AddMenuItem("Backing up 0 folders", "")
		folderItems := make([]*systray.MenuItem, trayFolderSlots)
		for i := range folderItems {
			folderItems[i] = foldersMenu.AddSubMenuItemCheckbox("", "", true)
			folderItems[i].Disable()
			folderItems[i].Hide()
		}
		editFolders := foldersMenu.AddSubMenuItem("Edit folders…", "")
		startup := systray.AddMenuItemCheckbox("Start at login", "", true)
		systray.AddSeparator()

		updateItem := systray.AddMenuItem("Install update", "")
		updateItem.Hide()
		syncNow := systray.AddMenuItem("Back up now", "")
		openUI := systray.AddMenuItem("Open GoBridgeSync", "")
		openFolder := systray.AddMenuItem("Open sync folder", "")
		openInbox := systray.AddMenuItem("Open Inbox", "")
		openDrive := systray.AddMenuItem("Open Google Drive", "")
		openLog := systray.AddMenuItem("Open log file", "")
		systray.AddSeparator()
		versionItem := systray.AddMenuItem(releaseName()+" "+version, "")
		versionItem.Disable()
		quitMenu := systray.AddMenuItem("Quit GoBridgeSync…", "")
		exit := quitMenu.AddSubMenuItem("Yes, stop backup and sync", "")

		lastBadge := ""
		update := func() {
			s := r.Snapshot()
			line, badge := trayStatus(s, time.Now())
			status.SetTitle(line)
			systray.SetTooltip("GoBridgeSync: " + line)
			if badge != lastBadge {
				setTrayIcon(badge)
				lastBadge = badge
			}
			storage.SetTitle(trayStorage(s.Quota))
			ready := s.HasCredentials && s.LoggedIn
			setChecked(running, !s.Paused)
			setEnabled(running, ready)
			setChecked(syncFolder, s.TwoWay)
			syncFolder.SetTitle("Sync " + shortHome(s.Root, s.Home) + " across computers")
			setEnabled(syncFolder, ready)
			for i, pct := range trayLimits {
				setChecked(limits[i], s.StorageLimit == pct)
			}
			for i, m := range trayIntervals {
				setChecked(everyItems[i], s.Every == m)
			}
			everyMenu.SetTitle("Sync: " + strings.ToLower(intervalLabel(s.Every)))
			setChecked(startup, s.StartAtLogin)
			foldersMenu.SetTitle(fmt.Sprintf("Backing up %d folder(s)", len(s.Folders)))
			for i, item := range folderItems {
				if i < len(s.Folders) {
					item.SetTitle(shortHome(s.Folders[i], s.Home))
					item.Show()
				} else {
					item.Hide()
				}
			}
			setEnabled(syncNow, ready && !s.Paused)
			if s.Update != "" {
				updateItem.SetTitle("Install update " + s.Update)
				setEnabled(updateItem, !s.Updating)
				updateItem.Show()
			} else {
				updateItem.Hide()
			}
		}
		update()

		everyPicked := make(chan int)
		for i, m := range trayIntervals {
			go func() {
				for range everyItems[i].ClickedCh {
					everyPicked <- m
				}
			}()
		}
		limitPicked := make(chan int)
		for i, pct := range trayLimits {
			go func() {
				for range limits[i].ClickedCh {
					limitPicked <- pct
				}
			}()
		}
		go func() {
			tick := time.NewTicker(2 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					systray.Quit()
					return
				case <-tick.C:
					update()
				case <-running.ClickedCh:
					_ = r.SetPaused(!r.Snapshot().Paused)
					update()
				case <-syncFolder.ClickedCh:
					_ = r.UpdateConfig(func(c *AppConfig) { c.TwoWay = !c.TwoWay })
					update()
				case m := <-everyPicked:
					_ = r.UpdateConfig(func(c *AppConfig) { c.Every = m })
					update()
				case <-startup.ClickedCh:
					on := !r.Snapshot().StartAtLogin
					if err := setAutostart(on); err != nil {
						log.Printf("start at login: %v", err)
					} else {
						_ = r.UpdateConfig(func(c *AppConfig) { c.Startup = &on })
					}
					update()
				case pct := <-limitPicked:
					_ = r.UpdateConfig(func(c *AppConfig) { c.Limit = pct })
					update()
				case <-syncNow.ClickedCh:
					r.Trigger()
				case <-updateItem.ClickedCh:
					go func() {
						if err := r.ApplyUpdate(ctx); err != nil {
							log.Printf("update: %v", err)
						}
					}()
				case <-openUI.ClickedCh:
					_ = openBrowser(url)
				case <-editFolders.ClickedCh:
					_ = openBrowser(url)
				case <-openInbox.ClickedCh:
					dir := filepath.Join(r.paths.Root, inboxName)
					_ = os.MkdirAll(dir, 0o755)
					_ = openPath(dir)
				case <-openFolder.ClickedCh:
					_ = os.MkdirAll(r.paths.Root, 0o755)
					_ = openPath(r.paths.Root)
				case <-openLog.ClickedCh:
					_ = openPath(r.paths.logFile())
				case <-openDrive.ClickedCh:
					_ = openBrowser("https://drive.google.com/drive/my-drive")
				case <-exit.ClickedCh:
					if err := desktopNotify("GoBridgeSync stopped", "Backup and sync are off until you open GoBridgeSync again."); err != nil {
						log.Printf("notify: %v", err)
					}
					quit()
				}
			}
		}()
	}
	systray.Run(onReady, quit)
	return nil
}

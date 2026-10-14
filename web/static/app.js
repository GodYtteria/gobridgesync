"use strict";

const token = document.querySelector('meta[name="gbs-token"]').content;
const $ = (id) => document.getElementById(id);
const SVG = "http://www.w3.org/2000/svg";

function icon(name) {
  const svg = document.createElementNS(SVG, "svg");
  svg.setAttribute("class", "icon");
  svg.setAttribute("aria-hidden", "true");
  const use = document.createElementNS(SVG, "use");
  use.setAttribute("href", "#i-" + name);
  svg.append(use);
  return svg;
}
let current = null;

async function post(path, body) {
  const res = await fetch(path, { method: "POST", headers: { "X-GBS-Token": token }, body });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new Error(data.error || "Request failed");
  }
  return data;
}

const osText = {
  windows: { sep: "\\", startup: "Start GoBridgeSync when Windows starts", folder: "~\\Downloads", restore: "~\\GoBridgeSync Restore", files: "File Explorer" },
  darwin: { sep: "/", startup: "Open GoBridgeSync at login", folder: "~/Downloads", restore: "~/GoBridgeSync Restore", files: "Finder" },
  linux: { sep: "/", startup: "Start GoBridgeSync when I sign in", folder: "~/Downloads", restore: "~/GoBridgeSync Restore", files: "Files" },
};
let osApplied = "";

function applyOS(os) {
  if (os === osApplied) {
    return;
  }
  osApplied = os;
  const t = osText[os] || osText.linux;
  document.documentElement.dataset.os = os;
  $("startup-label").textContent = t.startup;
  $("add-folder").placeholder = t.folder;
  $("exclude-input").placeholder = "*.tmp or a folder";
  if ($("restore-dest").value === osText.linux.restore || $("restore-dest").value === osText.windows.restore) {
    $("restore-dest").value = t.restore;
  }
  $("open").title = "Open in " + t.files;
}

function short(p) {
  const home = current && current.home;
  return home && (p === home || p.startsWith(home + "/") || p.startsWith(home + "\\")) ? "~" + p.slice(home.length) : p;
}

function when(t) {
  const d = new Date(t);
  return d.toDateString() === new Date().toDateString() ? d.toLocaleTimeString() : d.toLocaleString();
}

function bytes(n) {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return (i ? n.toFixed(1) : n) + " " + units[i];
}

function duration(sec) {
  if (sec < 60) return Math.round(sec) + "s";
  if (sec < 3600) return Math.floor(sec / 60) + "m " + Math.round(sec % 60) + "s";
  return Math.floor(sec / 3600) + "h " + Math.floor((sec % 3600) / 60) + "m";
}

function meter(bar, fill, pct) {
  const v = Math.max(0, Math.min(100, pct));
  fill.style.width = v + "%";
  bar.setAttribute("aria-valuenow", Math.round(v));
  return v;
}

function hintFor(msg) {
  if (msg.includes("not enough Google Drive storage")) {
    return "Fix: remove large folders under Backup folders, raise the storage limit, or upgrade your Google storage.";
  }
  if (msg.includes("connect to Google Drive") || msg.includes("oauth2") || msg.includes("token")) {
    return "Fix: check your internet connection, or sign in again.";
  }
  if (msg.includes("permission denied")) {
    return "Fix: GoBridgeSync cannot read this file. Check its permissions or remove it from backup.";
  }
  return "See Problems below for details.";
}

function renderAlert(s) {
  const issues = s.issues || [];
  const q = s.quota;
  const box = $("alert");
  if (s.state === "error" && issues.length) {
    box.className = "alert";
    $("alert-title").textContent = issues.length === 1 ? "Backup could not finish" : "Backup could not finish (" + issues.length + " problems)";
    $("alert-text").textContent = issues[0].message;
    $("alert-hint").textContent = hintFor(issues[0].message);
    box.hidden = false;
  } else if (q && q.limit > 0 && q.used / q.limit >= 0.9) {
    box.className = "alert warn";
    $("alert-title").textContent = "Google Drive is almost full";
    $("alert-text").textContent = bytes(Math.max(q.limit - q.used, 0)) + " free of " + bytes(q.limit) + ".";
    $("alert-hint").textContent = "New backups may stop. Free up space or upgrade your Google storage.";
    box.hidden = false;
  } else {
    box.hidden = true;
  }
}

function renderStats(s) {
  renderAlert(s);
  const ready = s.hasCredentials && s.loggedIn;
  $("overview").hidden = !ready;
  if (s.quota) {
    const q = s.quota;
    if (q.limit > 0) {
      const pct = meter($("quota-bar"), $("quota-fill"), (q.used / q.limit) * 100);
      $("quota-fill").className = "fill" + (pct >= 95 ? " full" : pct >= 80 ? " warn" : "");
      $("quota-text").textContent = bytes(q.used) + " of " + bytes(q.limit) + " used (" + Math.round(pct) + "%)";
      $("quota-free").textContent = bytes(Math.max(q.limit - q.used, 0)) + " free";
    } else {
      meter($("quota-bar"), $("quota-fill"), 0);
      $("quota-text").textContent = bytes(q.used) + " used";
      $("quota-free").textContent = "Unlimited";
    }
  }
  const p = s.progress || {};
  const active = s.state === "syncing" && p.phase;
  $("progress-box").hidden = !active;
  if (active) {
    const pct = p.bytesTotal ? (p.bytesDone / p.bytesTotal) * 100 : 0;
    meter($("progress-bar"), $("progress-fill"), pct);
    $("progress-phase").textContent = { checking: "Checking files…", uploading: "Uploading…", restoring: "Restoring…" }[p.phase] || "Working…";
    $("progress-text").textContent = p.filesDone + " / " + p.filesTotal + " files · " + bytes(p.bytesDone) + " / " + bytes(p.bytesTotal);
  }
  const r = s.lastRun;
  if (r) {
    $("stat-finished").textContent = when(r.finished);
    $("stat-duration").textContent = duration(r.seconds);
    $("stat-uploaded").textContent = r.uploaded + " files";
    $("stat-files").textContent = String(r.files);
    $("stat-bytes").textContent = bytes(r.bytes);
    $("stat-errors").textContent = String(r.errors);
    $("stat-errors").className = r.errors ? "bad" : "";
  }
  const issues = s.issues || [];
  $("issues-card").hidden = !issues.length;
  $("issues").replaceChildren(...issues.map((i) => {
    const li = document.createElement("li");
    li.textContent = new Date(i.time).toLocaleTimeString() + "  " + i.message;
    return li;
  }));
}

function showError(msg) {
  $("error").textContent = msg;
  $("error").hidden = !msg;
}

function render(s) {
  current = s;
  applyOS(s.os);
  $("dot").className = "dot " + s.state;
  $("message").textContent = s.message || s.state;
  const parts = ["Folder: " + short(s.root)];
  if (s.lastSync && !s.lastSync.startsWith("0001")) {
    parts.push("Last sync: " + when(s.lastSync));
  }
  $("detail").textContent = parts.join(" · ");
  showError(s.lastError);
  $("setup").hidden = s.hasCredentials;
  $("signin").hidden = !s.hasCredentials || s.loggedIn;
  $("login").disabled = s.state === "login";
  $("login-fallback").hidden = !s.loginUrl;
  if (s.loginUrl && s.loginUrl.startsWith("https://accounts.google.com/")) {
    $("login-link").href = s.loginUrl;
  }
  const ready = s.hasCredentials && s.loggedIn;
  $("sync").disabled = !ready || s.paused || s.state === "syncing";
  $("pause").disabled = !ready;
  $("pause-label").textContent = s.paused ? "Resume" : "Pause";
  $("pause-icon").setAttribute("href", s.paused ? "#i-play" : "#i-pause");
  $("pill-text").textContent = { idle: "Up to date", syncing: "Working", paused: "Paused", error: "Needs attention", setup: "Setup", signin: "Sign in", login: "Signing in" }[s.state] || "Starting";
  $("settings").hidden = !ready;
  $("restore-card").hidden = !ready;
  $("root").textContent = short(s.root);
  $("twoway").checked = s.twoWay;
  $("startup").checked = s.startAtLogin;
  $("autoupdate").checked = s.autoUpdate;
  $("version").textContent = s.version;
  $("app-name").textContent = s.appName;
  $("footer-name").textContent = s.appName;
  document.title = s.appName;
  $("update").hidden = !s.updateAvailable;
  $("update").disabled = s.updating;
  $("update-label").textContent = s.updating ? "Updating…" : "Update to " + s.updateAvailable;
  if (document.activeElement !== $("every")) {
    $("every").value = String(s.intervalMinutes);
  }
  renderStats(s);
  $("target").textContent = s.target;
  document.querySelectorAll('input[name="limit"]').forEach((el) => {
    el.checked = Number(el.value) === s.storageLimit;
  });
  document.querySelectorAll('input[name="speed"]').forEach((el) => {
    el.checked = Number(el.value) === s.speedLimitKB;
  });
  const folders = $("folders");
  folders.replaceChildren(...(s.folders || []).map((f) => {
    const li = document.createElement("li");
    const name = document.createElement("span");
    name.textContent = short(f);
    const sz = (s.folderSizes || {})[f];
    if (sz) {
      const meta = document.createElement("span");
      meta.className = "meta";
      meta.textContent = " · " + sz.files.toLocaleString() + " files · " + bytes(sz.bytes);
      name.append(meta);
    }
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "secondary";
    remove.append(icon("x"));
    remove.setAttribute("aria-label", "Stop backing up " + f);
    remove.addEventListener("click", () => {
      if (window.confirm("Stop backing up " + short(f) + "? Files already in Google Drive stay there.")) {
        saveSettings({ folders: (current.folders || []).filter((x) => x !== f) });
      }
    });
    li.append(icon("folder"), name, remove);
    return li;
  }));
  if (!(s.folders || []).length) {
    const li = document.createElement("li");
    li.textContent = "No folders yet. Pick a suggestion or add your own.";
    folders.append(li);
  }
  const suggestions = s.suggestions || [];
  $("suggest").hidden = !suggestions.length;
  $("suggest-list").replaceChildren(...suggestions.map((dir) => {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "secondary";
    b.setAttribute("aria-label", "Back up " + short(dir));
    b.append(icon("plus"), document.createTextNode(dir.split(/[\\/]/).pop()));
    b.disabled = measuring;
    b.addEventListener("click", () => addFolder(dir));
    return b;
  }));
  const excludes = $("excludes");
  excludes.replaceChildren(...(s.exclude || []).map((p) => {
    const li = document.createElement("li");
    const name = document.createElement("span");
    name.textContent = short(p);
    const remove = document.createElement("button");
    remove.type = "button";
    remove.className = "secondary";
    remove.setAttribute("aria-label", "Stop skipping " + p);
    remove.append(icon("x"));
    remove.addEventListener("click", () => saveSettings({ exclude: (current.exclude || []).filter((x) => x !== p) }));
    li.append(icon("ban"), name, remove);
    return li;
  }));
  const list = $("recent");
  list.replaceChildren(...s.recent.map((line) => {
    const li = document.createElement("li");
    li.textContent = line;
    return li;
  }));
  if (!s.recent.length) {
    const li = document.createElement("li");
    li.textContent = "Nothing yet";
    list.append(li);
  }
}

async function refresh() {
  try {
    const res = await fetch("/api/status");
    render(await res.json());
  } catch {
    $("dot").className = "dot error";
    $("message").textContent = "GoBridgeSync is not running";
  }
}

async function act(path, body) {
  try {
    render(await post(path, body));
  } catch (e) {
    showError(e.message);
  }
}

let measuring = false;

async function addFolder(dir) {
  const add = document.querySelector('#add-form button[type="submit"]');
  measuring = true;
  add.disabled = true;
  document.querySelectorAll("#suggest-list button").forEach((c) => { c.disabled = true; });
  $("add-folder").disabled = true;
  showError("");
  add.setAttribute("aria-label", "Measuring " + short(dir));
  add.classList.add("busy");
  try {
    const m = await call("/api/measure", { path: dir });
    const size = m.files.toLocaleString() + " files, " + bytes(m.bytes);
    if (m.known && !m.fits) {
      if (!window.confirm(short(dir) + " is " + size + ", but only " + bytes(m.free) + " of your Drive storage is free (before files still waiting from other folders).\n\nAdd it anyway? Files that don't fit won't be uploaded.")) {
        return false;
      }
    }
    await saveSettings({ folders: [...(current.folders || []), dir] });
    return true;
  } catch (e) {
    showError(e.message);
    return false;
  } finally {
    measuring = false;
    add.disabled = false;
    document.querySelectorAll("#suggest-list button").forEach((c) => { c.disabled = false; });
    $("add-folder").disabled = false;
    add.setAttribute("aria-label", "Add folder");
    add.classList.remove("busy");
  }
}

function saveSettings(changes) {
  return act("/api/settings", JSON.stringify(changes));
}

document.querySelectorAll('input[name="limit"]').forEach((el) => {
  el.addEventListener("change", () => {
    if (current) {
      saveSettings({ storageLimit: Number(el.value) });
    }
  });
});

$("add-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const value = $("add-folder").value.trim();
  if (value && current) {
    if (await addFolder(value)) {
      $("add-folder").value = "";
    }
  }
});
let pickerPath = "";
let pickerTarget = "add-folder";

async function browse(path) {
  $("picker-error").hidden = true;
  try {
    const res = await fetch("/api/dirs", {
      method: "POST",
      headers: { "X-GBS-Token": token },
      body: JSON.stringify({ path }),
    });
    const data = await res.json();
    if (!res.ok) {
      throw new Error(data.error || "Cannot open folder");
    }
    pickerPath = data.path;
    $("picker-path").value = data.path;
    $("picker-up").disabled = !data.parent;
    $("picker-up").dataset.parent = data.parent || "";
    const list = $("picker-list");
    list.replaceChildren(...data.dirs.map((d) => {
      const li = document.createElement("li");
      const b = document.createElement("button");
      b.type = "button";
      b.append(icon("folder"), document.createTextNode(d.name));
      b.addEventListener("click", () => browse(d.path));
      li.append(b);
      return li;
    }));
    if (!data.dirs.length) {
      const li = document.createElement("li");
      li.className = "empty";
      li.textContent = "No subfolders";
      list.append(li);
    }
  } catch (e) {
    $("picker-error").textContent = e.message;
    $("picker-error").hidden = false;
  }
}

function openPicker(target) {
  pickerTarget = target;
  $("picker").showModal();
  const start = $(target).value.trim();
  browse(start.startsWith("/") || start.startsWith("~") ? start : "");
}

$("browse").addEventListener("click", () => openPicker("add-folder"));
$("exclude-browse").addEventListener("click", () => openPicker("exclude-input"));
$("exclude-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const value = $("exclude-input").value.trim();
  if (value && current) {
    await saveSettings({ exclude: [...(current.exclude || []), value] });
    if ($("error").hidden) {
      $("exclude-input").value = "";
    }
  }
});
$("picker-up").addEventListener("click", (e) => browse(e.currentTarget.dataset.parent));
$("picker-go").addEventListener("click", () => browse($("picker-path").value));
$("picker-path").addEventListener("keydown", (e) => {
  if (e.key === "Enter") {
    e.preventDefault();
    browse($("picker-path").value);
  }
});
$("picker-cancel").addEventListener("click", () => $("picker").close());
$("picker-select").addEventListener("click", () => {
  $(pickerTarget).value = pickerPath;
  $("picker").close();
  $(pickerTarget).focus();
});
document.querySelectorAll('input[name="speed"]').forEach((el) => {
  el.addEventListener("change", () => saveSettings({ speedLimitKB: Number(el.value) }));
});
$("startup").addEventListener("change", (e) => saveSettings({ startAtLogin: e.target.checked }));
$("signout").addEventListener("click", () => {
  if (window.confirm("Sign out of Google Drive? Backup and sync stop until you sign in again. Your files stay on this computer and in Drive.\n\nIf you will sign in with a different account, move the files out of " + short(current.root) + " first, or they will be uploaded to the new account.")) {
    act("/api/signout");
  }
});
$("autoupdate").addEventListener("change", (e) => saveSettings({ autoUpdate: e.target.checked }));
$("update").addEventListener("click", () => act("/api/update"));
$("every").addEventListener("change", (e) => saveSettings({ intervalMinutes: Number(e.target.value) }));
$("twoway").addEventListener("change", (e) => {
  if (current) {
    saveSettings({ twoWay: e.target.checked });
  }
});
let remotePath = "";

async function call(path, body) {
  const res = await fetch(path, { method: "POST", headers: { "X-GBS-Token": token }, body: JSON.stringify(body) });
  const data = await res.json().catch(() => ({}));
  if (!res.ok) {
    throw new Error(data.error || "Request failed");
  }
  return data;
}

function restoreError(msg) {
  $("restore-error").textContent = msg || "";
  $("restore-error").hidden = !msg;
}

function smallButton(label, iconName, onClick) {
  const b = document.createElement("button");
  b.type = "button";
  b.className = "secondary small-btn";
  b.append(icon(iconName), document.createTextNode(label));
  b.addEventListener("click", onClick);
  return b;
}

async function startRestore(job) {
  try {
    await call("/api/restore", { ...job, dest: $("restore-dest").value });
    $("restore").close();
    refresh();
  } catch (e) {
    restoreError(e.message);
  }
}

async function showVersions(li, entry) {
  const old = li.querySelector(".versions");
  if (old) {
    old.remove();
    return;
  }
  const ul = document.createElement("ul");
  ul.className = "versions";
  li.append(ul);
  try {
    const versions = await call("/api/versions", { fileId: entry.id });
    if (!versions.length) {
      ul.textContent = "No older versions";
    }
    versions.forEach((v) => {
      const row = document.createElement("li");
      row.append(document.createTextNode(new Date(v.modified).toLocaleString() + " · " + bytes(v.size) + " "));
      row.append(smallButton("Restore", "restore", () => startRestore({ fileId: entry.id, revision: v.id, name: entry.name })));
      ul.append(row);
    });
  } catch (e) {
    ul.textContent = e.message;
  }
}

async function browseRemote(dir) {
  restoreError("");
  try {
    const entries = await call("/api/backups", { path: dir });
    remotePath = dir;
    $("restore-path").textContent = dir ? "My Drive / " + dir.split("/").join(" / ") : "My Drive";
    $("restore-up").disabled = !dir;
    $("restore-here").disabled = !dir;
    const list = $("restore-list");
    list.replaceChildren(...entries.map((e) => {
      const li = document.createElement("li");
      const name = document.createElement("button");
      name.type = "button";
      name.className = "name";
      name.append(icon(e.dir ? "folder" : "file"), document.createTextNode(e.name));
      if (e.dir) {
        name.addEventListener("click", () => browseRemote(e.path));
        li.append(name);
      } else {
        name.addEventListener("click", () => showVersions(li, e));
        const meta = document.createElement("span");
        meta.className = "meta";
        meta.textContent = bytes(e.size);
        li.append(name, meta, smallButton("Versions", "clock", () => showVersions(li, e)), smallButton("Restore", "restore", () => startRestore({ source: e.path })));
      }
      return li;
    }));
    if (!entries.length) {
      const li = document.createElement("li");
      li.className = "empty";
      li.textContent = "Empty";
      list.append(li);
    }
  } catch (e) {
    restoreError(e.message);
  }
}

$("restore-open").addEventListener("click", () => {
  $("restore").showModal();
  browseRemote(remotePath);
});
$("restore-up").addEventListener("click", () => browseRemote(remotePath.includes("/") ? remotePath.slice(0, remotePath.lastIndexOf("/")) : ""));
$("restore-here").addEventListener("click", () => startRestore({ source: remotePath }));
$("restore-close").addEventListener("click", () => $("restore").close());
$("clear-issues").addEventListener("click", () => act("/api/clear-issues"));
$("sync").addEventListener("click", () => act("/api/sync"));
$("open").addEventListener("click", () => act("/api/open-folder"));
$("login").addEventListener("click", () => act("/api/login"));
$("pause").addEventListener("click", () => act(current && current.paused ? "/api/resume" : "/api/pause"));
$("credentials").addEventListener("change", async (e) => {
  const file = e.target.files[0];
  if (file) {
    await act("/api/credentials", await file.text());
    e.target.value = "";
  }
});

refresh();
setInterval(refresh, 2000);

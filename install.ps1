$ErrorActionPreference = 'Stop'
$repo = if ($env:GBS_REPO) { $env:GBS_REPO } else { 'GodYtteria/gobridgesync-desktop' }
$base = if ($env:GBS_BASE_URL) { $env:GBS_BASE_URL } else { "https://github.com/$repo/releases/latest/download" }
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
if ($env:GBS_EDITION -eq 'bukluran') {
    $app, $title, $folder, $port = 'gobridgesync-bukluran', 'GoBridgeSync Bukluran', 'GoogleDrive', 47812
} else {
    $app, $title, $folder, $port = 'gobridgesync', 'GoBridgeSync', 'GoBridgeSync', 47811
}
$name = "$app-windows-$arch.zip"
$tmp = Join-Path ([IO.Path]::GetTempPath()) ([Guid]::NewGuid())
New-Item -ItemType Directory -Path $tmp | Out-Null
try {
    Write-Host "Downloading $name"
    Invoke-WebRequest "$base/$name" -OutFile "$tmp\$name" -UseBasicParsing
    Invoke-WebRequest "$base/checksums.txt" -OutFile "$tmp\checksums.txt" -UseBasicParsing
    $want = (Get-Content "$tmp\checksums.txt" | Where-Object { ($_ -split '\s+')[1] -eq $name } | ForEach-Object { ($_ -split '\s+')[0] })
    if (-not $want) { throw "No checksum for $name" }
    $got = (Get-FileHash "$tmp\$name" -Algorithm SHA256).Hash.ToLower()
    if ($got -ne $want.ToLower()) { throw "Checksum mismatch for $name" }

    $dir = Join-Path $env:LOCALAPPDATA $title
    Get-Process $app -ErrorAction SilentlyContinue | Stop-Process -Force
    New-Item -ItemType Directory -Force -Path $dir | Out-Null
    Expand-Archive "$tmp\$name" -DestinationPath $dir -Force
    $exe = Join-Path $dir "$app.exe"
    New-Item -ItemType Directory -Force -Path (Join-Path $env:USERPROFILE $folder) | Out-Null

    $shell = New-Object -ComObject WScript.Shell
    $link = $shell.CreateShortcut((Join-Path ([Environment]::GetFolderPath('Programs')) "$title.lnk"))
    $link.TargetPath = $exe
    $link.Arguments = 'ui -tray'
    $link.IconLocation = Join-Path $dir 'gobridgesync.ico'
    $link.Save()
    Start-Process $exe -ArgumentList 'ui', '-tray'
    Write-Host "$title installed. Open it from the Start menu, the tray, or http://127.0.0.1:$port/"
}
finally {
    Remove-Item -Recurse -Force $tmp
}

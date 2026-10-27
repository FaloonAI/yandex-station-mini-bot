param(
    [string]$TargetIP = "192.168.20.209",
    [int]$TargetPort = 2222,
    [string]$KeyPath = "$HOME\.ssh\yandex-mini\id_ed25519",
    [string]$BotToken = "8989354711:AAEGHljhkRSEK7aJRgqsgwHwK4-LSqRzqBk",
    [int64]$AdminID = 1184490028
)

$ErrorActionPreference = "Stop"

Write-Host "==========================================================" -ForegroundColor Cyan
Write-Host "  Yandex Station Mini Bot Deployment                      " -ForegroundColor Cyan
Write-Host "==========================================================" -ForegroundColor Cyan

$ProjectRoot = Resolve-Path "$PSScriptRoot\.."
Set-Location $ProjectRoot

# 1. Build Linux ARM64 Binary
Write-Host "[1/5] Compiling Go binary for Linux arm64..." -ForegroundColor Yellow
$env:GOOS = "linux"
$env:GOARCH = "arm64"
go build -ldflags="-s -w" -o bin\yandex-mini-bot .\cmd\bot
if ($LASTEXITCODE -ne 0) {
    Write-Error "Compilation failed!"
    exit 1
}
Write-Host "Binary compiled: bin\yandex-mini-bot ($( (Get-Item bin\yandex-mini-bot).Length / 1MB | ForEach-Object { '{0:N2} MB' -f $_ } ))" -ForegroundColor Green

# 2. Prepare Remote Directories
Write-Host "[2/5] Creating remote directories on Yandex Station Mini..." -ForegroundColor Yellow
$SSH_CMD = "ssh -i `"$KeyPath`" -p $TargetPort -o StrictHostKeyChecking=no root@$TargetIP"
cmd /c "$SSH_CMD `"mkdir -p /bkupgrade/data/telegram-bot/sounds /bkupgrade/data/telegram-bot/scripts`""

# 3. Transfer Binary, Scripts & Config
Write-Host "[3/5] Transferring binary, scripts and sounds..." -ForegroundColor Yellow

# Helper function to stream file via SSH stdin
function Send-FileOverSSH {
    param([string]$LocalPath, [string]$RemotePath)
    cmd /c "type `"$LocalPath`" | $SSH_CMD `"cat > '$RemotePath'`""
}

Write-Host "  -> Uploading yandex-mini-bot binary..."
Send-FileOverSSH "$ProjectRoot\bin\yandex-mini-bot" "/bkupgrade/data/telegram-bot/yandex-mini-bot"

Write-Host "  -> Uploading service start.sh..."
Send-FileOverSSH "$ProjectRoot\scripts\start.sh" "/bkupgrade/data/telegram-bot/start.sh"

Write-Host "  -> Uploading record_helper.sh..."
Send-FileOverSSH "$ProjectRoot\scripts\record_helper.sh" "/bkupgrade/data/telegram-bot/scripts/record_helper.sh"

# Upload Soundboard files
Write-Host "  -> Uploading sounds..."
Get-ChildItem "$ProjectRoot\sounds\*.wav" | ForEach-Object {
    $name = $_.Name
    Send-FileOverSSH $_.FullName "/bkupgrade/data/telegram-bot/sounds/$name"
}

# Generate and upload config.json
Write-Host "  -> Generating and uploading config.json..."
$configContent = @"
{
  "bot_token": "$BotToken",
  "admin_id": $AdminID,
  "sounds_dir": "/data/telegram-bot/sounds",
  "scripts_dir": "/data/telegram-bot/scripts",
  "log_path": "/data/telegram-bot/bot.log",
  "quasar_logs": "/data/quasar/daemons_logs/maind.log",
  "aplay_path": "/system/workdir/bin/aplay",
  "arecord_path": "/system/workdir/bin/arecord",
  "amixer_path": "/system/workdir/bin/amixer"
}
"@
$tempConfig = [System.IO.Path]::GetTempFileName()
$configContent | Out-File -FilePath $tempConfig -Encoding ascii
Send-FileOverSSH $tempConfig "/bkupgrade/data/telegram-bot/config.json"
Remove-Item $tempConfig -Force

# 4. Set Permissions & Hook Auto-start
Write-Host "[4/5] Setting permissions and auto-start hook..." -ForegroundColor Yellow
$setupScript = @'
chmod +x /bkupgrade/data/telegram-bot/yandex-mini-bot
chmod +x /bkupgrade/data/telegram-bot/start.sh
chmod +x /bkupgrade/data/telegram-bot/scripts/record_helper.sh

# Hook into /data/alice-homebrew/start.sh if not already present
if [ -f /data/alice-homebrew/start.sh ]; then
    if ! grep -q "telegram-bot" /data/alice-homebrew/start.sh; then
        echo '[ -x /data/telegram-bot/start.sh ] && /data/telegram-bot/start.sh "$1" >/dev/null 2>&1 || true' >> /data/alice-homebrew/start.sh
    fi
fi
'@
cmd /c "$SSH_CMD `"$setupScript`""

# 5. Start Service
Write-Host "[5/5] Starting bot service on speaker..." -ForegroundColor Yellow
cmd /c "$SSH_CMD `"/bkupgrade/data/telegram-bot/start.sh restart`""

Write-Host "==========================================================" -ForegroundColor Cyan
Write-Host "  Deployment Complete!                                    " -ForegroundColor Green
Write-Host "  Open Telegram and check messages from your bot.         " -ForegroundColor Green
Write-Host "==========================================================" -ForegroundColor Cyan

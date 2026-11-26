# Автозапуск раннера (WSL + sshd :2222) при старте Windows, без входа пользователя.
# Запускать в PowerShell ОТ АДМИНИСТРАТОРА.
$ErrorActionPreference = 'Stop'
$Distro = 'Ubuntu-24.04'
$admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $admin) { Write-Host 'Запусти PowerShell от администратора и повтори.' -ForegroundColor Red; return }

$cfg = Join-Path $env:USERPROFILE '.wslconfig'
$Mirrored = (Test-Path $cfg) -and ((Get-Content $cfg -Raw) -match 'networkingMode\s*=\s*mirrored')
if ($Mirrored) {
  $wslCmd = "wsl.exe -d $Distro -u root --exec sleep infinity"
} else {
  $wslCmd = "`$ip=((wsl.exe -d $Distro -u root -- hostname -I) -join '').Trim().Split(' ')[0]; netsh interface portproxy delete v4tov4 listenport=2222 listenaddress=0.0.0.0; netsh interface portproxy add v4tov4 listenport=2222 listenaddress=0.0.0.0 connectport=2222 connectaddress=`$ip; wsl.exe -d $Distro -u root --exec sleep infinity"
}
$act = New-ScheduledTaskAction -Execute 'powershell.exe' -Argument "-NoProfile -WindowStyle Hidden -Command $wslCmd"
# При старте системы (через 1 мин, чтобы поднялась сеть) и запасной — при входе
$boot = New-ScheduledTaskTrigger -AtStartup; $boot.Delay = 'PT1M'
$logon = New-ScheduledTaskTrigger -AtLogOn -User $env:USERNAME
# S4U: задача идёт от твоего пользователя без входа и без сохранения пароля
$prn = New-ScheduledTaskPrincipal -UserId "$env:USERDOMAIN\$env:USERNAME" -LogonType S4U -RunLevel Highest
$set = New-ScheduledTaskSettingsSet -ExecutionTimeLimit 0 -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) -MultipleInstances IgnoreNew
Register-ScheduledTask -TaskName 'LaneRunnerWSL' -Action $act -Trigger @($boot, $logon) -Settings $set -Principal $prn -Force | Out-Null
Write-Host 'Задача LaneRunnerWSL: при старте системы + при входе, без пароля.' -ForegroundColor Green

# Запустить сейчас и проверить
Get-ScheduledTask -TaskName 'LaneRunnerWSL' | Stop-ScheduledTask -ErrorAction SilentlyContinue
Start-ScheduledTask -TaskName 'LaneRunnerWSL'
$ok = $false
foreach ($i in 1..20) { Start-Sleep 3; if ((Test-NetConnection 127.0.0.1 -Port 2222 -WarningAction SilentlyContinue).TcpTestSucceeded) { $ok = $true; break } }
if ($ok) { Write-Host 'ГОТОВО: sshd на 2222 отвечает. Напиши в чат: ноут готов' -ForegroundColor Green }
else { Write-Host 'sshd на 2222 не ответил за минуту - пришли скрин в чат.' -ForegroundColor Red }

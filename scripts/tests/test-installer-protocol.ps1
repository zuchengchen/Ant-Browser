[CmdletBinding()]
param([Parameter(Mandatory)][string]$Installer)

$ErrorActionPreference = 'Stop'
$protocol = 'Software\Classes\ant-browser'
$command = "$protocol\shell\open\command"
$uninstall = 'Software\Microsoft\Windows\CurrentVersion\Uninstall\AntBrowser'
$view = [Microsoft.Win32.RegistryView]::Registry32
$user = [Microsoft.Win32.RegistryKey]::OpenBaseKey('CurrentUser', $view)
$machine = [Microsoft.Win32.RegistryKey]::OpenBaseKey('LocalMachine', $view)
$userDir = Join-Path $env:LOCALAPPDATA 'Programs\Ant Browser'
$machineDir = Join-Path $env:TEMP "Ant Browser machine $([guid]::NewGuid())"
$foreign = '"C:\other browser\app.exe" "%1"'

function Read-Entry($hive, $path, $name) {
    $key = $hive.OpenSubKey($path)
    if ($null -ne $key) {
        try { return $key.GetValue($name) } finally { $key.Dispose() }
    }
}

function Write-Entry($hive, $path, $name, $value) {
    $key = $hive.CreateSubKey($path)
    try { $key.SetValue($name, $value) } finally { $key.Dispose() }
}

function Assert-Entry($actual, $expected, $message) {
    if ($actual -ne $expected) { throw "$message. Got [$actual], expected [$expected]." }
}

function Invoke-Installer($path, $arguments) {
    $p = Start-Process -FilePath $path -ArgumentList $arguments -PassThru
    if (-not $p.WaitForExit(120000)) { $p.Kill(); throw "Timed out running $path" }
    if ($p.ExitCode -ne 0) { throw "$path exited with $($p.ExitCode)" }
}

function Assert-Installed($hive, $dir) {
    Assert-Entry (Read-Entry $hive $uninstall 'InstallLocation') $dir 'InstallLocation'
    Assert-Entry (Read-Entry $hive $protocol 'URL Protocol') '' 'URL Protocol marker'
    Assert-Entry (Read-Entry $hive $command '') ('"{0}\ant-chrome.exe" "%1"' -f $dir) 'Protocol command'
}

# This test exercises the actual installer on a fresh Windows runner.
foreach ($hive in @($user, $machine)) {
    foreach ($key in @($protocol, $uninstall)) {
        if ($null -ne $hive.OpenSubKey($key)) { throw "Test requires an unused registry key: $hive\$key" }
    }
}
if ((Test-Path $userDir) -or (Test-Path "${env:ProgramW6432}\Ant Browser")) {
    throw 'Test requires a fresh Ant Browser installation.'
}

try {
    Invoke-Installer $Installer '/S'
    Assert-Installed $user $userDir
    Assert-Entry (Read-Entry $machine $command '') $null 'Fresh install changed machine handler'

    Write-Entry $machine $uninstall 'InstallLocation' $machineDir
    Write-Entry $machine $command '' $foreign
    Invoke-Installer (Join-Path $userDir 'Uninstall.exe') "/S _?=$userDir"
    Assert-Entry (Read-Entry $user $command '') $null 'User handler left behind'
    Assert-Entry (Read-Entry $user $uninstall 'InstallLocation') $null 'User uninstall entry left behind'
    Assert-Entry (Read-Entry $machine $command '') $foreign 'User uninstall changed machine handler'
    Assert-Entry (Read-Entry $machine $uninstall 'InstallLocation') $machineDir 'User uninstall removed machine install'

    Write-Entry $user $uninstall 'InstallLocation' $userDir
    Write-Entry $user $command '' $foreign
    Invoke-Installer $Installer '/S'
    Assert-Installed $machine $machineDir
    Assert-Entry (Read-Entry $user $command '') $foreign 'Machine upgrade changed user handler'

    Write-Entry $machine $command '' $foreign
    Invoke-Installer (Join-Path $machineDir 'Uninstall.exe') "/S _?=$machineDir"
    Assert-Entry (Read-Entry $machine $command '') $foreign 'Machine uninstall removed a reassigned handler'
    Assert-Entry (Read-Entry $machine $uninstall 'InstallLocation') $null 'Machine uninstall entry left behind'
    Assert-Entry (Read-Entry $user $command '') $foreign 'Machine uninstall changed user handler'
    Assert-Entry (Read-Entry $user $uninstall 'InstallLocation') $userDir 'Machine uninstall removed user install'
} finally {
    foreach ($hive in @($user, $machine)) {
        $hive.DeleteSubKeyTree($protocol, $false)
        $hive.DeleteSubKeyTree($uninstall, $false)
        $hive.Dispose()
    }
    foreach ($dir in @($machineDir, $userDir)) {
        if (Test-Path $dir) { Remove-Item -LiteralPath $dir -Recurse -Force }
    }
}

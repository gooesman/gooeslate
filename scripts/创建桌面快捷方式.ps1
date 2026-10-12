$ErrorActionPreference = 'Stop'
$applicationDirectory = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
if (Test-Path -LiteralPath (Join-Path $PSScriptRoot 'gooeslate.exe') -PathType Leaf) {
    $applicationDirectory = $PSScriptRoot
}
$applicationExecutable = Join-Path $applicationDirectory 'gooeslate.exe'
$applicationIcon = Join-Path $applicationDirectory 'gooeslate.ico'
if (!(Test-Path -LiteralPath $applicationExecutable -PathType Leaf)) {
    throw '找不到 gooeslate.exe，请在解压后的程序目录运行此脚本。'
}
$desktopDirectory = [Environment]::GetFolderPath('DesktopDirectory')
$shortcutPath = Join-Path $desktopDirectory '鹅译.lnk'
$legacyExecutable = Join-Path $applicationDirectory 'gooestranslate.exe'
$shortcutShell = New-Object -ComObject WScript.Shell
if (Test-Path -LiteralPath $shortcutPath) {
    $existingShortcut = $shortcutShell.CreateShortcut($shortcutPath)
    if (![string]::Equals($existingShortcut.TargetPath,$applicationExecutable,[StringComparison]::OrdinalIgnoreCase) -and ![string]::Equals($existingShortcut.TargetPath,$legacyExecutable,[StringComparison]::OrdinalIgnoreCase)) {
        throw '桌面已有指向其它位置的鹅译快捷方式，请重命名它后重试。'
    }
}
$shortcut = $shortcutShell.CreateShortcut($shortcutPath)
$shortcut.TargetPath = $applicationExecutable
$shortcut.WorkingDirectory = $applicationDirectory
$shortcut.Description = '鹅译 · Gooeslate · 截图识别、翻译与查词'
$shortcut.IconLocation = if (Test-Path -LiteralPath $applicationIcon -PathType Leaf) { $applicationIcon + ',0' } else { $applicationExecutable + ',0' }
$shortcut.Save()
$legacyShortcutPath = Join-Path $desktopDirectory 'gooestranslate.lnk'
if (Test-Path -LiteralPath $legacyShortcutPath) {
    $legacyShortcut = $shortcutShell.CreateShortcut($legacyShortcutPath)
    if ([string]::Equals($legacyShortcut.TargetPath,$legacyExecutable,[StringComparison]::OrdinalIgnoreCase) -or [string]::Equals($legacyShortcut.TargetPath,$applicationExecutable,[StringComparison]::OrdinalIgnoreCase)) {
        Remove-Item -LiteralPath $legacyShortcutPath -Force
    }
}
if (!('GooeslateShortcutNotify' -as [type])) {
    Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
public static class GooeslateShortcutNotify {
    [DllImport("shell32.dll", ExactSpelling=true, CharSet=CharSet.Unicode)]
    public static extern void SHChangeNotify(int change, uint flags, string path, IntPtr unused);
}
"@
}
foreach ($changedPath in @($applicationIcon,$applicationExecutable,$shortcutPath)) {
    [GooeslateShortcutNotify]::SHChangeNotify(0x2000,5,$changedPath,[IntPtr]::Zero)
}
Write-Output ('已创建桌面快捷方式：' + $shortcutPath)

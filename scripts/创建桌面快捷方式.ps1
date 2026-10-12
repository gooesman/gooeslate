$ErrorActionPreference = 'Stop'
$applicationDirectory = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
# In a release ZIP this script sits beside the executable.
if (Test-Path -LiteralPath (Join-Path $PSScriptRoot 'gooestranslate.exe') -PathType Leaf) {
    $applicationDirectory = $PSScriptRoot
}
$applicationExecutable = Join-Path $applicationDirectory 'gooestranslate.exe'
$applicationIcon = Join-Path $applicationDirectory 'gooestranslate-goose.ico'
if (!(Test-Path -LiteralPath $applicationIcon -PathType Leaf)) {
    $applicationIcon = Join-Path $applicationDirectory 'gooestranslate.ico'
}
if (!(Test-Path -LiteralPath $applicationExecutable -PathType Leaf)) {
    throw '找不到 gooestranslate.exe，请在解压后的程序目录运行此脚本。'
}
$desktopDirectory = [Environment]::GetFolderPath('DesktopDirectory')
$shortcutPath = Join-Path $desktopDirectory 'gooestranslate.lnk'
$shortcutShell = New-Object -ComObject WScript.Shell
if (Test-Path -LiteralPath $shortcutPath) {
    $existingShortcut = $shortcutShell.CreateShortcut($shortcutPath)
    if (![string]::Equals($existingShortcut.TargetPath,$applicationExecutable,[StringComparison]::OrdinalIgnoreCase)) {
        throw '桌面已有指向其它位置的 gooestranslate 快捷方式，请重命名它后重试。'
    }
}
$shortcut = $shortcutShell.CreateShortcut($shortcutPath)
$shortcut.TargetPath = $applicationExecutable
$shortcut.WorkingDirectory = $applicationDirectory
$shortcut.Description = 'gooestranslate · 截图识别、翻译与查词'
if (Test-Path -LiteralPath $applicationIcon -PathType Leaf) {
    $shortcut.IconLocation = $applicationIcon + ',0'
} else {
    $shortcut.IconLocation = $applicationExecutable + ',0'
}
$shortcut.Save()
if (!('GooesTranslateShortcutNotify' -as [type])) {
    Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
public static class GooesTranslateShortcutNotify {
    [DllImport("shell32.dll", ExactSpelling=true, CharSet=CharSet.Unicode)]
    public static extern void SHChangeNotify(int change, uint flags, string path, IntPtr unused);
}
"@
}
foreach ($changedPath in @($applicationIcon,$applicationExecutable,$shortcutPath)) {
    [GooesTranslateShortcutNotify]::SHChangeNotify(0x2000,5,$changedPath,[IntPtr]::Zero)
}
Write-Output ('已创建桌面快捷方式：' + $shortcutPath)

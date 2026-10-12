$ErrorActionPreference = 'Stop'
$repositoryDirectory = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$sourcePath = Join-Path $repositoryDirectory 'assets\app-icon-source.png'
$resourceDirectory = Join-Path $repositoryDirectory 'cmd\gooestranslate\ui'
Add-Type -AssemblyName System.Drawing
$sourceImage = [Drawing.Bitmap]::new($sourcePath)
try {
    if ($sourceImage.Width -ne $sourceImage.Height) { throw 'The application icon source must be square.' }
    $frames = @()
    foreach ($size in @(16,20,24,32,40,48,64,128,256)) {
        $resizedImage = [Drawing.Bitmap]::new($size,$size,[Drawing.Imaging.PixelFormat]::Format32bppArgb)
        try {
            $graphics = [Drawing.Graphics]::FromImage($resizedImage)
            try {
                $graphics.CompositingMode = [Drawing.Drawing2D.CompositingMode]::SourceCopy
                $graphics.CompositingQuality = [Drawing.Drawing2D.CompositingQuality]::HighQuality
                $graphics.InterpolationMode = [Drawing.Drawing2D.InterpolationMode]::HighQualityBicubic
                $graphics.PixelOffsetMode = [Drawing.Drawing2D.PixelOffsetMode]::HighQuality
                $graphics.Clear([Drawing.Color]::Transparent)
                $graphics.DrawImage($sourceImage,[Drawing.Rectangle]::new(0,0,$size,$size),0,0,$sourceImage.Width,$sourceImage.Height,[Drawing.GraphicsUnit]::Pixel)
            } finally { $graphics.Dispose() }
            $pngStream = [IO.MemoryStream]::new()
            try {
                $resizedImage.Save($pngStream,[Drawing.Imaging.ImageFormat]::Png)
                $frames += [pscustomobject]@{Size=$size;Bytes=$pngStream.ToArray()}
                if ($size -eq 256) {
                    [IO.File]::WriteAllBytes((Join-Path $resourceDirectory 'app-icon.png'),$pngStream.ToArray())
                }
            } finally { $pngStream.Dispose() }
        } finally { $resizedImage.Dispose() }
    }
    $iconStream = [IO.MemoryStream]::new()
    $writer = [IO.BinaryWriter]::new($iconStream)
    try {
        $writer.Write([uint16]0)
        $writer.Write([uint16]1)
        $writer.Write([uint16]$frames.Count)
        $offset = 6 + 16 * $frames.Count
        foreach ($frame in $frames) {
            $encodedSize = if ($frame.Size -eq 256) { 0 } else { $frame.Size }
            $writer.Write([byte]$encodedSize)
            $writer.Write([byte]$encodedSize)
            $writer.Write([byte]0)
            $writer.Write([byte]0)
            $writer.Write([uint16]1)
            $writer.Write([uint16]32)
            $writer.Write([uint32]$frame.Bytes.Length)
            $writer.Write([uint32]$offset)
            $offset += $frame.Bytes.Length
        }
        foreach ($frame in $frames) { $writer.Write([byte[]]$frame.Bytes) }
        $writer.Flush()
        [IO.File]::WriteAllBytes((Join-Path $resourceDirectory 'app-icon.ico'),$iconStream.ToArray())
    } finally { $writer.Dispose(); $iconStream.Dispose() }
} finally { $sourceImage.Dispose() }
Push-Location $repositoryDirectory
try {
    # With icon-only generation rsrc assigns group ID 1, used by createAppIcon.
    & go run github.com/akavel/rsrc@v0.10.2 -arch amd64 -ico cmd/gooestranslate/ui/app-icon.ico -o cmd/gooestranslate/rsrc_windows_amd64.syso
    if ($LASTEXITCODE -ne 0) { throw 'Windows icon resource generation failed.' }
} finally { Pop-Location }
Write-Output 'Generated transparent application PNG, nine ICO sizes, and Windows executable resource.'

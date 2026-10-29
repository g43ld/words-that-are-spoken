$ErrorActionPreference = "Stop"

$Root = (Resolve-Path (Join-Path $PSScriptRoot "..\")).Path
$Output = Join-Path $Root "dist\windows"
$Executable = Join-Path $Output "wtas.exe"
$Source = Join-Path $Root "src"

if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw "Go was not found on PATH. Install Go 1.22 or newer and reopen the terminal."
}

foreach ($RequiredPath in @(
    (Join-Path $Root "go.mod"),
    $Source,
    (Join-Path $Root "bin"),
    (Join-Path $Root "models")
)) {
    if (-not (Test-Path -LiteralPath $RequiredPath)) {
        throw "Required path not found: $RequiredPath"
    }
}

if (Test-Path -LiteralPath $Output) {
    Remove-Item -LiteralPath $Output -Recurse -Force
}

New-Item -ItemType Directory -Path $Output | Out-Null

$env:GOOS = "windows"
$env:GOARCH = "amd64"
go build -trimpath -ldflags="-s -w" -o $Executable $Source

Copy-Item -LiteralPath (Join-Path $Root "bin") -Destination $Output -Recurse
Copy-Item -LiteralPath (Join-Path $Root "models") -Destination $Output -Recurse

Write-Host "Windows package created: $Output"

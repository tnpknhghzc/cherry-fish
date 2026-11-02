# Build locallm on Windows with ONE command from plain PowerShell:
#     powershell -ExecutionPolicy Bypass -File scripts\build-windows.ps1
# Installs MSYS2 (via winget) and packages (gcc, cmake, make, go, git), then runs
# `make deps` and `make build` inside the MSYS2 UCRT64 shell. Result: bin\locallm.exe
# Internet is needed only for building.
param([string]$Target = "build", [string]$Tags = "")
$ErrorActionPreference = "Stop"
$repo = Resolve-Path (Join-Path $PSScriptRoot "..")
$msys = "C:\msys64"
$bash = "$msys\usr\bin\bash.exe"

if (-not (Test-Path $bash)) {
    Write-Host "Installing MSYS2..."
    winget install -e --id MSYS2.MSYS2 --accept-package-agreements --accept-source-agreements
    if (-not (Test-Path $bash)) { throw "MSYS2 not found in $msys. Install it from https://www.msys2.org and run this script again." }
}

$env:MSYSTEM = "UCRT64"
$env:CHERE_INVOKING = "1"
Set-Location $repo

Write-Host "Installing packages (gcc, cmake, make, go, git)..."
& $bash -lc "pacman -S --needed --noconfirm git make mingw-w64-ucrt-x86_64-gcc mingw-w64-ucrt-x86_64-cmake mingw-w64-ucrt-x86_64-make mingw-w64-ucrt-x86_64-go perl"
if ($LASTEXITCODE -ne 0) { throw "pacman failed" }

Write-Host "Building llama.cpp (first time: 10-30 minutes)..."
& $bash -lc "make deps"
if ($LASTEXITCODE -ne 0) { throw "make deps failed (send the output)" }

Write-Host "Building locallm..."
& $bash -lc "make $Target TAGS='$Tags'"
if ($LASTEXITCODE -ne 0) { throw "make $Target failed" }

Write-Host "Done: $repo\bin\locallm.exe"

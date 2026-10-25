<#
.SYNOPSIS
    mpv-gotorrent Installer & Updater for Windows (PowerShell)

.DESCRIPTION
    Installs and automatically updates the mpv-gotorrent companion binary and
    Lua script into your mpv configuration directory.

.EXAMPLE
    irm https://raw.githubusercontent.com/biohazard786/mpv-gotorrent/main/install.ps1 | iex

.EXAMPLE
    .\install.ps1 -Force
    .\install.ps1 -Dir "C:\custom\mpv\scripts\mpv-gotorrent"
    .\install.ps1 -Version "v0.1.0"
    .\install.ps1 -Local
#>

[CmdletBinding()]
param (
    [Parameter(Mandatory = $false)]
    [switch]$Force,

    [Parameter(Mandatory = $false)]
    [string]$Dir,

    [Parameter(Mandatory = $false)]
    [string]$Version,

    [Parameter(Mandatory = $false)]
    [switch]$Local,

    [Parameter(Mandatory = $false)]
    [string]$Repo
)

# Set strict mode and error preferences
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest

# Configure security protocols to TLS 1.2 and TLS 1.3
try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12 -bor [Net.SecurityProtocolType]::Tls13
} catch {
    # Fall back if Tls13 is not defined in older .NET versions
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
}

# Defaults & Environment Overrides
$DefaultRepo = "biohazard786/mpv-gotorrent"
if (-not $Repo) {
    if ($env:REPO) { $Repo = $env:REPO } else { $Repo = $DefaultRepo }
}

if (-not $Version) {
    if ($env:VERSION) { $Version = $env:VERSION } else { $Version = "latest" }
}

if ($env:FORCE -eq "1" -or $env:FORCE -eq "true") {
    $Force = [switch]::Present
}

if ($env:LOCAL -eq "1" -or $env:LOCAL -eq "true") {
    $Local = [switch]::Present
}

if (-not $Dir -and $env:MPV_DIR) {
    $Dir = $env:MPV_DIR
}

# Styling helper functions
function Write-Info([string]$Message) {
    Write-Host "[INFO] " -ForegroundColor Cyan -NoNewline
    Write-Host $Message
}

function Write-Success([string]$Message) {
    Write-Host "[OK] " -ForegroundColor Green -NoNewline
    Write-Host $Message
}

function Write-WarnMsg([string]$Message) {
    Write-Host "[WARN] " -ForegroundColor Yellow -NoNewline
    Write-Host $Message
}

function Write-ErrMsg([string]$Message) {
    Write-Host "[ERROR] " -ForegroundColor Red -NoNewline
    Write-Host $Message
}

function Show-Banner {
    $Banner = @"
  __  __ _____ ___      ____ _____ 
 |  \/  |  _ \ \ \    / / _ \_   _|
 | |\/| | |_) | \ \  / / | | || |  
 | |  | |  __/   \ \/ /| |_| || |  
 |_|  |_|_|       \__/  \___/ |_|  mpv-gotorrent
"@
    Write-Host $Banner -ForegroundColor Cyan
    Write-Host " Cross-platform streaming torrent companion for mpv`n" -ForegroundColor DarkGray
}

# Detect CPU Architecture
function Get-Architecture {
    $arch = $env:PROCESSOR_ARCHITECTURE
    switch -Regex ($arch) {
        'AMD64|x64' { return 'amd64' }
        'ARM64'     { return 'arm64' }
        'x86'       { return '386' }
        default     { return 'amd64' }
    }
}

# Detect Target mpv Directory
function Get-MpvDirectory([string]$CustomDir) {
    if ($CustomDir) {
        return $CustomDir
    }

    if ($env:MPV_HOME) {
        return (Join-Path $env:MPV_HOME "scripts\mpv-gotorrent")
    }

    # Check portable mpv in PATH
    $mpvCmd = Get-Command "mpv.exe" -ErrorAction SilentlyContinue
    if ($mpvCmd -and $mpvCmd.Source) {
        $mpvParent = Split-Path (Split-Path $mpvCmd.Source -Parent) -Parent
        $portableScripts = Join-Path $mpvParent "portable_config\scripts"
        if (Test-Path $portableScripts) {
            return (Join-Path $portableScripts "mpv-gotorrent")
        }
    }

    # Check %USERPROFILE%\.config\mpv\scripts
    if ($env:USERPROFILE) {
        $dotConfigScripts = Join-Path $env:USERPROFILE ".config\mpv\scripts"
        if (Test-Path $dotConfigScripts) {
            return (Join-Path $dotConfigScripts "mpv-gotorrent")
        }
    }

    # Standard Windows mpv scripts directory: %APPDATA%\mpv\scripts\mpv-gotorrent
    if ($env:APPDATA) {
        return (Join-Path $env:APPDATA "mpv\scripts\mpv-gotorrent")
    }

    return (Join-Path $HOME "AppData\Roaming\mpv\scripts\mpv-gotorrent")
}

# Check if current working directory is the local repo
function Test-IsLocalRepo {
    $hasMainGo = Test-Path ".\cmd\mpv-gotorrent\main.go"
    $hasMainLua = (Test-Path ".\scripts\main.lua") -or (Test-Path ".\scripts\mpv-gotorrent\main.lua")
    return ($hasMainGo -and $hasMainLua)
}

# Check currently installed version
function Get-InstalledVersion([string]$TargetDir) {
    $versionFile = Join-Path $TargetDir ".version"
    if (Test-Path $versionFile) {
        $content = (Get-Content -Path $versionFile -Raw).Trim()
        if ($content) { return $content }
    }

    $binPath = Join-Path $TargetDir "mpv-gotorrent.exe"
    if (Test-Path $binPath) {
        try {
            $verOutput = (& $binPath -v 2>$null)
            if ($verOutput) {
                $parts = $verOutput.Trim().Split(" ")
                return $parts[-1]
            }
        } catch { }
    }

    return "unknown"
}

# Perform installation from local repository files
function Install-FromLocal([string]$TargetDir) {
    Write-Info "Installing from local repository..."

    $luaSrc = ""
    if (Test-Path ".\scripts\main.lua") {
        $luaSrc = ".\scripts\main.lua"
    } elseif (Test-Path ".\scripts\mpv-gotorrent\main.lua") {
        $luaSrc = ".\scripts\mpv-gotorrent\main.lua"
    } else {
        Write-ErrMsg "Could not locate main.lua in scripts directory."
        exit 1
    }

    $localBin = ".\bin\mpv-gotorrent.exe"
    if (-not (Test-Path $localBin)) {
        $goCmd = Get-Command "go" -ErrorAction SilentlyContinue
        if ($goCmd) {
            Write-Info "Compiling Windows binary using local Go compiler..."
            if (-not (Test-Path ".\bin")) { New-Item -ItemType Directory -Path ".\bin" | Out-Null }
            $gitVer = "v0.1.0-dev"
            try { $gitVer = (git describe --tags --always --dirty 2>$null).Trim() } catch { }
            & go build -ldflags="-s -w -X main.Version=$gitVer" -o $localBin .\cmd\mpv-gotorrent
        } else {
            Write-ErrMsg "Local binary $localBin not found and 'go' compiler is not installed."
            exit 1
        }
    }

    $stamp = "local-dev"
    try { $stamp = (git describe --tags --always --dirty 2>$null).Trim() } catch { }

    if ($isInstalled -and (-not $Force) -and ($currentVersion -eq $stamp)) {
        Write-Success "mpv-gotorrent is already up to date ($currentVersion) in $TargetDir"
        Write-Host "To force reinstallation, pass -Force or set `$env:FORCE=1" -ForegroundColor DarkGray
        exit 0
    }

    if (-not (Test-Path $TargetDir)) {
        New-Item -ItemType Directory -Force -Path $TargetDir | Out-Null
    }

    Copy-Item -Path $luaSrc -Destination (Join-Path $TargetDir "main.lua") -Force
    Copy-Item -Path $localBin -Destination (Join-Path $TargetDir "mpv-gotorrent.exe") -Force
    Set-Content -Path (Join-Path $TargetDir ".version") -Value $stamp -Encoding UTF8

    Write-Host ""
    Write-Success "Successfully installed mpv-gotorrent ($stamp) to $TargetDir"
    Show-Usage $TargetDir
}

function Show-Usage([string]$TargetDir) {
    Write-Host "Installed files in: $TargetDir" -ForegroundColor DarkGray
    Write-Host "  main.lua          - mpv plugin hook script" -ForegroundColor White
    Write-Host "  mpv-gotorrent.exe - Standalone companion stream engine" -ForegroundColor White
    Write-Host ""
    Write-Host "How to stream:" -ForegroundColor White
    Write-Host "  mpv ""magnet:?xt=urn:btih:..."""
    Write-Host "  mpv path\to\movie.torrent"
    Write-Host "  mpv ""https://example.com/stream.torrent"""
    Write-Host ""
    Write-Host "In-player Controls:" -ForegroundColor White
    Write-Host "  Press " -NoNewline
    Write-Host "p" -ForegroundColor Cyan -NoNewline
    Write-Host " in mpv to toggle live swarm download/upload stats."
}

# Main Execution Flow
try {
    Show-Banner

    $Arch = Get-Architecture
    $TargetDir = Get-MpvDirectory $Dir

    Write-Info "Platform:  windows/$Arch"
    Write-Info "Target:    $TargetDir"

    # Check existing installation
    $isInstalled = (Test-Path (Join-Path $TargetDir "main.lua")) -and (Test-Path (Join-Path $TargetDir "mpv-gotorrent.exe"))
    $currentVersion = "none"
    if ($isInstalled) {
        $currentVersion = Get-InstalledVersion $TargetDir
        Write-Info "Current installation: $currentVersion"
    }

    # 1. Local installation if requested
    if ($Local) {
        if (Test-IsLocalRepo) {
            Install-FromLocal $TargetDir
            exit 0
        } else {
            Write-ErrMsg "-Local specified but current directory is not the mpv-gotorrent repository."
            exit 1
        }
    }

    # 2. Query GitHub for latest release
    Write-Info "Fetching release information from GitHub ($Repo)..."
    $ReleaseApiUrl = if ($Version -eq "latest") {
        "https://api.github.com/repos/$Repo/releases/latest"
    } else {
        "https://api.github.com/repos/$Repo/releases/tags/$Version"
    }

    $ReleaseJson = $null
    try {
        $headers = @{
            "User-Agent" = "mpv-gotorrent-installer-powershell"
            "Accept"     = "application/vnd.github.v3+json"
        }
        $ReleaseJson = Invoke-RestMethod -Uri $ReleaseApiUrl -Headers $headers -UseBasicParsing -ErrorAction Stop
    } catch {
        # Check if local repo fallback applies
        if (Test-IsLocalRepo) {
            Write-WarnMsg "No published GitHub release found for $Repo. Using local repository fallback!"
            Install-FromLocal $TargetDir
            exit 0
        }
        Write-ErrMsg "No release found for $Repo (version: $Version)."
        Write-Info "Once a release is tagged on GitHub, prebuilt binaries will download automatically."
        Write-Info "To install from source locally: clone repo and run '.\install.ps1 -Local'"
        exit 1
    }

    $LatestTag = $ReleaseJson.tag_name
    if (-not $LatestTag) {
        Write-ErrMsg "Failed to parse release tag from GitHub."
        exit 1
    }

    Write-Info "Latest version available: $LatestTag"

    # Check if up-to-date
    if ($isInstalled -and (-not $Force)) {
        $normCurrent = $currentVersion.TrimStart('v')
        $normLatest = $LatestTag.TrimStart('v')
        if ($normCurrent -eq $normLatest) {
            Write-Success "mpv-gotorrent is already up to date ($currentVersion) in $TargetDir"
            Write-Host "To force reinstallation or update, run with -Force or set `$env:FORCE=1" -ForegroundColor DarkGray
            exit 0
        } else {
            Write-Info "Update available: $currentVersion -> $LatestTag"
            Write-Info "Updating mpv-gotorrent..."
        }
    } else {
        Write-Info "Installing mpv-gotorrent $LatestTag..."
    }

    # Create temporary staging directory
    $tempFolder = Join-Path ([System.IO.Path]::GetTempPath()) ("mpv-gotorrent-" + [System.IO.Path]::GetRandomFileName())
    New-Item -ItemType Directory -Path $tempFolder -Force | Out-Null

    try {
        # Find Windows binary asset
        $assetUrl = $null
        $assetName = $null

        if ($ReleaseJson.assets) {
            foreach ($asset in $ReleaseJson.assets) {
                $name = $asset.name.ToLower()
                $isWin = $name.Contains("windows") -or $name.Contains("win")
                $isArch = $name.Contains($Arch) -or ($Arch -eq "amd64" -and $name.Contains("x86_64"))
                
                if ($isWin -and $isArch) {
                    $assetUrl = $asset.browser_download_url
                    $assetName = $asset.name
                    break
                }
            }
        }

        # Direct asset naming fallback
        if (-not $assetUrl) {
            $candidateName = "mpv-gotorrent-windows-$Arch.exe"
            $assetUrl = "https://github.com/$Repo/releases/download/$LatestTag/$candidateName"
            $assetName = $candidateName
            Write-Info "Trying direct asset name: $assetName"
        }

        # Download binary / archive
        Write-Info "Downloading companion binary: $assetName..."
        $downloadFile = Join-Path $tempFolder $assetName
        Invoke-WebRequest -Uri $assetUrl -OutFile $downloadFile -UseBasicParsing -Headers @{ "User-Agent" = "mpv-gotorrent-installer" }

        $extractedBin = Join-Path $tempFolder "mpv-gotorrent.exe"

        # Handle zip archive if applicable
        if ($assetName.EndsWith(".zip")) {
            Expand-Archive -Path $downloadFile -DestinationPath $tempFolder -Force
            if (-not (Test-Path $extractedBin)) {
                $found = Get-ChildItem -Path $tempFolder -Filter "mpv-gotorrent*.exe" -Recurse | Select-Object -First 1
                if ($found) { Move-Item -Path $found.FullName -Destination $extractedBin -Force }
            }
        } else {
            Move-Item -Path $downloadFile -Destination $extractedBin -Force
        }

        if (-not (Test-Path $extractedBin)) {
            Write-ErrMsg "Failed to find extracted mpv-gotorrent.exe"
            exit 1
        }

        # Retrieve main.lua script
        $luaFile = Join-Path $tempFolder "main.lua"
        if (-not (Test-Path $luaFile)) {
            $foundLua = Get-ChildItem -Path $tempFolder -Filter "main.lua" -Recurse | Select-Object -First 1
            if ($foundLua) {
                Copy-Item -Path $foundLua.FullName -Destination $luaFile -Force
            } else {
                Write-Info "Fetching mpv Lua script (main.lua)..."
                $rawUrls = @(
                    "https://raw.githubusercontent.com/$Repo/$LatestTag/scripts/main.lua",
                    "https://raw.githubusercontent.com/$Repo/main/scripts/main.lua",
                    "https://raw.githubusercontent.com/$Repo/main/scripts/mpv-gotorrent/main.lua"
                )
                $fetched = $false
                foreach ($url in $rawUrls) {
                    try {
                        Invoke-WebRequest -Uri $url -OutFile $luaFile -UseBasicParsing -Headers @{ "User-Agent" = "mpv-gotorrent-installer" } -ErrorAction Stop
                        if ((Get-Item $luaFile).Length -gt 0) {
                            $fetched = $true
                            break
                        }
                    } catch { }
                }

                if (-not $fetched) {
                    Write-ErrMsg "Failed to retrieve valid main.lua script from repository."
                    exit 1
                }
            }
        }

        # Deploy into target mpv directory
        if (-not (Test-Path $TargetDir)) {
            New-Item -ItemType Directory -Force -Path $TargetDir | Out-Null
        }

        Copy-Item -Path $luaFile -Destination (Join-Path $TargetDir "main.lua") -Force
        Copy-Item -Path $extractedBin -Destination (Join-Path $TargetDir "mpv-gotorrent.exe") -Force
        Set-Content -Path (Join-Path $TargetDir ".version") -Value $LatestTag -Encoding UTF8

        # Verify executable
        $verifiedPath = Join-Path $TargetDir "mpv-gotorrent.exe"
        try {
            $testOut = (& $verifiedPath -v 2>$null)
            if ($testOut) {
                Write-Success "Installation verified: $($testOut.Trim())"
            }
        } catch { }

        Write-Host ""
        if ($isInstalled) {
            Write-Success "mpv-gotorrent successfully updated to $LatestTag!"
        } else {
            Write-Success "mpv-gotorrent $LatestTag successfully installed!"
        }
        Show-Usage $TargetDir

    } finally {
        # Cleanup temporary files
        if (Test-Path $tempFolder) {
            Remove-Item -Path $tempFolder -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
} catch {
    Write-ErrMsg $_.Exception.Message
    exit 1
}

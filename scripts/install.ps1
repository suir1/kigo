param(
    [string]$Repo = $(if ($env:KIGO_REPO) { $env:KIGO_REPO } else { "suir1/kigo" }),
    [string]$Version = $env:KIGO_VERSION,
    [string]$InstallDir = $(if ($env:KIGO_INSTALL_DIR) { $env:KIGO_INSTALL_DIR } else { Join-Path $HOME "bin" }),
    [string]$ReleaseBaseURL = $env:KIGO_RELEASE_BASE_URL,
    [switch]$AddToPath,
    [switch]$DryRun
)

$ErrorActionPreference = "Stop"

function Test-Truthy([string]$Value) {
    return -not [string]::IsNullOrWhiteSpace($Value) -and $Value.ToLowerInvariant() -in @("1", "true", "yes", "on")
}

function Get-KigoArchitecture {
    if ($env:KIGO_TEST_ARCH) {
        return $env:KIGO_TEST_ARCH
    }
    try {
        return [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()
    }
    catch {
        if ($env:PROCESSOR_ARCHITEW6432) { return $env:PROCESSOR_ARCHITEW6432 }
        return $env:PROCESSOR_ARCHITECTURE
    }
}

function Test-PathContains([string]$PathValue, [string]$Directory) {
    if ([string]::IsNullOrWhiteSpace($PathValue)) { return $false }
    $target = $Directory.TrimEnd([char[]]@(92, 47))
    foreach ($entry in $PathValue.Split([System.IO.Path]::PathSeparator)) {
        if ($entry.TrimEnd([char[]]@(92, 47)) -ieq $target) { return $true }
    }
    return $false
}

function Save-KigoDownload([string]$Uri, [string]$OutFile) {
    $parsed = [Uri]$Uri
    if ($parsed.IsFile) {
        Copy-Item -LiteralPath $parsed.LocalPath -Destination $OutFile -Force
        return
    }
    Invoke-WebRequest -Uri $Uri -OutFile $OutFile -UseBasicParsing
}

try {
    $protocols = [Net.ServicePointManager]::SecurityProtocol
    $protocols = $protocols -bor [Net.SecurityProtocolType]::Tls12
    [Net.ServicePointManager]::SecurityProtocol = $protocols
}
catch {
}

if (-not $DryRun -and (Test-Truthy $env:KIGO_INSTALL_DRY_RUN)) {
    $DryRun = $true
}
if (-not $AddToPath -and (Test-Truthy $env:KIGO_ADD_TO_PATH)) {
    $AddToPath = $true
}

if (-not $Version) {
    if ($Repo -notmatch '^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$') {
        throw "Invalid KIGO_REPO: $Repo"
    }
    try {
        $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -Headers @{ Accept = "application/vnd.github+json"; "User-Agent" = "kigo-install" }
        $Version = $release.tag_name
    }
    catch {
    }
    if (-not $Version) {
        try {
            $releases = @(Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases?per_page=100" -Headers @{ Accept = "application/vnd.github+json"; "User-Agent" = "kigo-install" })
            $release = $releases | Where-Object { $_.tag_name } | Select-Object -First 1
            $Version = $release.tag_name
        }
        catch {
            throw "Could not determine the latest release; set KIGO_VERSION explicitly. $($_.Exception.Message)"
        }
    }
    if (-not $Version) {
        throw "Could not determine the latest release; set KIGO_VERSION explicitly"
    }
}
if ($Version -notmatch '^[A-Za-z0-9][A-Za-z0-9._+-]*$') {
    throw "Invalid KIGO_VERSION: $Version"
}

$archName = (Get-KigoArchitecture).ToLowerInvariant()
switch ($archName) {
    { $_ -in @("x64", "x86_64", "amd64") } { $arch = "amd64"; break }
    default { throw "Unsupported Windows architecture: $archName" }
}

$root = "kigo-$Version-windows-$arch"
$archive = "$root.zip"
if ([string]::IsNullOrWhiteSpace($ReleaseBaseURL)) {
    if ($Repo -notmatch '^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$') {
        throw "Invalid KIGO_REPO: $Repo"
    }
    $ReleaseBaseURL = "https://github.com/$Repo/releases/download/$Version"
}
$ReleaseBaseURL = $ReleaseBaseURL.TrimEnd('/')
$releaseUri = [Uri]$ReleaseBaseURL
$isLoopbackHttp = $releaseUri.Scheme -eq "http" -and $releaseUri.IsLoopback
if ($releaseUri.Scheme -notin @("https", "file") -and -not $isLoopbackHttp) {
    throw "KIGO_RELEASE_BASE_URL must use HTTPS, file, or loopback HTTP"
}
$archiveURL = "$ReleaseBaseURL/$archive"
$checksumsURL = "$ReleaseBaseURL/SHA256SUMS"

if ($DryRun) {
    Write-Output "version=$Version"
    Write-Output "platform=windows-$arch"
    Write-Output "archive=$archive"
    Write-Output "archive_url=$archiveURL"
    Write-Output "checksums_url=$checksumsURL"
    Write-Output "install_dir=$InstallDir"
    return
}

$work = Join-Path ([System.IO.Path]::GetTempPath()) ("kigo-install-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force -Path $work | Out-Null
try {
    $archivePath = Join-Path $work $archive
    $checksumsPath = Join-Path $work "SHA256SUMS"
    Write-Host "Downloading $archiveURL"
    Save-KigoDownload $archiveURL $archivePath
    Save-KigoDownload $checksumsURL $checksumsPath

    $checksumMatches = @()
    foreach ($line in Get-Content $checksumsPath) {
        if ($line -match '^([0-9A-Fa-f]{64})\s+\*?([^\s]+)$' -and $Matches[2] -ceq $archive) {
            $checksumMatches += $Matches[1].ToLowerInvariant()
        }
    }
    if ($checksumMatches.Count -ne 1) {
        throw "SHA256SUMS does not contain exactly one entry for $archive"
    }
    $actual = (Get-FileHash -Algorithm SHA256 -Path $archivePath).Hash.ToLowerInvariant()
    if ($actual -cne $checksumMatches[0]) {
        throw "SHA-256 mismatch for $archive"
    }

    Expand-Archive -Path $archivePath -DestinationPath $work -Force
    $sourcePath = Join-Path $work "$root/kigo.exe"
    if (-not (Test-Path -PathType Leaf $sourcePath)) {
        throw "Release archive does not contain $root/kigo.exe"
    }
    & $sourcePath version --json | Out-Null
    if ($LASTEXITCODE -ne 0) {
        throw "Downloaded kigo binary failed its version check"
    }

    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    $target = Join-Path $InstallDir "kigo.exe"
    $staged = Join-Path $InstallDir (".kigo.install." + [Guid]::NewGuid().ToString("N") + ".exe")
    Copy-Item -Path $sourcePath -Destination $staged -Force
    Move-Item -Path $staged -Destination $target -Force
    Write-Host "Installed kigo $Version to $target"

    if ($AddToPath) {
        $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
        if (-not (Test-PathContains $userPath $InstallDir)) {
            $newPath = if ([string]::IsNullOrWhiteSpace($userPath)) { $InstallDir } else { "$userPath$([IO.Path]::PathSeparator)$InstallDir" }
            [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
            Write-Host "Added $InstallDir to the user PATH; open a new terminal to apply it"
        }
    }
    elseif (-not (Test-PathContains $env:Path $InstallDir)) {
        Write-Host "Add $InstallDir to PATH, or rerun with KIGO_ADD_TO_PATH=1"
    }
}
finally {
    Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}

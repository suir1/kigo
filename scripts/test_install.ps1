$ErrorActionPreference = "Stop"

$rootDir = Split-Path -Parent $PSScriptRoot
$binary = Join-Path $rootDir "kigo.exe"
if (-not (Test-Path -PathType Leaf $binary)) {
    throw "Build kigo.exe before running the Windows installer smoke test"
}

$work = Join-Path ([IO.Path]::GetTempPath()) ("kigo-installer-test-" + [Guid]::NewGuid().ToString("N"))
$release = Join-Path $work "release"
$installDir = Join-Path $work "bin"
$version = "v9.8.7-test"
$archiveRoot = "kigo-$version-windows-amd64"
$archive = "$archiveRoot.zip"

New-Item -ItemType Directory -Force -Path (Join-Path $release $archiveRoot) | Out-Null
try {
    function Invoke-RestMethod {
        param([string]$Uri, [hashtable]$Headers)
        if ($Uri -match '/releases/latest$') { throw "no stable release" }
        if ($Uri -match '/releases\?per_page=100$') { return @([pscustomobject]@{ tag_name = "v1.2.3-alpha.2" }) }
        throw "unexpected release API request: $Uri"
    }
    $resolved = & "$PSScriptRoot/install.ps1" -Repo "example/kigo" -InstallDir $installDir -DryRun
    if ($resolved -notcontains "version=v1.2.3-alpha.2") { throw "prerelease fallback failed: $resolved" }
    Remove-Item function:Invoke-RestMethod

    Copy-Item $binary (Join-Path $release "$archiveRoot/kigo.exe")
    Compress-Archive -Path (Join-Path $release $archiveRoot) -DestinationPath (Join-Path $release $archive)
    Remove-Item -Recurse -Force (Join-Path $release $archiveRoot)
    $digest = (Get-FileHash -Algorithm SHA256 (Join-Path $release $archive)).Hash.ToLowerInvariant()
    Set-Content -Path (Join-Path $release "SHA256SUMS") -Value "$digest  $archive" -Encoding ascii
    $releaseURL = ([Uri](Resolve-Path $release).Path).AbsoluteUri

    $plan = & "$PSScriptRoot/install.ps1" -Version $version -ReleaseBaseURL $releaseURL -InstallDir $installDir -DryRun
    if ($plan -notcontains "platform=windows-amd64") { throw "unexpected installer platform: $plan" }
    if ($plan -notcontains "archive=$archive") { throw "unexpected installer archive: $plan" }

    & "$PSScriptRoot/install.ps1" -Version $version -ReleaseBaseURL $releaseURL -InstallDir $installDir
    $installed = Join-Path $installDir "kigo.exe"
    if (-not (Test-Path -PathType Leaf $installed)) { throw "installer did not write kigo.exe" }
    if ((Get-FileHash -Algorithm SHA256 $installed).Hash -cne (Get-FileHash -Algorithm SHA256 $binary).Hash) {
        throw "installed binary did not match the release binary"
    }
    & $installed version --json | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "installed binary version check failed" }

    Add-Content -Path (Join-Path $release $archive) -Value "tampered"
    try {
        & "$PSScriptRoot/install.ps1" -Version $version -ReleaseBaseURL $releaseURL -InstallDir (Join-Path $work "tampered-bin")
        throw "installer accepted an archive with the wrong SHA-256"
    }
    catch {
        if ($_.Exception.Message -notmatch "SHA-256 mismatch") { throw }
    }

    Write-Output "Windows installer smoke passed"
}
finally {
    Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}

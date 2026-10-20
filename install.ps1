# Installs the SaladCloud CLI (saladcloud) on Windows.
#
#   irm https://raw.githubusercontent.com/SaladTechnologies/salad-cloud-cli/main/install.ps1 | iex
#
# Downloads the release archive, verifies it against the release's
# checksums.txt, installs saladcloud.exe, and adds its folder to your user
# PATH. Works in Windows PowerShell 5.1 and PowerShell 7. Environment, all
# optional:
#
#   SALAD_CLI_VERSION         release tag, e.g. v0.1.0-beta.1 (default: the latest
#                              release, or the newest pre-release while there is none)
#   SALAD_CLI_INSTALL_DIR     where to put the binary (default: %LOCALAPPDATA%\Programs\saladcloud)
#   SALAD_CLI_REPO            GitHub repository (default: SaladTechnologies/salad-cloud-cli)
#   SALAD_CLI_DOWNLOAD_URL    base URL holding the release assets, instead of the
#                              repository's release downloads (for testing)
#   SALAD_CLI_NO_MODIFY_PATH  set to 1 to leave your user PATH unchanged
#   GITHUB_TOKEN              sent to the GitHub API when finding the latest
#                              release, to avoid its rate limit (CI)

# Everything runs inside a function so nothing leaks into the caller's
# session, and errors are thrown rather than calling exit, which would close
# the window when the script is piped into iex.
function Install-Saladcloud {
    [Diagnostics.CodeAnalysis.SuppressMessageAttribute('PSAvoidUsingWriteHost', '',
        Justification = 'Run under irm | iex, it talks to the person at the console.')]
    param()

    $ErrorActionPreference = 'Stop'
    $ProgressPreference = 'SilentlyContinue' # the progress bar slows downloads in Windows PowerShell 5.1
    $protocols = [Net.ServicePointManager]::SecurityProtocol
    [Net.ServicePointManager]::SecurityProtocol = $protocols -bor [Net.SecurityProtocolType]::Tls12

    $repo = if ($env:SALAD_CLI_REPO) {
        $env:SALAD_CLI_REPO
    }
    else {
        'SaladTechnologies/salad-cloud-cli'
    }
    $installDir = if ($env:SALAD_CLI_INSTALL_DIR) {
        $env:SALAD_CLI_INSTALL_DIR
    }
    else {
        Join-Path $env:LOCALAPPDATA 'Programs\saladcloud'
    }
    $version = $env:SALAD_CLI_VERSION

    $arch = $env:PROCESSOR_ARCHITECTURE
    if ($env:PROCESSOR_ARCHITEW6432) {
        $arch = $env:PROCESSOR_ARCHITEW6432
    }
    if ($arch -eq 'ARM64') {
        Write-Host 'saladcloud install: no native ARM64 build; installing the amd64 build, which runs under emulation'
    }
    elseif ($arch -ne 'AMD64') {
        throw "saladcloud install: unsupported CPU architecture '$arch'"
    }

    if (-not $version) {
        # The latest full release first, from where github.com/<repo>/releases/latest
        # redirects (no API call, no rate limit); during the beta every release
        # is a pre-release, which that skips, so fall back to the newest
        # release of any kind through the API.
        try {
            $req = [Net.HttpWebRequest]::Create("https://github.com/$repo/releases/latest")
            $req.AllowAutoRedirect = $false
            $resp = $req.GetResponse()
            $location = $resp.Headers['Location']
            $resp.Close()
            if ($location -match '/releases/tag/([^/]+)$') {
                $version = $Matches[1]
            }
        }
        catch {
            $version = $null
        }
        if (-not $version) {
            $headers = @{}
            if ($env:GITHUB_TOKEN) {
                $headers['Authorization'] = "Bearer $env:GITHUB_TOKEN"
            }
            try {
                $releasesUri = "https://api.github.com/repos/$repo/releases?per_page=1"
                $version = @(Invoke-RestMethod -UseBasicParsing -Headers $headers $releasesUri)[0].tag_name
            }
            catch {
                $version = $null
            }
        }
        if (-not $version) {
            throw "saladcloud install: could not find a release of $repo; set SALAD_CLI_VERSION to choose one"
        }
    }

    $name = "saladcloud_${version}_windows_amd64"
    $archive = "$name.zip"
    $base = if ($env:SALAD_CLI_DOWNLOAD_URL) {
        $env:SALAD_CLI_DOWNLOAD_URL
    }
    else {
        "https://github.com/$repo/releases/download/$version"
    }
    Write-Host "saladcloud install: installing saladcloud $version for windows/amd64"

    $tmp = Join-Path ([IO.Path]::GetTempPath()) ('saladcloud-' + [guid]::NewGuid())
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        $zip = Join-Path $tmp $archive
        $sums = Join-Path $tmp 'checksums.txt'
        foreach ($download in @(@("$base/$archive", $zip), @("$base/checksums.txt", $sums))) {
            try {
                Invoke-WebRequest -UseBasicParsing -Uri $download[0] -OutFile $download[1]
            }
            catch {
                throw "saladcloud install: download failed: $($download[0]) ($($_.Exception.Message))"
            }
        }

        $expected = $null
        foreach ($line in Get-Content $sums) {
            $fields = $line -split '\s+'
            if ($fields.Count -ge 2 -and $fields[1] -eq $archive) {
                $expected = $fields[0].ToLower()
            }
        }
        if (-not $expected) {
            throw "saladcloud install: $archive is not listed in checksums.txt"
        }
        $actual = (Get-FileHash -Algorithm SHA256 -Path $zip).Hash.ToLower()
        if ($actual -ne $expected) {
            throw "saladcloud install: checksum mismatch for $archive (expected $expected, got $actual)"
        }

        Expand-Archive -Path $zip -DestinationPath $tmp -Force
        New-Item -ItemType Directory -Path $installDir -Force | Out-Null
        $exe = Join-Path $installDir 'saladcloud.exe'
        # A running saladcloud.exe cannot be overwritten, but it can be
        # renamed: move the old one aside, copy the new one in, and remove the
        # old one if nothing holds it (a later install cleans up otherwise).
        $old = "$exe.old"
        Remove-Item -Force $old -ErrorAction SilentlyContinue
        if (Test-Path $exe) {
            Move-Item -Force $exe $old
        }
        try {
            Copy-Item -Path (Join-Path (Join-Path $tmp $name) 'saladcloud.exe') -Destination $exe -Force
        }
        catch {
            # Put the old binary back rather than leave none (an antivirus
            # lock or a full disk can fail the copy).
            if ((Test-Path $old) -and -not (Test-Path $exe)) {
                Move-Item -Force $old $exe
            }
            throw
        }
        Remove-Item -Force $old -ErrorAction SilentlyContinue
        Write-Host "saladcloud install: installed $exe"
    }
    finally {
        Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    }

    # The user PATH is read and written as stored in the registry: unexpanded
    # (%USERPROFILE%\... stays a reference) and as REG_EXPAND_SZ, which
    # [Environment]::SetEnvironmentVariable would rewrite as a plain,
    # expanded string.
    $envKey = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
    $noExpand = [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames
    $userPath = [string]$envKey.GetValue('Path', '', $noExpand)
    $expanded = [Environment]::ExpandEnvironmentVariables($userPath)
    $onPath = @($expanded -split ';' | Where-Object { $_.TrimEnd('\') -ieq $installDir.TrimEnd('\') }).Count -gt 0
    if (-not $onPath) {
        if ($env:SALAD_CLI_NO_MODIFY_PATH -eq '1') {
            Write-Host "saladcloud install: $installDir is not on your PATH; add it to run saladcloud by name"
        }
        else {
            $newPath = if ($userPath) {
                "$($userPath.TrimEnd(';'));$installDir"
            }
            else {
                $installDir
            }
            $envKey.SetValue('Path', $newPath, [Microsoft.Win32.RegistryValueKind]::ExpandString)
            # Setting and clearing a variable broadcasts WM_SETTINGCHANGE, so
            # Explorer and new terminals see the new PATH without a sign-out.
            [Environment]::SetEnvironmentVariable('SALAD_CLI_PATH_REFRESH', '1', 'User')
            [Environment]::SetEnvironmentVariable('SALAD_CLI_PATH_REFRESH', $null, 'User')
            $env:Path = "$env:Path;$installDir"
            Write-Host "saladcloud install: added $installDir to your user PATH (new terminals pick it up)"
        }
    }
    $envKey.Close()

    Write-Host ('saladcloud install: for shell completion, see saladcloud completion powershell --help ' +
        '(scripts are also in the release archive)')
    if (-not $env:SALAD_CLI_DOWNLOAD_URL) {
        Write-Host ("saladcloud install: to verify this build's provenance: " +
            "gh attestation verify $archive --repo $repo (on the archive from $base)")
    }
    & $exe version
    Write-Host ''
    Write-Host 'Next, log in:'
    Write-Host '  saladcloud login'
}

Install-Saladcloud

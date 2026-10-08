# Uninstalls the SaladCloud CLI (saladcloud) that install.ps1 put on Windows.
#
#   irm https://raw.githubusercontent.com/SaladTechnologies/salad-cloud-cli/main/uninstall.ps1 | iex
#
# Removes saladcloud.exe from the folder install.ps1 used, and that folder from
# your user PATH. The config file (your saved API keys, organization and
# project) stays, so a reinstall picks up where you left off, unless
# SALAD_CLI_PURGE=1. Works in Windows PowerShell 5.1 and PowerShell 7.
# Environment, all optional:
#
#   SALAD_CLI_INSTALL_DIR     the folder install.ps1 used (default: %LOCALAPPDATA%\Programs\saladcloud)
#   SALAD_CLI_PURGE           set to 1 to also delete the config file and the CLI's
#                              known_hosts (and their folder, once empty)
#   SALAD_CLI_CONFIG_DIR      the config folder, if you set it for the CLI
#   SALAD_CLI_NO_MODIFY_PATH  set to 1 to leave your user PATH unchanged
#
# A saladcloud installed some other way (Scoop, winget, go install, a manual
# copy) is not touched; the script says where it is.

# As in install.ps1: everything runs inside a function, and errors are thrown
# rather than calling exit, which would close the window when piped into iex.
function Uninstall-Saladcloud {
    $ErrorActionPreference = 'Stop'

    $installDir = if ($env:SALAD_CLI_INSTALL_DIR) { $env:SALAD_CLI_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\saladcloud' }
    $exe = Join-Path $installDir 'saladcloud.exe'

    # The binary knows where its config is (the OS's folder, or
    # SALAD_CLI_CONFIG_DIR); ask it before removing it. Without it, use the
    # folder it would have used.
    $config = $null
    if (Test-Path $exe) {
        try { $config = (& $exe config path 2>$null | Select-Object -First 1) } catch { $config = $null }
    }
    if (-not $config) {
        $dir = if ($env:SALAD_CLI_CONFIG_DIR) { $env:SALAD_CLI_CONFIG_DIR } else { Join-Path $env:APPDATA 'saladcloud' }
        $config = Join-Path $dir 'config.json'
    }

    if (Test-Path $exe) {
        foreach ($f in @($exe, "$exe.old")) {
            if (-not (Test-Path $f)) { continue }
            try {
                Remove-Item -Force $f
            } catch {
                throw "saladcloud uninstall: cannot remove ${f}: $($_.Exception.Message) (close any running saladcloud and run this again)"
            }
        }
        Write-Host "saladcloud uninstall: removed $exe"
    } else {
        Remove-Item -Force "$exe.old" -ErrorAction SilentlyContinue
        Write-Host "saladcloud uninstall: no saladcloud.exe in $installDir; nothing to remove there"
    }
    # The folder too, but only when nothing else is in it.
    if ((Test-Path $installDir) -and -not (Get-ChildItem -Force $installDir)) {
        Remove-Item -Force $installDir
    }

    if ($env:SALAD_CLI_NO_MODIFY_PATH -ne '1') {
        # Read and write the user PATH as stored, as install.ps1 does: unexpanded
        # and as REG_EXPAND_SZ. An entry goes when it expands to the install folder.
        $target = $installDir.TrimEnd('\')
        $envKey = [Microsoft.Win32.Registry]::CurrentUser.OpenSubKey('Environment', $true)
        try {
            $userPath = [string]$envKey.GetValue('Path', '', [Microsoft.Win32.RegistryValueOptions]::DoNotExpandEnvironmentNames)
            $entries = @($userPath -split ';' | Where-Object { $_ -ne '' })
            $kept = @($entries | Where-Object { [Environment]::ExpandEnvironmentVariables($_).TrimEnd('\') -ine $target })
            if ($kept.Count -ne $entries.Count) {
                if ($kept.Count -eq 0) {
                    $envKey.DeleteValue('Path')
                } else {
                    $envKey.SetValue('Path', ($kept -join ';'), [Microsoft.Win32.RegistryValueKind]::ExpandString)
                }
                # Setting and clearing a variable broadcasts WM_SETTINGCHANGE, so
                # Explorer and new terminals see the change without a sign-out.
                [Environment]::SetEnvironmentVariable('SALAD_CLI_PATH_REFRESH', '1', 'User')
                [Environment]::SetEnvironmentVariable('SALAD_CLI_PATH_REFRESH', $null, 'User')
                $env:Path = (@($env:Path -split ';' | Where-Object { $_.TrimEnd('\') -ine $target }) -join ';')
                Write-Host "saladcloud uninstall: removed $installDir from your user PATH"
            }
        } finally {
            $envKey.Close()
        }
    }

    $configDir = Split-Path -Parent $config
    $knownHosts = Join-Path $configDir 'known_hosts'
    if ($env:SALAD_CLI_PURGE -eq '1') {
        # Only the files the CLI writes, never the folder's other contents:
        # SALAD_CLI_CONFIG_DIR can point at a folder that holds other things.
        if ((Test-Path $config) -or (Test-Path $knownHosts)) {
            Remove-Item -Force $config, $knownHosts -ErrorAction SilentlyContinue
            if ((Test-Path $configDir) -and -not (Get-ChildItem -Force $configDir)) { Remove-Item -Force $configDir }
            Write-Host "saladcloud uninstall: removed the config file $config and the CLI's known_hosts"
        }
    } elseif (Test-Path $config) {
        Write-Host "saladcloud uninstall: kept the config file $config (it holds your saved API keys); delete it, or rerun with SALAD_CLI_PURGE=1"
    }
    if (Test-Path (Join-Path $HOME '.ssh\saladcloud_ed25519')) {
        Write-Host "saladcloud uninstall: kept the SSH key ~\.ssh\saladcloud_ed25519 (saladcloud ssh-key setup made it, and it may be registered in the portal)"
    }

    # Another copy still on PATH came from somewhere else.
    $other = Get-Command saladcloud -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($other) {
        $path = $other.Source
        $how = if ($path -match '\\scoop\\') { 'scoop uninstall saladcloud' }
        elseif ($path -match '\\WinGet\\') { 'winget uninstall SaladTechnologies.SaladCloudCLI' }
        elseif ($path -match '\\go\\bin\\') { "delete $path (go install put it there)" }
        else { 'remove it the way it was installed' }
        Write-Host "saladcloud uninstall: another saladcloud is still on your PATH: $path; to remove it, $how"
    }
}

Uninstall-Saladcloud

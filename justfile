export GOFLAGS := "-mod=readonly"
export GOTOOLCHAIN := "local"

# Bash is required. If the target already sets a shell, keep whichever is stricter.
set shell := ["bash", "-euo", "pipefail", "-c"]

# List of allowed licenses for dependencies.
go-allowed-licenses := "Apache-2.0,BSD-2-Clause,BSD-3-Clause,ISC,MIT,MPL-2.0,Unicode-3.0,Zlib"

# Load PSScriptAnalyzer (and installs if missing). List of every PowerShell file discovered by extension. Skips files git ignores.
powershell-prelude := '''
$ErrorActionPreference = 'Stop'
$PSNativeCommandUseErrorActionPreference = $true
Set-StrictMode -Version Latest
$analyzerVersion = '1.25.0'
if (-not (Get-Module -ListAvailable -Name PSScriptAnalyzer | Where-Object Version -EQ $analyzerVersion)) {
    Install-PSResource -Name PSScriptAnalyzer -Version $analyzerVersion -Scope CurrentUser -TrustRepository -Quiet
}
Import-Module -Name PSScriptAnalyzer -RequiredVersion $analyzerVersion
$settings = Join-Path $PWD 'PSScriptAnalyzerSettings.psd1'
$files = @(
    (git ls-files -z --cached --others --exclude-standard -- '*.ps1' '*.psd1' '*.psm1') -split "`0" |
        Where-Object { $_ -and $_ -notmatch '(^|/)testdata/' -and (Test-Path -LiteralPath $_ -PathType Leaf) }
)
function Format-PowerShell([string] $Text) {
    if ([string]::IsNullOrWhiteSpace($Text)) {
        return $Text
    }
    (Invoke-Formatter -ScriptDefinition $Text -Settings $settings) -replace '(?m)[ \t]+(?=\r?$)', ''
}
'''

# List of every shell script discovered by extension or shebang. Skips files git ignores.
shell-files := 'git ls-files -z --cached --others --exclude-standard | while IFS= read -r -d "" f; do case "$f" in testdata/* | */testdata/*) ;; *) if [ -f "$f" ]; then printf "%s\0" "$f"; fi ;; esac; done | xargs -0 shfmt -f'

[private]
default:
    @just --justfile {{ justfile() }} --list

# Format Go files.
fmt-go:
    golangci-lint fmt

# Check formatting of Go files and fail on differences.
fmt-check-go:
    golangci-lint fmt --diff

# Lint Go files.
lint-go:
    golangci-lint run
    go mod tidy -diff
    go mod verify
    govulncheck ./...
    go-licenses check ./... --include_tests --ignore "$(go list -m)" --allowed_licenses={{ go-allowed-licenses }}

# Vet Go files.
vet:
    go vet ./...

# Test Go files, with the race detector where cgo is available (it needs a C compiler).
test-go:
    race="$(if [ "$(go env CGO_ENABLED)" = 1 ]; then echo -race; fi)"; go test $race -shuffle=on -count=1 -timeout=2m ./...

# Cross-compile into dist/ (windows-amd64, linux-amd64, darwin-arm64, darwin-amd64).
build:
    bash scripts/build.sh

# The live suite needs SALAD_API_KEY and SALAD_ORGANIZATION (SALAD_PROJECT optional) and fails without them, so a pass
# always means it ran. Optional: SALAD_CLI_TEST_REQUIRE_SSH=1 fails the ssh test instead of skipping it on a publickey
# denial (set it for the pre-tag run); SALAD_AI_GATEWAY_API_KEY adds the AI Gateway test; SALAD_CLI_TEST_AUDIO=<short
# wav> and SALAD_CLI_TEST_PHRASE=<words in it> add transcription; SALAD_CLI_TEST_AUDIO_FOREIGN=<short single-track
# non-English clip> adds the language_code and audio_stream_index test; SALAD_CLI_TEST_BIN=<path> tests another binary
# than dist/.

# Run the live suite against the real API (spends money; see the notes above).
test-live: build
    go test -tags live ./internal/livetest/ -timeout 30m -v

# Format PowerShell files.
[extension('.ps1')]
[script('pwsh', '-NoLogo', '-NoProfile', '-NonInteractive', '-File')]
fmt-powershell:
    {{ powershell-prelude }}
    foreach ($file in $files) {
        $path = Join-Path $PWD $file
        $bytes = [IO.File]::ReadAllBytes($path)
        $bom = $bytes.Length -ge 3 -and $bytes[0] -eq 0xEF -and $bytes[1] -eq 0xBB -and $bytes[2] -eq 0xBF
        $text = [IO.File]::ReadAllText($path)
        $formatted = Format-PowerShell $text
        if ($formatted -cne $text) {
            [IO.File]::WriteAllText($path, $formatted, [Text.UTF8Encoding]::new($bom))
        }
    }

# Check formatting of PowerShell files and fail on differences.
[extension('.ps1')]
[script('pwsh', '-NoLogo', '-NoProfile', '-NonInteractive', '-File')]
fmt-check-powershell:
    {{ powershell-prelude }}
    $temp = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
    $bad = $false
    try {
        foreach ($file in $files) {
            $text = [IO.File]::ReadAllText((Join-Path $PWD $file))
            $formatted = Format-PowerShell $text
            if ($formatted -cne $text) {
                $bad = $true
                foreach ($side in @(@('a', $text), @('b', $formatted))) {
                    $target = Join-Path $temp $side[0] $file
                    $null = New-Item -ItemType Directory -Force -Path (Split-Path $target)
                    [IO.File]::WriteAllText($target, $side[1])
                }
                $PSNativeCommandUseErrorActionPreference = $false
                git -C $temp diff --no-index --no-prefix -- "a/$file" "b/$file"
                $PSNativeCommandUseErrorActionPreference = $true
            }
        }
    } finally {
        Remove-Item -LiteralPath $temp -Recurse -Force -ErrorAction Ignore
    }
    if ($bad) {
        Write-Host 'fix: just fmt-powershell'
        exit 1
    }

# Lint PowerShell files. This also checks that PowerShell scripts with a shebang are executable.
[extension('.ps1')]
[script('pwsh', '-NoLogo', '-NoProfile', '-NonInteractive', '-File')]
lint-powershell:
    {{ powershell-prelude }}
    $bad = $false
    foreach ($file in $files) {
        $records = Invoke-ScriptAnalyzer -Path $file -Settings $settings
        foreach ($record in $records | Sort-Object Line, Column) {
            $bad = $true
            $location = if ($record.Line) { "${file}:$($record.Line):$($record.Column)" } else { $file }
            Write-Host "${location}: $($record.Severity) $($record.RuleName): $($record.Message)"
        }
        if ($file -like '*.ps1' -and (Get-Content -LiteralPath $file -TotalCount 1) -like '#!*' -and
            (git ls-files -s -- $file) -like '100644 *') {
            $bad = $true
            Write-Host "not executable: $file (fix: git update-index --chmod=+x -- $file)"
        }
    }
    if ($bad) {
        exit 1
    }

# Format shell scripts.
fmt-shell:
    files="$({{ shell-files }})"; if [ -n "$files" ]; then printf '%s\n' "$files" | xargs shfmt -w; fi

# Check formatting of shell scripts and fail on differences.
fmt-check-shell:
    files="$({{ shell-files }})"; if [ -n "$files" ]; then printf '%s\n' "$files" | xargs shfmt -d; fi

# Lint shell scripts. This also checks that shell scripts are executable.
lint-shell:
    files="$({{ shell-files }})"; if [ -n "$files" ]; then printf '%s\n' "$files" | xargs shellcheck; fi
    {{ shell-files }} \
        | while IFS= read -r f; do if head -n 1 -- "$f" | grep -q '^#!'; then git ls-files -s -- "$f"; fi; done \
        | awk -F '\t' '/^100644 / { bad = 1; \
            print "not executable: " $2 " (fix: git update-index --chmod=+x -- " $2 ")" } END { exit bad }'

# Format Markdown, YAML, and JSON files.
fmt-markup:
    npx --no --quiet -- prettier --write --log-level warn .

# Check formatting of Markdown, YAML, and JSON files and fail on differences.
fmt-check-markup:
    npx --no --quiet -- prettier --check --log-level warn .

# Check spelling in every file.
lint-spelling:
    npx --no --quiet -- cspell lint --no-progress --dot "**"

# Format every file.
fmt: fmt-go fmt-powershell fmt-shell fmt-markup

# Check formatting of every file and fail on differences.
fmt-check: fmt-check-go fmt-check-powershell fmt-check-shell fmt-check-markup

# Lint every file.
lint: lint-go lint-powershell lint-shell lint-spelling

# Run every test.
test: test-go

# Check formatting, lint, and test, as CI does.
ci: fmt-check lint test

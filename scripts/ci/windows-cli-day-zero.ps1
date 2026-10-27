param(
    [Parameter(Mandatory = $true)][string]$Binary,
    [Parameter(Mandatory = $true)][string]$Fixture,
    [string]$ExpectedVersionLine = ''
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$exe = (Resolve-Path -LiteralPath $Binary).ProviderPath
$fixturePath = (Resolve-Path -LiteralPath $Fixture).ProviderPath
if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) { throw 'Binary must be a regular file.' }
if (-not (Test-Path -LiteralPath $fixturePath -PathType Leaf)) { throw 'Fixture must be a regular file.' }

$nonAscii = [char]0x96EA
$root = Join-Path ([IO.Path]::GetTempPath()) ("kmx day zero $nonAscii " + [Guid]::NewGuid().ToString('N'))
if (Test-Path -LiteralPath $root) { throw 'Day-zero qualification requires a fresh root.' }
if ($root -notmatch ' ' -or $root -notmatch '[^\x00-\x7f]') { throw 'Qualification root must exercise spaces and non-ASCII paths.' }

$environmentNames = @(
    'KMX_HOME', 'KMX_TOOLCHAIN', 'MODEL', 'PATH',
    'COPILOT_GITHUB_TOKEN', 'GH_TOKEN', 'GITHUB_TOKEN',
    'OPENAI_API_KEY', 'AZURE_OPENAI_API_KEY', 'ANTHROPIC_API_KEY',
    'GOOGLE_API_KEY', 'GEMINI_API_KEY', 'MODEL_API_KEY', 'ORKA_API_KEY',
    'AZURE_OPENAI_ENDPOINT', 'OLLAMA_HOST',
    'HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY', 'NO_PROXY',
    'KMX_WINDOWS_AMBIENT_CREDENTIAL'
)
$savedEnvironment = @{}
foreach ($name in $environmentNames) {
    $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
$savedDefaultProxy = [Net.WebRequest]::DefaultWebProxy

function Invoke-Kmx {
    param(
        [Parameter(Mandatory = $true)][string[]]$CommandArgs,
        [int[]]$ExpectedExitCodes = @(0)
    )

    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $exe
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    foreach ($argument in $CommandArgs) {
        [void]$start.ArgumentList.Add($argument)
    }
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $start
    if (-not $process.Start()) { throw "Could not start kmx $($CommandArgs[0])." }
    $stdoutTask = $process.StandardOutput.ReadToEndAsync()
    $stderrTask = $process.StandardError.ReadToEndAsync()
    if (-not $process.WaitForExit(180000)) {
        $process.Kill($true)
        $process.WaitForExit()
        throw "kmx $($CommandArgs[0]) exceeded the three-minute qualification bound."
    }
    $stdout = $stdoutTask.Result
    $stderr = $stderrTask.Result
    $exitCode = $process.ExitCode
    $process.Dispose()
    if ($stdout.Length -gt 0) { [Console]::Out.Write($stdout) }
    if ($stderr.Length -gt 0) { [Console]::Error.Write($stderr) }
    if ($ExpectedExitCodes -notcontains $exitCode) {
        throw "kmx $($CommandArgs[0]) exited $exitCode; expected $($ExpectedExitCodes -join ', ')."
    }
    return [PSCustomObject]@{ ExitCode = $exitCode; Stdout = $stdout; Stderr = $stderr }
}

function Assert-Contract([bool]$Condition, [string]$Message) {
    if (-not $Condition) { throw $Message }
}

function Get-StateSnapshot([string]$StateRoot) {
    if (-not (Test-Path -LiteralPath $StateRoot -PathType Container)) {
        throw 'Expected retained state was not created.'
    }
    $snapshot = @{}
    foreach ($item in @(Get-ChildItem -LiteralPath $StateRoot -Recurse -Force -File)) {
        $relative = [IO.Path]::GetRelativePath($StateRoot, $item.FullName)
        $digest = (Get-FileHash -LiteralPath $item.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
        $snapshot[$relative] = "$digest/$($item.Length)/$($item.LastWriteTimeUtc.Ticks)"
    }
    if ($snapshot.Count -eq 0) { throw 'Retained state contains no files.' }
    return $snapshot
}

function Assert-SnapshotEqual($Before, $After, [string]$Label) {
    Assert-Contract ($Before.Count -eq $After.Count) "$Label changed the retained file inventory."
    foreach ($name in $Before.Keys) {
        Assert-Contract ($After.ContainsKey($name) -and $After[$name] -eq $Before[$name]) "$Label changed retained file $name."
    }
}

function Assert-NoCanary([string]$StateRoot, [string[]]$Forbidden) {
    foreach ($item in @(Get-ChildItem -LiteralPath $StateRoot -Recurse -Force -File)) {
        $bytes = [IO.File]::ReadAllBytes($item.FullName)
        foreach ($value in $Forbidden) {
            foreach ($encoding in @([Text.Encoding]::UTF8, [Text.Encoding]::Unicode, [Text.Encoding]::BigEndianUnicode)) {
                $needle = $encoding.GetBytes($value)
                for ($offset = 0; $offset -le $bytes.Length - $needle.Length; $offset++) {
                    $match = $true
                    for ($index = 0; $index -lt $needle.Length; $index++) {
                        if ($bytes[$offset + $index] -ne $needle[$index]) {
                            $match = $false
                            break
                        }
                    }
                    if ($match) { throw "Retained state $($item.FullName) contains ambient credential material." }
                }
            }
        }
    }
}

try {
    New-Item -ItemType Directory -Path $root | Out-Null
    $stateHome = Join-Path $root "state home $nonAscii"
    [Environment]::SetEnvironmentVariable('KMX_HOME', $stateHome, 'Process')
    [Environment]::SetEnvironmentVariable('KMX_TOOLCHAIN', 'off', 'Process')
    [Environment]::SetEnvironmentVariable('MODEL', $null, 'Process')
    [Environment]::SetEnvironmentVariable('PATH', '', 'Process')
    foreach ($name in @(
        'COPILOT_GITHUB_TOKEN', 'GH_TOKEN', 'GITHUB_TOKEN',
        'OPENAI_API_KEY', 'AZURE_OPENAI_API_KEY', 'ANTHROPIC_API_KEY',
        'GOOGLE_API_KEY', 'GEMINI_API_KEY', 'MODEL_API_KEY', 'ORKA_API_KEY',
        'AZURE_OPENAI_ENDPOINT', 'OLLAMA_HOST'
    )) {
        [Environment]::SetEnvironmentVariable($name, $null, 'Process')
    }
    foreach ($name in @('HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY')) {
        [Environment]::SetEnvironmentVariable($name, 'http://127.0.0.1:1', 'Process')
    }
    [Environment]::SetEnvironmentVariable('NO_PROXY', '', 'Process')
    [Net.WebRequest]::DefaultWebProxy = [Net.WebProxy]::new('http://127.0.0.1:1', $false)
    $canaryName = 'KMX_WINDOWS_AMBIENT_CREDENTIAL'
    $canaryValue = 'github_pat_' + ('a' * 82)
    [Environment]::SetEnvironmentVariable($canaryName, $canaryValue, 'Process')

    $version = Invoke-Kmx -CommandArgs @('version')
    $versionLine = ($version.Stdout -split '\r?\n')[0]
    if ($ExpectedVersionLine -ne '') {
        Assert-Contract ($versionLine -ceq $ExpectedVersionLine) "Version line '$versionLine' did not equal '$ExpectedVersionLine'."
    } else {
        Assert-Contract ($versionLine -match '^kmx (?:v[0-9]+\.[0-9]+\.[0-9]+[0-9A-Za-z.+-]*|unknown) \((?:release build|development build from a checkout|development build from a MODIFIED checkout|installed with go install|built without version or revision information|no build information recorded)\)$') "Unexpected version line '$versionLine'."
    }
    $help = Invoke-Kmx -CommandArgs @('--help')
    Assert-Contract ($help.Stdout -match '(?m)^Usage:' -and $help.Stdout -match '(?m)^Available Commands:') 'Root help contract is incomplete.'
    $completion = Invoke-Kmx -CommandArgs @('completion', 'bash')
    Assert-Contract ($completion.Stdout.Contains('__start_kmx()') -and $completion.Stdout.Contains('complete -o default')) 'Bash completion contract is incomplete.'
    Assert-Contract (-not (Test-Path -LiteralPath $stateHome)) 'Version, help or completion created state.'

    $workspace = Join-Path $root "fixture workspace $nonAscii"
    New-Item -ItemType Directory -Path $workspace | Out-Null
    $agent = Join-Path $workspace "greeter $nonAscii.agent.md"
    $fixtureBytes = [IO.File]::ReadAllBytes($fixturePath)
    [IO.File]::WriteAllBytes($agent, $fixtureBytes)

    $run = Invoke-Kmx -CommandArgs @('run', $agent, '--prompt', 'Grace', '--output', 'json')
    $runJSON = $run.Stdout | ConvertFrom-Json -Depth 20
    Assert-Contract ($runJSON.schemaVersion -eq 1 -and $runJSON.kind -ceq 'kmx.agent-file-preview') 'Run JSON schema contract changed.'
    Assert-Contract ($runJSON.operation -ceq 'run' -and $runJSON.state -ceq 'complete' -and $runJSON.verdict -ceq 'unknown' -and $runJSON.fullPass -eq $false -and $runJSON.reasonCode -ceq 'completion_ready') 'Run JSON outcome contract changed.'
    Assert-Contract ($runJSON.modelRoute.provider -ceq 'fake' -and $runJSON.modelRoute.model -ceq 'fake') 'Run did not use the offline fake route.'
    Assert-Contract ($runJSON.selection.scope -ceq 'full' -and $runJSON.selection.totalCases -eq 1 -and @($runJSON.selection.caseIds).Count -eq 1 -and $runJSON.selection.caseIds[0] -ceq 'greeting') 'Run selection contract changed.'
    Assert-Contract ($runJSON.adHoc.state -ceq 'complete' -and $runJSON.adHoc.answer -ceq 'Hello, Grace') 'Run ad-hoc answer contract changed.'
    Assert-Contract (@($runJSON.examples).Count -eq 1 -and $runJSON.examples[0].state -ceq 'complete' -and $runJSON.examples[0].verdict -ceq 'unknown' -and @($runJSON.examples[0].assertions).Count -eq 0) 'Run unexpectedly evaluated checks.'
    Assert-Contract ($runJSON.retention.status -ceq 'not_requested') 'Run unexpectedly requested failure retention.'

    $source = [Text.Encoding]::UTF8.GetString($fixtureBytes)
    $regressed = $source.Replace('Reply with exactly `Hello, `', 'Reply with exactly `Hi, `')
    Assert-Contract ($regressed -cne $source) 'Fixture does not contain the expected fake instruction.'
    [IO.File]::WriteAllText($agent, $regressed, [Text.UTF8Encoding]::new($false))

    $failed = Invoke-Kmx -CommandArgs @('test', $agent, '--output', 'json') -ExpectedExitCodes @(1)
    $failedJSON = $failed.Stdout | ConvertFrom-Json -Depth 20
    Assert-Contract ($failedJSON.schemaVersion -eq 1 -and $failedJSON.kind -ceq 'kmx.agent-file-preview') 'Test JSON schema contract changed.'
    Assert-Contract ($failedJSON.operation -ceq 'test' -and $failedJSON.state -ceq 'complete' -and $failedJSON.verdict -ceq 'fail' -and $failedJSON.fullPass -eq $false -and $failedJSON.reasonCode -ceq 'assertion_failed') 'Regressed Test outcome contract changed.'
    Assert-Contract ($failedJSON.modelRoute.provider -ceq 'fake' -and $failedJSON.modelRoute.model -ceq 'fake') 'Test did not use the offline fake route.'
    Assert-Contract (@($failedJSON.examples).Count -eq 1 -and $failedJSON.examples[0].id -ceq 'greeting' -and $failedJSON.examples[0].state -ceq 'complete' -and $failedJSON.examples[0].verdict -ceq 'fail') 'Test example attribution changed.'
    Assert-Contract (@($failedJSON.examples[0].assertions).Count -eq 1 -and $failedJSON.examples[0].assertions[0].id -ceq 'greeting-text' -and $failedJSON.examples[0].assertions[0].type -ceq 'contains' -and $failedJSON.examples[0].assertions[0].verdict -ceq 'fail') 'Test assertion attribution changed.'
    Assert-Contract ($failedJSON.retention.status -ceq 'retained' -and @($failedJSON.retention.diagnostics).Count -eq 1 -and $failedJSON.retention.diagnostics[0].caseId -ceq 'greeting') 'Failing Test did not retain one attributed diagnostic.'
    $diagnosticID = [string]$failedJSON.retention.diagnostics[0].id
    Assert-Contract ($diagnosticID -match '^f-[0-9a-f]{32}$') 'Retained diagnostic ID is not canonical.'

    Assert-Contract (Test-Path -LiteralPath (Join-Path $stateHome 'local-preview\journals') -PathType Container) 'Run/Test journals were not retained.'
    Assert-Contract (Test-Path -LiteralPath (Join-Path $stateHome 'eval-failures') -PathType Container) 'Failure diagnostics were not retained.'
    $beforeExplain = Get-StateSnapshot $stateHome
    Assert-NoCanary $stateHome @($canaryName, $canaryValue)

    $current = Invoke-Kmx -CommandArgs @('test', 'explain', $diagnosticID, '--file', $agent)
    Assert-Contract ($current.Stdout.Contains($diagnosticID) -and $current.Stdout.Contains('current') -and $current.Stdout.Contains('revision ' + $failedJSON.file.revision) -and $current.Stdout.Contains('fake/fake')) 'Offline current Explain omitted retained identity.'
    Assert-SnapshotEqual $beforeExplain (Get-StateSnapshot $stateHome) 'Current Explain'

    [IO.File]::WriteAllBytes($agent, $fixtureBytes)
    $historical = Invoke-Kmx -CommandArgs @('test', 'explain', $diagnosticID, '--file', $agent)
    Assert-Contract ($historical.Stdout.Contains($diagnosticID) -and $historical.Stdout.Contains('historical') -and $historical.Stdout.Contains('revision ' + $failedJSON.file.revision)) 'Offline historical Explain omitted the regressed revision.'
    Assert-SnapshotEqual $beforeExplain (Get-StateSnapshot $stateHome) 'Historical Explain'
    Assert-NoCanary $stateHome @($canaryName, $canaryValue)

    $deferredHome = Join-Path $root "deferred state $nonAscii"
    $deferredWorkspace = Join-Path $root "deferred workspace $nonAscii"
    [Environment]::SetEnvironmentVariable('KMX_HOME', $deferredHome, 'Process')
    $deferred = Invoke-Kmx -CommandArgs @('create', 'deferred-greeter', '--workspace', $deferredWorkspace) -ExpectedExitCodes @(1)
    Assert-Contract ([string]::IsNullOrWhiteSpace($deferred.Stdout)) 'Deferred command wrote ordinary output before refusal.'
    Assert-Contract ($deferred.Stderr -match '(?i)(not available|unsupported|refus)') 'Deferred command did not report an operation refusal.'
    Assert-Contract (-not (Test-Path -LiteralPath $deferredHome) -and -not (Test-Path -LiteralPath $deferredWorkspace)) 'Deferred command created state before refusal.'

    Write-Output 'Native Windows amd64 day-zero CLI qualification passed.'
} finally {
    [Net.WebRequest]::DefaultWebProxy = $savedDefaultProxy
    foreach ($name in $environmentNames) {
        [Environment]::SetEnvironmentVariable($name, $savedEnvironment[$name], 'Process')
    }
    if (Test-Path -LiteralPath $root) {
        Remove-Item -LiteralPath $root -Recurse -Force
    }
}

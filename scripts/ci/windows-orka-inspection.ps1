param(
    [Parameter(Mandatory = $true)][string]$Binary,
    [Parameter(Mandatory = $true)][string]$Kubectl,
    [bool]$ProtocolFixtureMode = $true
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$exe = (Resolve-Path -LiteralPath $Binary).ProviderPath
$kubectlExe = (Resolve-Path -LiteralPath $Kubectl).ProviderPath
if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) { throw 'Binary must be a regular file.' }
if (-not (Test-Path -LiteralPath $kubectlExe -PathType Leaf)) { throw 'Kubectl fixture must be a regular file.' }

$nonAscii = [char]0x96EA
$root = Join-Path ([IO.Path]::GetTempPath()) ("kmx remote inspect $nonAscii " + [Guid]::NewGuid().ToString('N'))
if (Test-Path -LiteralPath $root) { throw 'Remote-inspection qualification requires a fresh root.' }

$saved = @{}
foreach ($name in @('PATH', 'KMX_HOME', 'KMX_TOOLCHAIN', 'KMX_KUBECTL_FIXTURE_MODE',
        'KUBECONFIG',
        'KMX_INSPECT_CALLS', 'KMX_INSPECT_AGENTS', 'KMX_INSPECT_AGENT',
        'KMX_INSPECT_PROVIDER', 'KMX_INSPECT_SECRET', 'KMX_INSPECT_TASKS')) {
    $saved[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}

function Invoke-Kmx {
    param([Parameter(Mandatory = $true)][string[]]$CommandArgs)
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $exe
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    foreach ($argument in $CommandArgs) { [void]$start.ArgumentList.Add($argument) }
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $start
    if (-not $process.Start()) { throw 'Could not start kmx.' }
    $stdoutTask = $process.StandardOutput.ReadToEndAsync()
    $stderrTask = $process.StandardError.ReadToEndAsync()
    if (-not $process.WaitForExit(180000)) {
        $process.Kill($true)
        $process.WaitForExit()
        throw 'Remote inspection exceeded the three-minute bound.'
    }
    $stdout, $stderr, $status = $stdoutTask.Result, $stderrTask.Result, $process.ExitCode
    $process.Dispose()
    if ($stdout.Length -gt 0) { [Console]::Out.Write($stdout) }
    if ($stderr.Length -gt 0) { [Console]::Error.Write($stderr) }
    if ($status -ne 0) { throw "kmx exited $status" }
    return $stdout
}

try {
    New-Item -ItemType Directory -Path $root | Out-Null
    $fixtureDir = Join-Path $root "native tools $nonAscii"
    New-Item -ItemType Directory -Path $fixtureDir | Out-Null
    $fixture = Join-Path $fixtureDir 'kubectl.exe'
    Copy-Item -LiteralPath $kubectlExe -Destination $fixture
    $state = Join-Path $root "state must not exist $nonAscii"
    $calls = Join-Path $root 'calls.txt'
    $kubeconfig = Join-Path $root 'empty kubeconfig.yaml'
    [IO.File]::WriteAllText($kubeconfig, "apiVersion: v1`nkind: Config`nclusters: []`ncontexts: []`nusers: []`n", [Text.UTF8Encoding]::new($false))

    [Environment]::SetEnvironmentVariable('PATH', $fixtureDir, 'Process')
    [Environment]::SetEnvironmentVariable('KMX_HOME', $state, 'Process')
    [Environment]::SetEnvironmentVariable('KMX_TOOLCHAIN', 'off', 'Process')
    [Environment]::SetEnvironmentVariable('KUBECONFIG', $kubeconfig, 'Process')
    if ($ProtocolFixtureMode) {
        [Environment]::SetEnvironmentVariable('KMX_KUBECTL_FIXTURE_MODE', 'inspect', 'Process')
        [Environment]::SetEnvironmentVariable('KMX_INSPECT_CALLS', $calls, 'Process')
        [Environment]::SetEnvironmentVariable('KMX_INSPECT_AGENTS', '{"items":[{"metadata":{"name":"concierge","namespace":"demo"},"spec":{"providerRef":{"name":"local"},"model":{"name":"fixture-model"}},"status":{"ready":true}}]}', 'Process')
        [Environment]::SetEnvironmentVariable('KMX_INSPECT_AGENT', '{"metadata":{"name":"concierge","namespace":"demo"},"spec":{"providerRef":{"name":"local"},"model":{"name":"fixture-model"}},"status":{"ready":true,"activeTasks":0}}', 'Process')
        [Environment]::SetEnvironmentVariable('KMX_INSPECT_PROVIDER', '{"metadata":{"name":"local","namespace":"demo"},"spec":{"type":"openai","baseURL":"https://model.invalid/v1","defaultModel":"fixture-model","secretRef":{"name":"model-key","key":"api-key"}},"status":{"ready":true}}', 'Process')
        [Environment]::SetEnvironmentVariable('KMX_INSPECT_SECRET', 'secret/model-key', 'Process')
        [Environment]::SetEnvironmentVariable('KMX_INSPECT_TASKS', '{"items":[]}', 'Process')
    } else {
        # A real kubectl proves the exact staged kmx binary accepts only native
        # kubectl and reaches its bounded preflight. There is deliberately no
        # kubeconfig or cluster in the release job, so operational reads refuse.
        $start = [Diagnostics.ProcessStartInfo]::new()
        $start.FileName = $exe
        $start.UseShellExecute = $false
        $start.CreateNoWindow = $true
        $start.RedirectStandardOutput = $true
        $start.RedirectStandardError = $true
        foreach ($argument in @('--context', 'release-unconfigured', 'cluster', 'orka', 'list', '--namespace', 'demo')) {
            [void]$start.ArgumentList.Add($argument)
        }
        $process = [Diagnostics.Process]::new()
        $process.StartInfo = $start
        if (-not $process.Start()) { throw 'Could not start kmx release inspection refusal.' }
        $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        $stderrTask = $process.StandardError.ReadToEndAsync()
        if (-not $process.WaitForExit(180000)) { $process.Kill($true); $process.WaitForExit(); throw 'Release inspection refusal timed out.' }
        $stdout, $stderr, $status = $stdoutTask.Result, $stderrTask.Result, $process.ExitCode
        $process.Dispose()
        if ($status -eq 0 -or -not [string]::IsNullOrWhiteSpace($stdout) -or $stderr -notmatch '(?i)(context|cluster|kubectl|prerequisite|unavailable)') {
            throw "Exact staged executable did not safely reach the native kubectl boundary: status=$status stdout=$stdout stderr=$stderr"
        }
        if (Test-Path -LiteralPath $state) { throw 'Release inspection refusal created KMX state.' }
        Write-Output 'Exact staged Windows amd64 remote Orka inspection boundary passed.'
        return
    }

    $listed = Invoke-Kmx -CommandArgs @('--context', 'kind-test', 'cluster', 'orka', 'list', '--namespace', 'demo', '--output', 'json') | ConvertFrom-Json -Depth 20
    if (@($listed.items).Count -ne 1 -or $listed.items[0].metadata.name -cne 'concierge') {
        throw 'Native list JSON contract changed.'
    }
    $shown = Invoke-Kmx -CommandArgs @('--context', 'kind-test', 'cluster', 'orka', 'show', 'concierge', '--namespace', 'demo', '--output', 'json') | ConvertFrom-Json -Depth 20
    if ($shown.agent.name -cne 'concierge' -or $shown.agent.ready -cne 'yes' -or $shown.provider.name -cne 'local' -or $shown.secret.ready -cne 'present' -or $shown.model -cne 'fixture-model') {
        throw 'Native show JSON contract changed.'
    }
    if (Test-Path -LiteralPath $state) { throw 'Read-only inspection created KMX state.' }
    $callsText = Get-Content -LiteralPath $calls -Raw
    if ($callsText -notmatch '--context kind-test' -or $callsText -notmatch '/agents\?limit=128' -or $callsText -notmatch 'get providers\.core\.orka\.ai') {
        throw 'Inspection did not retain the explicit context and expected reads.'
    }
    Write-Output 'Native Windows amd64 remote Orka inspection qualification passed.'
} finally {
    foreach ($name in $saved.Keys) {
        [Environment]::SetEnvironmentVariable($name, $saved[$name], 'Process')
    }
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
}

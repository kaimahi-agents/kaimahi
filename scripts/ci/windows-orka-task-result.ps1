param(
    [Parameter(Mandatory = $true)][string]$Binary,
    [Parameter(Mandatory = $true)][string]$Kubectl
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$exe = (Resolve-Path -LiteralPath $Binary).ProviderPath
$kubectlExe = (Resolve-Path -LiteralPath $Kubectl).ProviderPath
$root = Join-Path ([IO.Path]::GetTempPath()) ("kmx task result " + [Guid]::NewGuid().ToString('N'))
if (Test-Path -LiteralPath $root) { throw 'Task Result qualification requires a fresh root.' }

$saved = @{}
foreach ($name in @('PATH', 'KMX_HOME', 'KMX_TOOLCHAIN', 'KMX_KUBECTL_FIXTURE_MODE',
        'KMX_EVAL_DIR', 'KMX_EVAL_DIRECT_RESULT', 'KMX_EVAL_ANSWER', 'KMX_EVAL_PHASE',
        'KMX_EVAL_RESULT_AVAILABLE', 'KAIMAHI_CONFIRM')) {
    $saved[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}

function Invoke-Kmx {
    param([Parameter(Mandatory = $true)][string[]]$CommandArgs, [int[]]$AllowedStatus = @(0))
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
        throw 'Task Result exceeded the three-minute bound.'
    }
    $result = [pscustomobject]@{
        Stdout = $stdoutTask.Result
        Stderr = $stderrTask.Result
        Status = $process.ExitCode
    }
    $process.Dispose()
    if ($AllowedStatus -notcontains $result.Status) { throw "kmx exited $($result.Status): $($result.Stderr)" }
    return $result
}

try {
    New-Item -ItemType Directory -Path $root | Out-Null
    $fixtureDir = Join-Path $root 'native-tools'
    New-Item -ItemType Directory -Path $fixtureDir | Out-Null
    $fixture = Join-Path $fixtureDir 'kubectl.exe'
    Copy-Item -LiteralPath $kubectlExe -Destination $fixture
    $state = Join-Path $root 'state-must-not-exist'
    $existing = '{"apiVersion":"core.orka.ai/v1alpha1","kind":"Task","metadata":{"name":"existing-task","namespace":"demo","uid":"existing-uid","generation":1},"spec":{"type":"ai","prompt":"existing"}}'
    [IO.File]::WriteAllText((Join-Path $root 'task-existing-task.json'), $existing, [Text.UTF8Encoding]::new($false))

    [Environment]::SetEnvironmentVariable('PATH', $fixtureDir, 'Process')
    [Environment]::SetEnvironmentVariable('KMX_HOME', $state, 'Process')
    [Environment]::SetEnvironmentVariable('KMX_TOOLCHAIN', 'off', 'Process')
    [Environment]::SetEnvironmentVariable('KMX_KUBECTL_FIXTURE_MODE', 'evaluate', 'Process')
    [Environment]::SetEnvironmentVariable('KMX_EVAL_DIR', $root, 'Process')
    [Environment]::SetEnvironmentVariable('KMX_EVAL_DIRECT_RESULT', '1', 'Process')
    [Environment]::SetEnvironmentVariable('KMX_EVAL_ANSWER', 'native Windows answer', 'Process')
    [Environment]::SetEnvironmentVariable('KMX_EVAL_PHASE', 'Running', 'Process')
    $pending = Invoke-Kmx -CommandArgs @('--context', 'kind-test', 'cluster', 'task', 'result', 'existing-task', '--namespace', 'demo') -AllowedStatus @(2)
    if ($pending.Stdout.Length -ne 0 -or $pending.Stderr -notmatch 'pending') { throw 'Pending Task Result contract changed.' }
    if (Test-Path -LiteralPath (Join-Path $root 'forward-pid')) { throw 'Pending Task Result started a forward.' }

    [Environment]::SetEnvironmentVariable('KMX_EVAL_PHASE', 'Succeeded', 'Process')
    $result = Invoke-Kmx -CommandArgs @('--context', 'kind-test', 'cluster', 'task', 'result', 'existing-task', '--namespace', 'demo')
    if ($result.Stdout -cne "native Windows answer`n") { throw 'Task Result did not print only the answer.' }
    $forwardPID = [int](Get-Content -LiteralPath (Join-Path $root 'forward-pid') -Raw)
    if ($null -ne (Get-Process -Id $forwardPID -ErrorAction SilentlyContinue)) { throw 'Task Result accepted an answer before the owned forward exited.' }
    $token = 'private-' + 'session-token-never-print'
    $observed = $result.Stdout + $result.Stderr + (Get-Content -LiteralPath (Join-Path $root 'calls') -Raw)
    if ($observed.Contains($token)) { throw 'Task Result exposed temporary bearer material.' }

    if (Test-Path -LiteralPath $state) { throw 'Remote Task Result qualification created KMX state.' }
    Write-Output 'Native Windows amd64 Orka Task Result qualification passed.'
} finally {
    foreach ($name in $saved.Keys) {
        [Environment]::SetEnvironmentVariable($name, $saved[$name], 'Process')
    }
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
}

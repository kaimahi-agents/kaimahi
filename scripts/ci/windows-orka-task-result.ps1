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

$fixtureControls = @(
    'KMX_EVAL_REMOTE', 'KMX_EVAL_STOCK_RELEASE', 'KMX_EVAL_FORWARD_DELAY',
    'KMX_EVAL_FORWARD_TARGET', 'KMX_EVAL_DIRECT_RESULT', 'KMX_EVAL_ANSWER',
    'KMX_EVAL_CLUSTER_UID', 'KMX_EVAL_SERVICES', 'KMX_EVAL_AGENT_SWAP_ON_READ',
    'KMX_EVAL_TASK_GET_DELAY', 'KMX_EVAL_PHASE', 'KMX_EVAL_PHASE_SEQUENCE',
    'KMX_EVAL_TASK_SWAP_ON_READ', 'KMX_EVAL_RESULT_AVAILABLE',
    'KMX_EVAL_TASK_DUPLICATE_IDENTITY_ON_READ', 'KMX_EVAL_CREATE_FAIL'
)
$saved = @{}
foreach ($name in @('PATH', 'KMX_HOME', 'KMX_TOOLCHAIN', 'KMX_KUBECTL_FIXTURE_MODE',
        'KMX_EVAL_DIR', 'KAIMAHI_CONFIRM') + $fixtureControls) {
    $saved[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}

function Invoke-Kmx {
    param(
        [Parameter(Mandatory = $true)][string[]]$CommandArgs,
        [int[]]$AllowedStatus = @(0),
        [string]$ForwardPIDPath = ''
    )
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $exe
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    $start.RedirectStandardOutput = $true
    $start.RedirectStandardError = $true
    foreach ($argument in $CommandArgs) { [void]$start.ArgumentList.Add($argument) }
    $process = [Diagnostics.Process]::new()
    $process.StartInfo = $start
    $started = $false
    try {
        if (-not $process.Start()) { throw 'Could not start kmx.' }
        $started = $true
        $stderrTask = $process.StandardError.ReadToEndAsync()
        $deadline = [DateTime]::UtcNow.AddMinutes(3)
        $firstLine = $null
        $stdoutTask = $null
        if ([string]::IsNullOrEmpty($ForwardPIDPath)) {
            $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        } else {
            $firstLineTask = $process.StandardOutput.ReadLineAsync()
            $remaining = [Math]::Max(1, [int][Math]::Ceiling(($deadline - [DateTime]::UtcNow).TotalMilliseconds))
            if (-not $firstLineTask.Wait($remaining)) {
                throw 'Task Result did not publish an answer within the three-minute bound.'
            }
            $firstLine = $firstLineTask.Result
            if ($null -ne $firstLine) {
                if (-not (Test-Path -LiteralPath $ForwardPIDPath -PathType Leaf)) {
                    throw 'Task Result published an answer without a recorded owned forward.'
                }
                $forwardPID = [int](Get-Content -LiteralPath $ForwardPIDPath -Raw)
                if ($null -ne (Get-Process -Id $forwardPID -ErrorAction SilentlyContinue)) {
                    throw 'Task Result accepted an answer before the owned forward exited.'
                }
            }
            $stdoutTask = $process.StandardOutput.ReadToEndAsync()
        }
        $remaining = [Math]::Max(1, [int][Math]::Ceiling(($deadline - [DateTime]::UtcNow).TotalMilliseconds))
        if (-not $process.WaitForExit($remaining)) {
            throw 'Task Result exceeded the three-minute bound.'
        }
        $stdout = $stdoutTask.Result
        if ($null -ne $firstLine) { $stdout = $firstLine + "`n" + $stdout }
        $result = [pscustomobject]@{
            Stdout = $stdout
            Stderr = $stderrTask.Result
            Status = $process.ExitCode
        }
        if ($AllowedStatus -notcontains $result.Status) { throw "kmx exited $($result.Status): $($result.Stderr)" }
        return $result
    } finally {
        if ($started -and -not $process.HasExited) {
            $process.Kill($true)
            $process.WaitForExit()
        }
        $process.Dispose()
    }
}

function Assert-ExactArgs {
    param([Parameter(Mandatory = $true)]$Call, [Parameter(Mandatory = $true)][string[]]$Expected)
    $actual = @($Call.Args)
    if ($actual.Count -ne $Expected.Count) { throw "kubectl argument count changed: $($actual -join ' ')" }
    for ($index = 0; $index -lt $Expected.Count; $index++) {
        if ($actual[$index] -cne $Expected[$index]) { throw "kubectl arguments changed: $($actual -join ' ')" }
    }
}

function Read-KubectlCalls {
    param([Parameter(Mandatory = $true)][string]$Path)
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { return @() }
    return @(Get-Content -LiteralPath $Path | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } |
        ForEach-Object { $_ | ConvertFrom-Json -Depth 20 })
}

try {
    New-Item -ItemType Directory -Path $root | Out-Null
    $fixtureDir = Join-Path $root 'native-tools'
    New-Item -ItemType Directory -Path $fixtureDir | Out-Null
    $fixture = Join-Path $fixtureDir 'kubectl.exe'
    Copy-Item -LiteralPath $kubectlExe -Destination $fixture
    $state = Join-Path $root 'state-must-not-exist'
    $callsPath = Join-Path $root 'calls'
    $taskPath = Join-Path $root 'task-existing-task.json'
    $forwardPIDPath = Join-Path $root 'forward-pid'
    $existing = '{"apiVersion":"core.orka.ai/v1alpha1","kind":"Task","metadata":{"name":"existing-task","namespace":"demo","uid":"existing-uid","generation":1},"spec":{"type":"ai","prompt":"existing"}}'
    [IO.File]::WriteAllText($taskPath, $existing, [Text.UTF8Encoding]::new($false))

    [Environment]::SetEnvironmentVariable('PATH', $fixtureDir, 'Process')
    [Environment]::SetEnvironmentVariable('KMX_HOME', $state, 'Process')
    [Environment]::SetEnvironmentVariable('KMX_TOOLCHAIN', 'off', 'Process')
    [Environment]::SetEnvironmentVariable('KMX_KUBECTL_FIXTURE_MODE', 'evaluate', 'Process')
    [Environment]::SetEnvironmentVariable('KMX_EVAL_DIR', $root, 'Process')
    foreach ($name in $fixtureControls) { [Environment]::SetEnvironmentVariable($name, $null, 'Process') }
    [Environment]::SetEnvironmentVariable('KMX_EVAL_DIRECT_RESULT', '1', 'Process')
    [Environment]::SetEnvironmentVariable('KMX_EVAL_ANSWER', 'native Windows answer', 'Process')
    [Environment]::SetEnvironmentVariable('KMX_EVAL_PHASE', 'Running', 'Process')
    $pending = Invoke-Kmx -CommandArgs @('--context', 'kind-test', 'cluster', 'task', 'result', 'existing-task', '--namespace', 'demo') -AllowedStatus @(2)
    if ($pending.Stdout.Length -ne 0 -or $pending.Stderr -notmatch 'pending') { throw 'Pending Task Result contract changed.' }
    if (Test-Path -LiteralPath $forwardPIDPath) { throw 'Pending Task Result started a forward.' }
    foreach ($call in @(Read-KubectlCalls -Path $callsPath)) {
        if ($null -ne $call.Document -or @($call.Args) -contains 'token' -or @($call.Args) -contains 'port-forward') {
            throw 'Pending Task Result performed a write or opened result authority.'
        }
    }
    [IO.File]::WriteAllText($callsPath, '', [Text.UTF8Encoding]::new($false))

    [Environment]::SetEnvironmentVariable('KMX_EVAL_PHASE', 'Succeeded', 'Process')
    $result = Invoke-Kmx -CommandArgs @('--context', 'kind-test', 'cluster', 'task', 'result', 'existing-task', '--namespace', 'demo') -ForwardPIDPath $forwardPIDPath
    if ($result.Stdout -cne "native Windows answer`n") { throw 'Task Result did not print only the answer.' }
    if ([IO.File]::ReadAllText($taskPath) -cne $existing) { throw 'Task Result changed the existing Task.' }

    $calls = @(Read-KubectlCalls -Path $callsPath)
    if (@($calls | Where-Object { $null -ne $_.Document }).Count -ne 0) { throw 'Task Result submitted a Kubernetes document.' }
    foreach ($call in $calls) {
        $callArgs = @($call.Args)
        foreach ($verb in @('apply', 'delete', 'patch', 'replace')) {
            if ($callArgs -contains $verb) { throw "Task Result invoked mutating kubectl verb $verb." }
        }
        $create = [Array]::IndexOf($callArgs, 'create')
        if ($create -ge 0 -and ($create + 1 -ge $callArgs.Count -or $callArgs[$create + 1] -cne 'token')) {
            throw 'Task Result created or resent a Kubernetes resource.'
        }
    }
    $tokenCalls = @($calls | Where-Object { @($_.Args) -contains 'token' })
    $forwardCalls = @($calls | Where-Object { @($_.Args) -contains 'port-forward' })
    if ($tokenCalls.Count -ne 1 -or $forwardCalls.Count -ne 1) { throw 'Task Result authority call count changed.' }
    Assert-ExactArgs -Call $tokenCalls[0] -Expected @('--context', 'kind-test', '--request-timeout=10s', '-n', 'demo', 'create', 'token', 'orka-result-reader', '--duration=10m', '-o', 'json')
    Assert-ExactArgs -Call $forwardCalls[0] -Expected @('--context', 'kind-test', '-n', 'demo', 'port-forward', '--address', '127.0.0.1', 'svc/orka', ':8080')

    $token = 'private-' + 'session-token-never-print'
    $observed = $result.Stdout + $result.Stderr + (Get-Content -LiteralPath $callsPath -Raw)
    if ($observed.Contains($token)) { throw 'Task Result exposed temporary bearer material.' }

    if (Test-Path -LiteralPath $state) { throw 'Remote Task Result qualification created KMX state.' }
    Write-Output 'Native Windows amd64 Orka Task Result qualification passed.'
} finally {
    foreach ($name in $saved.Keys) {
        [Environment]::SetEnvironmentVariable($name, $saved[$name], 'Process')
    }
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
}

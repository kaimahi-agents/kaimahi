param(
    [Parameter(Mandatory = $true)][string]$Binary,
    [Parameter(Mandatory = $true)][string]$Kubectl,
    [bool]$ProtocolFixtureMode = $true
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$exe = (Resolve-Path -LiteralPath $Binary).ProviderPath
$kubectlExe = (Resolve-Path -LiteralPath $Kubectl).ProviderPath
$powershellExe = (Get-Process -Id $PID).Path
if (-not (Test-Path -LiteralPath $exe -PathType Leaf)) { throw 'Binary must be a regular file.' }
if (-not (Test-Path -LiteralPath $kubectlExe -PathType Leaf)) { throw 'Kubectl fixture must be a regular file.' }

$nonAscii = [char]0x96EA
$root = Join-Path ([IO.Path]::GetTempPath()) ("kmx remote inspect $nonAscii " + [Guid]::NewGuid().ToString('N'))
if (Test-Path -LiteralPath $root) { throw 'Remote-inspection qualification requires a fresh root.' }

$saved = @{}
$serverProcess = $null
foreach ($name in @('PATH', 'KMX_HOME', 'KMX_TOOLCHAIN', 'KMX_KUBECTL_FIXTURE_MODE',
        'KUBECONFIG',
        'KMX_INSPECT_CALLS', 'KMX_INSPECT_AGENTS', 'KMX_INSPECT_AGENT',
        'KMX_INSPECT_PROVIDER', 'KMX_INSPECT_TASKS')) {
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
    $fixtureHash = (Get-FileHash -LiteralPath $fixture -Algorithm SHA256).Hash.ToLowerInvariant()
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
        [Environment]::SetEnvironmentVariable('KMX_INSPECT_AGENTS', '{"apiVersion":"core.orka.ai/v1alpha1","kind":"AgentList","metadata":{},"items":[{"apiVersion":"core.orka.ai/v1alpha1","kind":"Agent","metadata":{"name":"concierge","namespace":"demo"},"spec":{"providerRef":{"name":"local"},"model":{"name":"fixture-model"}},"status":{"ready":true}}]}', 'Process')
        [Environment]::SetEnvironmentVariable('KMX_INSPECT_AGENT', '{"apiVersion":"core.orka.ai/v1alpha1","kind":"Agent","metadata":{"name":"concierge","namespace":"demo"},"spec":{"providerRef":{"name":"local"},"model":{"name":"fixture-model"}},"status":{"ready":true,"activeTasks":0}}', 'Process')
        [Environment]::SetEnvironmentVariable('KMX_INSPECT_PROVIDER', '{"apiVersion":"core.orka.ai/v1alpha1","kind":"Provider","metadata":{"name":"local","namespace":"demo"},"spec":{"type":"openai","baseURL":"https://model.invalid/v1","defaultModel":"fixture-model","secretRef":{"name":"model-key","key":"api-key"}},"status":{"ready":true}}', 'Process')
        [Environment]::SetEnvironmentVariable('KMX_INSPECT_TASKS', '{"items":[]}', 'Process')
    } else {
        foreach ($name in @('KMX_KUBECTL_FIXTURE_MODE', 'KMX_INSPECT_CALLS', 'KMX_INSPECT_AGENTS',
                'KMX_INSPECT_AGENT', 'KMX_INSPECT_PROVIDER', 'KMX_INSPECT_TASKS')) {
            [Environment]::SetEnvironmentVariable($name, $null, 'Process')
        }

        # Serve one checked Kubernetes raw-list response. Success proves the
        # exact staged kmx binary started the checksum-verified real kubectl,
        # retained the explicit context and consumed the bounded API result.
        $ready = Join-Path $root 'api-ready.txt'
        $observed = Join-Path $root 'api-request.txt'
        $serverScript = Join-Path $root 'api-server.ps1'
        $serverSource = @'
param([string]$Ready, [string]$Observed, [string]$ExpectedKubectl, [string]$ExpectedSHA256)
$ErrorActionPreference = 'Stop'
$listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
try {
    $listener.Start()
    $port = ([Net.IPEndPoint]$listener.LocalEndpoint).Port
    $readyTemp = "$Ready.pending"
    [IO.File]::WriteAllText($readyTemp, [string]$port, [Text.UTF8Encoding]::new($false))
    [IO.File]::Move($readyTemp, $Ready)
    $client = $listener.AcceptTcpClient()
    try {
        $client.ReceiveTimeout = 10000
        $client.SendTimeout = 10000
        $serverEndpoint = [Net.IPEndPoint]$client.Client.LocalEndPoint
        $clientEndpoint = [Net.IPEndPoint]$client.Client.RemoteEndPoint
        $connections = @(Get-NetTCPConnection -State Established `
            -LocalAddress $clientEndpoint.Address.ToString() -LocalPort $clientEndpoint.Port `
            -RemoteAddress $serverEndpoint.Address.ToString() -RemotePort $serverEndpoint.Port)
        if ($connections.Count -ne 1 -or $connections[0].OwningProcess -le 0) {
            throw 'request owner could not be identified'
        }
        $owner = Get-Process -Id $connections[0].OwningProcess -ErrorAction Stop
        $ownerPath = $owner.Path
        if ([string]::IsNullOrWhiteSpace($ownerPath) -or
                -not [string]::Equals([IO.Path]::GetFullPath($ownerPath), [IO.Path]::GetFullPath($ExpectedKubectl), [StringComparison]::OrdinalIgnoreCase)) {
            throw 'request owner was not the selected kubectl executable'
        }
        $ownerHash = (Get-FileHash -LiteralPath $ownerPath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($ExpectedSHA256 -cnotmatch '^[0-9a-f]{64}$' -or $ownerHash -cne $ExpectedSHA256) {
            throw 'request owner did not match the selected kubectl digest'
        }
        $stream = $client.GetStream()
        $reader = [IO.StreamReader]::new($stream, [Text.Encoding]::ASCII, $false, 1024, $true)
        $request = $reader.ReadLine()
        while (($line = $reader.ReadLine()) -ne '') {
            if ($null -eq $line) { throw 'request headers ended unexpectedly' }
        }
        $expected = 'GET /apis/core.orka.ai/v1alpha1/namespaces/demo/agents?limit=128 HTTP/1.1'
        if ($request -cne $expected) { throw 'request did not match the bounded Agent-list path' }
        $body = '{"apiVersion":"core.orka.ai/v1alpha1","kind":"AgentList","metadata":{},"items":[{"apiVersion":"core.orka.ai/v1alpha1","kind":"Agent","metadata":{"name":"release-agent","namespace":"demo"},"spec":{"providerRef":{"name":"release-provider"},"model":{"name":"release-model"}},"status":{"ready":true}}]}'
        $bytes = [Text.Encoding]::UTF8.GetBytes($body)
        $headers = [Text.Encoding]::ASCII.GetBytes("HTTP/1.1 200 OK`r`nContent-Type: application/json`r`nContent-Length: $($bytes.Length)`r`nConnection: close`r`n`r`n")
        $stream.Write($headers, 0, $headers.Length)
        $stream.Write($bytes, 0, $bytes.Length)
        $stream.Flush()
        [IO.File]::WriteAllText($Observed, $request, [Text.UTF8Encoding]::new($false))
    } finally {
        $client.Dispose()
    }
} finally {
    $listener.Stop()
}
'@
        [IO.File]::WriteAllText($serverScript, $serverSource, [Text.UTF8Encoding]::new($false))
        $serverStart = [Diagnostics.ProcessStartInfo]::new()
        $serverStart.FileName = $powershellExe
        $serverStart.UseShellExecute = $false
        $serverStart.CreateNoWindow = $true
        $serverStart.RedirectStandardOutput = $true
        $serverStart.RedirectStandardError = $true
        foreach ($argument in @('-NoProfile', '-NonInteractive', '-File', $serverScript,
                '-Ready', $ready, '-Observed', $observed,
                '-ExpectedKubectl', $fixture, '-ExpectedSHA256', $fixtureHash)) {
            [void]$serverStart.ArgumentList.Add($argument)
        }
        $serverProcess = [Diagnostics.Process]::new()
        $serverProcess.StartInfo = $serverStart
        if (-not $serverProcess.Start()) { throw 'Could not start the local API fixture.' }
        $deadline = [DateTime]::UtcNow.AddSeconds(15)
        while (-not (Test-Path -LiteralPath $ready)) {
            if ($serverProcess.HasExited) { throw "Local API fixture exited early: $($serverProcess.StandardError.ReadToEnd())" }
            if ([DateTime]::UtcNow -gt $deadline) { throw 'Local API fixture did not publish its port.' }
            Start-Sleep -Milliseconds 25
        }
        $port = (Get-Content -LiteralPath $ready -Raw).Trim()
        if ($port -cnotmatch '^[1-9][0-9]{0,4}$' -or [int]$port -gt 65535) { throw 'Local API fixture published an invalid port.' }
        $config = @"
apiVersion: v1
kind: Config
clusters:
- name: release-fixture
  cluster:
    server: http://127.0.0.1:$port
contexts:
- name: release-fixture
  context:
    cluster: release-fixture
    user: release-fixture
current-context: release-fixture
users:
- name: release-fixture
  user: {}
"@
        [IO.File]::WriteAllText($kubeconfig, $config, [Text.UTF8Encoding]::new($false))
        $listed = Invoke-Kmx -CommandArgs @('--context', 'release-fixture', 'cluster', 'orka', 'list', '--namespace', 'demo')
        if ($listed -notmatch 'release-agent' -or $listed -notmatch 'release-model') {
            throw 'Exact staged executable did not render the checked local API response.'
        }
        if (-not $serverProcess.WaitForExit(15000)) { throw 'Local API fixture did not finish.' }
        if ($serverProcess.ExitCode -ne 0) { throw "Local API fixture failed: $($serverProcess.StandardError.ReadToEnd())" }
        $request = (Get-Content -LiteralPath $observed -Raw).Trim()
        if ($request -cne 'GET /apis/core.orka.ai/v1alpha1/namespaces/demo/agents?limit=128 HTTP/1.1') {
            throw 'Exact staged executable did not make the expected bounded kubectl request.'
        }
        if (Test-Path -LiteralPath $state) { throw 'Release inspection qualification created KMX state.' }
        Write-Output 'Exact staged Windows amd64 remote Orka inspection qualification passed.'
        return
    }

    $listed = Invoke-Kmx -CommandArgs @('--context', 'kind-test', 'cluster', 'orka', 'list', '--namespace', 'demo')
    if ($listed -notmatch 'concierge' -or $listed -notmatch 'fixture-model') {
        throw 'Native list table contract changed.'
    }
    $shown = Invoke-Kmx -CommandArgs @('--context', 'kind-test', 'cluster', 'orka', 'show', 'concierge', '--namespace', 'demo', '--output', 'json') | ConvertFrom-Json -Depth 20
    if ($shown.agent.name -cne 'concierge' -or $shown.agent.ready -cne 'yes' -or $shown.provider.name -cne 'local' -or $shown.secret.ready -notlike 'unknown*' -or $shown.model -cne 'fixture-model') {
        throw 'Native show JSON contract changed.'
    }
    if (Test-Path -LiteralPath $state) { throw 'Read-only inspection created KMX state.' }
    $callsText = Get-Content -LiteralPath $calls -Raw
    if ($callsText -notmatch '--context kind-test' -or $callsText -notmatch '/agents\?limit=128' -or $callsText -notmatch 'get providers\.core\.orka\.ai') {
        throw 'Inspection did not retain the explicit context and expected reads.'
    }
    Write-Output 'Native Windows amd64 remote Orka inspection qualification passed.'
} finally {
    if ($null -ne $serverProcess) {
        if (-not $serverProcess.HasExited) { $serverProcess.Kill($true); $serverProcess.WaitForExit() }
        $serverProcess.Dispose()
    }
    foreach ($name in $saved.Keys) {
        [Environment]::SetEnvironmentVariable($name, $saved[$name], 'Process')
    }
    if (Test-Path -LiteralPath $root) { Remove-Item -LiteralPath $root -Recurse -Force }
}

[CmdletBinding()]
param(
    [ValidateSet('Run', 'Stop')][string]$Action = 'Run',
    [ValidateRange(1024, 65535)][int]$WebPort = 13430
)
$ErrorActionPreference = 'Stop'
$taskRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..'))
$taskOutput = Join-Path $taskRoot 'dist\balance-validation'
$taskPreview = 'magpie-balance-preview'
$taskVerifier = 'magpie-balance-verifier'
$taskImage = 'magpie-balance-validation:local'
$taskLabel = 'ai.magpie.balance-validation'
function Invoke-ValidationDocker {
    param([Parameter(ValueFromRemainingArguments)][string[]]$DockerArgs)
    & docker --context desktop-linux @DockerArgs
    if ($LASTEXITCODE -ne 0) { throw "Docker failed ($LASTEXITCODE): $($DockerArgs -join ' ')" }
}
function Test-ValidationEngine {
    try { & docker --context desktop-linux info --format '{{.OSType}}' 2>$null | Out-Null; return $LASTEXITCODE -eq 0 } catch { return $false }
}
function Remove-OwnedContainer([string]$Name) {
    $taskIDs = @(& docker --context desktop-linux ps -aq --filter "name=^/$Name`$" 2>$null)
    if ($taskIDs.Count -eq 0) { return }
    $taskInspection = (& docker --context desktop-linux inspect $Name | ConvertFrom-Json)[0]
    if ($taskInspection.Config.Labels.$taskLabel -ne $taskRoot) { throw "Container $Name exists and is not owned by this validation module." }
    Invoke-ValidationDocker rm -f $Name | Out-Null
}
[IO.Directory]::CreateDirectory($taskOutput) | Out-Null
if (-not (Test-ValidationEngine)) {
    $taskDesktop = Join-Path $env:ProgramFiles 'Docker\Docker\Docker Desktop.exe'
    if (-not (Test-Path -LiteralPath $taskDesktop)) { throw 'Docker Desktop is not installed.' }
    Start-Process -FilePath $taskDesktop -WindowStyle Hidden
    $taskDeadline = [DateTime]::UtcNow.AddSeconds(180)
    while (-not (Test-ValidationEngine)) {
        if ([DateTime]::UtcNow -ge $taskDeadline) {
            & docker --context desktop-linux version 2>&1 | Out-File (Join-Path $taskOutput 'docker-startup.log')
            throw 'Docker did not become ready within 180 seconds; see docker-startup.log.'
        }
        Start-Sleep -Seconds 5
    }
}
if ($Action -eq 'Stop') {
    Remove-OwnedContainer $taskPreview
    Remove-OwnedContainer $taskVerifier
    Write-Output 'Stopped this validation module; Docker Desktop and unrelated resources were kept.'
    return
}
Remove-OwnedContainer $taskPreview
Remove-OwnedContainer $taskVerifier
$taskListener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, $WebPort)
try { $taskListener.Start() } catch { throw "Preview port $WebPort is occupied; use -WebPort to select another port." } finally { $taskListener.Stop() }
Push-Location $taskRoot
try {
    $taskVersion = (& git describe --tags --always --dirty).Trim()
    $taskGoVersion = ((Get-Content go.mod | Where-Object { $_ -match '^go ' }) -split '\s+')[1]
    $taskFiles = @(& git ls-files --cached --others --exclude-standard)
    $taskSources = @($taskFiles | Sort-Object -Unique | ForEach-Object {
        $taskFile = Join-Path $taskRoot $_
        if (Test-Path -LiteralPath $taskFile -PathType Leaf) { [ordered]@{ path = $_; sha256 = (Get-FileHash -LiteralPath $taskFile -Algorithm SHA256).Hash.ToLowerInvariant() } }
    })
    $taskMetadata = [ordered]@{
        branch = (& git branch --show-current).Trim(); commit = (& git rev-parse HEAD).Trim()
        version = $taskVersion; status = @(& git status --short); sources = $taskSources
        builtAtUTC = [DateTime]::UtcNow.ToString('o'); windowsTarget = 'windows/amd64'
        verification = 'running'; nativeWindowsWindow = 'not run'
    }
    $taskMetadata | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $taskOutput 'build-info.json') -Encoding utf8
    $taskBuildArgs = @('build', '--progress=plain', '-f', 'build/validation/Dockerfile', '--build-arg', "GO_IMAGE=golang:$taskGoVersion-bookworm", '--build-arg', "VERSION=$taskVersion", '-t', $taskImage, '.')
    Invoke-ValidationDocker @taskBuildArgs 2>&1 | Tee-Object -FilePath (Join-Path $taskOutput 'build.log')
    $taskRunArgs = @('run', '--name', $taskVerifier, '--label', "$taskLabel=$taskRoot", '--mount', "type=bind,source=$taskOutput,target=/artifacts", '--mount', 'type=volume,source=magpie-balance-validation-gobuild,target=/root/.cache/go-build', $taskImage, 'verify')
    try { Invoke-ValidationDocker @taskRunArgs 2>&1 | Tee-Object -FilePath (Join-Path $taskOutput 'verification.log') }
    finally { Remove-OwnedContainer $taskVerifier }
    $taskKey = [Guid]::NewGuid().ToString('N')
    $taskPreviewArgs = @('run', '-d', '--name', $taskPreview, '--label', "$taskLabel=$taskRoot", '-p', "127.0.0.1:${WebPort}:3430", '-e', "MAGPIE_WEB_KEY=$taskKey", $taskImage, 'preview')
    Invoke-ValidationDocker @taskPreviewArgs | Out-Null
    $taskURL = "http://127.0.0.1:$WebPort/?k=$taskKey&view=providers&locale=zh"
    $taskReady = $false
    for ($taskAttempt = 0; $taskAttempt -lt 60; $taskAttempt++) {
        try { if ((Invoke-WebRequest -Uri $taskURL -TimeoutSec 2).StatusCode -eq 200) { $taskReady = $true; break } } catch {}
        Start-Sleep -Seconds 1
    }
    if (-not $taskReady) {
        Invoke-ValidationDocker logs $taskPreview | Out-File (Join-Path $taskOutput 'preview.log')
        throw 'Preview failed to become ready; see preview.log.'
    }
    $taskMetadata.verification = 'passed'
    $taskMetadata['exeSHA256'] = (Get-FileHash -LiteralPath (Join-Path $taskOutput 'magpie-windows-amd64.exe') -Algorithm SHA256).Hash.ToLowerInvariant()
    $taskMetadata['previewURL'] = $taskURL
    $taskMetadata | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $taskOutput 'build-info.json') -Encoding utf8
    @"
# 余额查询模板验证结果

- 分支：$($taskMetadata.branch)
- 源码基点：$($taskMetadata.commit)，版本：$taskVersion，工作区状态和源文件校验值见 build-info.json。
- 通过：容器内 make test；Chromium / WebKit 浏览器回归；中英文真实 Web 界面新增、保存、重新打开及余额查询。
- 余额协议：实际 HTTP 请求到容器内模拟供应商，New API 为 `$3.00，Sub2API 为 `$12.50；使用演示令牌，不涉及真实供应商凭据。
- Windows amd64 桌面 EXE 已交叉编译并导出；Windows 原生窗口尚未运行。
- EXE SHA-256：$($taskMetadata.exeSHA256)
- 预览：[$taskURL]($taskURL)
- New API 演示令牌：demo-newapi-access-token；账户 ID：42；API key：sk-validation-api-key。
- Sub2API 演示 JWT：eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2ln。
- 停止预览：在仓库运行 build/validation/validate.ps1 -Action Stop。

日志：build.log、go-tests.log、browser-tests.log、e2e-tests.log。截图和供应商实际请求记录在 screenshots/。
"@ | Set-Content (Join-Path $taskOutput 'VERIFICATION.md') -Encoding utf8
    Write-Output "EXE: $(Join-Path $taskOutput 'magpie-windows-amd64.exe')"
    Write-Output "Preview: $taskURL"
} catch {
    if ($taskMetadata) {
        $taskMetadata.verification = 'failed'
        $taskMetadata['error'] = $_.Exception.Message
        $taskMetadata | ConvertTo-Json -Depth 8 | Set-Content (Join-Path $taskOutput 'build-info.json') -Encoding utf8
    }
    "# 余额查询模板验证未通过`n`n$($_.Exception.Message)`n`n详见 build-info.json 和本次构建、测试日志。已有 EXE 不代表本次验证通过。" | Set-Content (Join-Path $taskOutput 'VERIFICATION.md') -Encoding utf8
    throw
} finally { Pop-Location }

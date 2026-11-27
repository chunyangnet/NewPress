$ErrorActionPreference = 'Stop'
$runtime = Join-Path $PSScriptRoot '../output/runtime-latest-20261117'
& (Join-Path $runtime 'start.ps1')
$serverId = [int](Get-Content -LiteralPath (Join-Path $runtime 'server.pid'))
Wait-Process -Id $serverId

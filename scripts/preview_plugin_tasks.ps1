$env:NEWPRESS_TASK_BROWSER_PREVIEW = '1'
Set-Location (Join-Path $PSScriptRoot '../internal/httpserver')
go test -run '^TestPluginTasksBrowserPreview$' -count=1 -timeout=30m -v

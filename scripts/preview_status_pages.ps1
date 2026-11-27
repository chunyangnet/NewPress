$ErrorActionPreference = 'Stop'
$env:NEWPRESS_STATUS_BROWSER_ADDR = '127.0.0.1:19427'
go test ./internal/httpserver -run '^TestStatusPagesBrowserFixture$' -count=1 -v -timeout 35m

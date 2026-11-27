$ErrorActionPreference = 'Stop'
$env:NEWPRESS_CHILD_BROWSER_ADDR = '127.0.0.1:19428'
go test ./internal/httpserver -run '^TestChildThemeBrowserFixture$' -count=1 -v -timeout 35m

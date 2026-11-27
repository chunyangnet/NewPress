$env:NEWPRESS_CDN_UI = '1'
go test ./internal/httpserver -run '^TestCDNInteractiveUI$' -count=1 -timeout=30m

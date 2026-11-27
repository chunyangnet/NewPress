module newpress.local/newpress

go 1.26.0

toolchain go1.26.11

require (
	github.com/HugoSmits86/nativewebp v1.3.0
	github.com/aliyun/alibabacloud-oss-go-sdk-v2 v1.6.1
	github.com/aws/aws-sdk-go-v2 v1.48.0
	github.com/aws/aws-sdk-go-v2/service/s3 v1.114.10
	github.com/aws/smithy-go v1.28.9
	github.com/jackc/pgx/v5 v5.10.0
	github.com/microcosm-cc/bluemonday v1.0.27
	github.com/skip2/go-qrcode v0.0.0-20200617195104-da1b6568686e
	github.com/tetratelabs/wazero v1.12.0
	github.com/yuin/goldmark v1.8.7
	go.yaml.in/yaml/v3 v3.0.6
	golang.org/x/image v0.47.0
)

require (
	github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream v1.7.27 // indirect
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.12 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.12 // indirect
	github.com/aws/aws-sdk-go-v2/internal/v4a v1.5.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/accept-encoding v1.13.25 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/checksum v1.11.13 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/presigned-url v1.14.12 // indirect
	github.com/aws/aws-sdk-go-v2/service/internal/s3shared v1.20.12 // indirect
	golang.org/x/time v0.4.0 // indirect
)

require (
	github.com/aymerick/douceur v0.2.0 // indirect
	github.com/gorilla/css v1.0.1 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/crypto v0.58.0
	golang.org/x/net v0.62.0
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0
	golang.org/x/text v0.43.0
)

replace golang.org/x/crypto => ./third_party/xcrypto

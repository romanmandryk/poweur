module github.com/poweur/integration

go 1.25.0

require (
	github.com/coreos/go-oidc/v3 v3.17.0
	github.com/poweur/api v0.0.0
	github.com/poweur/cli v0.0.0
	github.com/poweur/guestbook v0.0.0
	go.opentelemetry.io/proto/otlp v1.7.1
	golang.org/x/oauth2 v0.32.0
	google.golang.org/protobuf v1.36.8
)

require (
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/go-jose/go-jose/v4 v4.1.3 // indirect
	github.com/go-logr/logr v1.4.3 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.27.2 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/tyler-smith/go-bip39 v1.1.0 // indirect
	go.opentelemetry.io/auto/sdk v1.1.0 // indirect
	go.opentelemetry.io/otel v1.38.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp v0.14.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.38.0 // indirect
	go.opentelemetry.io/otel/log v0.14.0 // indirect
	go.opentelemetry.io/otel/metric v1.38.0 // indirect
	go.opentelemetry.io/otel/sdk v1.38.0 // indirect
	go.opentelemetry.io/otel/sdk/log v0.14.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.38.0 // indirect
	go.opentelemetry.io/otel/trace v1.38.0 // indirect
	golang.org/x/net v0.56.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20250825161204-c5933d9347a5 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20250825161204-c5933d9347a5 // indirect
	google.golang.org/grpc v1.75.0 // indirect
	modernc.org/libc v1.75.7 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
	modernc.org/sqlite v1.59.0 // indirect
	rsc.io/qr v0.2.0 // indirect
)

require (
	github.com/pelletier/go-toml/v2 v2.3.0
	github.com/poweur/identity v0.0.0
	github.com/poweur/oauth v0.0.0
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace (
	github.com/poweur/api => ../api
	github.com/poweur/cli => ../cli
	github.com/poweur/guestbook => ../guestbook
	github.com/poweur/identity => ../../packages/identity
)

replace github.com/poweur/oauth => ../oauth

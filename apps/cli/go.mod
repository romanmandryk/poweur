module github.com/poweur/cli

go 1.26.0

require (
	github.com/pelletier/go-toml/v2 v2.4.3
	github.com/poweur/identity v0.0.0
	golang.org/x/crypto v0.58.0
	rsc.io/qr v0.2.0
)

require (
	github.com/tyler-smith/go-bip39 v1.1.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
)

replace github.com/poweur/identity => ../../packages/identity

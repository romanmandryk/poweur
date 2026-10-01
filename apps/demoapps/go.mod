module github.com/poweur/demoapps

go 1.25.0

require (
	github.com/poweur/cli v0.0.0
	github.com/poweur/identity v0.0.0
	github.com/yuin/goldmark v1.8.6
)

require (
	github.com/pelletier/go-toml/v2 v2.3.0 // indirect
	github.com/tyler-smith/go-bip39 v1.1.0 // indirect
	golang.org/x/crypto v0.54.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	rsc.io/qr v0.2.0 // indirect
)

replace (
	github.com/poweur/cli => ../cli
	github.com/poweur/identity => ../../packages/identity
)

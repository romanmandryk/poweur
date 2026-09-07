module github.com/poweur/cli

go 1.25.0

require (
	github.com/pelletier/go-toml/v2 v2.3.0
	github.com/poweur/identity v0.0.0
	golang.org/x/crypto v0.54.0
)

require (
	github.com/tyler-smith/go-bip39 v1.1.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace github.com/poweur/identity => ../../packages/identity

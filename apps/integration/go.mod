module github.com/poweur/integration

go 1.25.0

require (
	github.com/poweur/api v0.0.0
	github.com/poweur/cli v0.0.0
)

require github.com/pelletier/go-toml/v2 v2.3.0 // indirect

replace (
	github.com/poweur/api => ../api
	github.com/poweur/cli => ../cli
)

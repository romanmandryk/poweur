module github.com/eurything/integration

go 1.25.0

require (
	github.com/eurything/api v0.0.0
	github.com/eurything/cli v0.0.0
)

require github.com/pelletier/go-toml/v2 v2.3.0 // indirect

replace (
	github.com/eurything/api => ../api
	github.com/eurything/cli => ../cli
)

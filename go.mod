module bpslocal

go 1.27.0

require (
	github.com/pelletier/go-toml/v2 v2.2.4
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
	github.com/stretchr/testify v1.10.0
	github.com/tidwall/gjson v1.18.0
	golang.org/x/image v0.25.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/tidwall/match v1.1.1 // indirect
	github.com/tidwall/pretty v1.2.0 // indirect
	golang.org/x/text v0.23.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/pelletier/go-toml/v2 => ./third_party/go-toml

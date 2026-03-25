package docs

import _ "embed"

var (
	//go:embed static/landing.html
	LandingHTML []byte

	//go:embed static/scalar.html
	ScalarHTML []byte

	//go:embed static/openapi.yaml
	OpenAPI []byte
)

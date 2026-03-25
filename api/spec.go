package api

import _ "embed"

var (
	//go:embed openapi.yaml
	OpenAPI []byte
)

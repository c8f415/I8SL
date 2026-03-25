package docs_test

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	apispec "i8sl/api"
)

func TestEmbeddedOpenAPIIsValid(t *testing.T) {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true

	doc, err := loader.LoadFromData(apispec.OpenAPI)
	if err != nil {
		t.Fatalf("load openapi document: %v", err)
	}

	if err := doc.Validate(loader.Context); err != nil {
		t.Fatalf("validate openapi document: %v", err)
	}
}

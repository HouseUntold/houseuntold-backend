package docs

import _ "embed"

// openapiYAML is the complete checked-in OpenAPI document used at runtime.
//
//go:embed openapi.yaml
var openapiYAML []byte

// swaggerIndexHTML is the static Swagger UI shell served at /swagger/index.html.
//
//go:embed index.html
var swaggerIndexHTML []byte

func OpenAPIYAML() []byte {
	return openapiYAML
}

func SwaggerIndexHTML() []byte {
	return swaggerIndexHTML
}

func SwaggerJSON() []byte {
	return []byte(SwaggerInfo.ReadDoc())
}

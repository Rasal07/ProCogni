// Package docs embeds the API contract so the service can serve the exact
// document it was built with.
package docs

import _ "embed"

//go:embed openapi.yaml
var OpenAPI []byte

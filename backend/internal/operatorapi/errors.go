package operatorapi

import "github.com/gin-gonic/gin"

// errorEnvelope mirrors 05-api-contracts.md's one JSON error envelope —
// the identical shape agentapi.errorEnvelope already uses for the
// agent-facing surface, duplicated package-local rather than shared across
// an import, the same way this repo's other internal packages don't reach
// into each other for small, stable leaf types.
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Field   *string `json:"field"`
}

func writeError(c *gin.Context, status int, code, message string) {
	c.JSON(status, errorEnvelope{Error: errorBody{Code: code, Message: message, Field: nil}})
}

func writeFieldError(c *gin.Context, status int, code, message, field string) {
	c.JSON(status, errorEnvelope{Error: errorBody{Code: code, Message: message, Field: &field}})
}

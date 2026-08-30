package agentapi

import "github.com/gin-gonic/gin"

// errorEnvelope mirrors 05-api-contracts.md's one JSON error envelope:
// { "error": { "code", "message", "field" } } — the same shape every other
// endpoint in this system uses, not a bespoke agent-surface-only format.
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

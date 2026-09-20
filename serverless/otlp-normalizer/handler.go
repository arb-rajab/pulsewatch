package main

import (
	"encoding/json"
	"net/http"

	"github.com/aws/aws-lambda-go/events"
)

// handleRequest is the API Gateway (HTTP API, payload format 2.0) entry
// point: POST a normalizeRequest JSON body, get a normalizeResponse back.
// It never returns a Lambda-level error for a bad request body — same
// convention as IngestLogs in backend/internal/agentapi/logs.go — a
// malformed body is a 422 response, not an invocation failure.
func handleRequest(request events.APIGatewayV2HTTPRequest) (events.APIGatewayV2HTTPResponse, error) {
	var req normalizeRequest
	if err := json.Unmarshal([]byte(request.Body), &req); err != nil {
		return jsonResponse(http.StatusUnprocessableEntity, map[string]string{
			"error": "request body is not valid JSON for {\"logRecords\": [...]}",
		})
	}

	return jsonResponse(http.StatusOK, normalize(req))
}

func jsonResponse(status int, body any) (events.APIGatewayV2HTTPResponse, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return events.APIGatewayV2HTTPResponse{StatusCode: http.StatusInternalServerError}, err
	}
	return events.APIGatewayV2HTTPResponse{
		StatusCode: status,
		Headers:    map[string]string{"Content-Type": "application/json"},
		Body:       string(b),
	}, nil
}

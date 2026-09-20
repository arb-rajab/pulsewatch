package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/aws/aws-lambda-go/events"
)

// TestHandleRequest_ValidBatch invokes handleRequest exactly as the Lambda
// runtime would (an events.APIGatewayV2HTTPRequest in, an
// events.APIGatewayV2HTTPResponse out) — the same call path `sam local
// invoke` and a real deployed HTTP API both exercise, just without the
// containerized runtime shell around it.
func TestHandleRequest_ValidBatch(t *testing.T) {
	body := `{"logRecords":[
		{"timeUnixNano":"1735689600000000000","attributes":[
			{"key":"pulsewatch.event_type","value":{"stringValue":"heartbeat"}}
		]},
		{"timeUnixNano":"1735689601000000000","attributes":[
			{"key":"pulsewatch.event_type","value":{"stringValue":"check_result"}},
			{"key":"pulsewatch.target_id","value":{"stringValue":"11111111-1111-1111-1111-111111111111"}},
			{"key":"pulsewatch.outcome","value":{"stringValue":"success"}},
			{"key":"pulsewatch.latency_ms","value":{"intValue":"87"}},
			{"key":"pulsewatch.status_code","value":{"intValue":"200"}}
		]}
	]}`

	resp, err := handleRequest(events.APIGatewayV2HTTPRequest{Body: body})
	if err != nil {
		t.Fatalf("handleRequest returned an error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, resp.Body)
	}

	var got normalizeResponse
	if err := json.Unmarshal([]byte(resp.Body), &got); err != nil {
		t.Fatalf("decode response body: %v (body=%s)", err, resp.Body)
	}
	if got.Accepted != 2 || got.Rejected != 0 {
		t.Fatalf("expected 2 accepted / 0 rejected, got accepted=%d rejected=%d", got.Accepted, got.Rejected)
	}
}

func TestHandleRequest_MalformedBody(t *testing.T) {
	resp, err := handleRequest(events.APIGatewayV2HTTPRequest{Body: "not json"})
	if err != nil {
		t.Fatalf("handleRequest returned an error: %v", err)
	}
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for a malformed body, got %d: %s", resp.StatusCode, resp.Body)
	}
}

func TestHandleRequest_RejectedRecordSurfacesInResponse(t *testing.T) {
	body := `{"logRecords":[{"timeUnixNano":"not-a-number"}]}`

	resp, err := handleRequest(events.APIGatewayV2HTTPRequest{Body: body})
	if err != nil {
		t.Fatalf("handleRequest returned an error: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (this function reports rejects in-band, like the real ingestion endpoint's partial success), got %d", resp.StatusCode)
	}

	var got normalizeResponse
	if err := json.Unmarshal([]byte(resp.Body), &got); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if got.Rejected != 1 || got.Accepted != 0 {
		t.Fatalf("expected 1 rejected / 0 accepted, got accepted=%d rejected=%d", got.Accepted, got.Rejected)
	}
	if got.Records[0].Error == "" {
		t.Fatalf("expected the rejected record to carry an error message")
	}
}

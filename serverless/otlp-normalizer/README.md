# otlp-normalizer

A standalone AWS Lambda function that validates and normalizes pulsewatch's
simplified OTLP/HTTP+JSON `check_result`/`heartbeat` payload shape — the
same wire format the always-on backend accepts at `POST /v1/logs`
(`backend/internal/agentapi`, `docs/architecture/openapi.yaml`) — with no
database and no alert dispatcher. See
[`docs/adr/ADR-0009-serverless-otlp-normalizer.md`](../../docs/adr/ADR-0009-serverless-otlp-normalizer.md)
for why this exists, how it relates to the real ingestion endpoint, and what
was and wasn't verified locally.

This is a separate Go module (`go.mod` here, distinct from `backend/go.mod`)
and a separate deployment target — it does not replace anything in the
existing Kubernetes-deployed backend.

## Request / response shape

`POST /normalize`:

```json
{
  "logRecords": [
    {
      "timeUnixNano": "1735689601000000000",
      "attributes": [
        {"key": "pulsewatch.event_type", "value": {"stringValue": "check_result"}},
        {"key": "pulsewatch.target_id", "value": {"stringValue": "11111111-1111-1111-1111-111111111111"}},
        {"key": "pulsewatch.outcome", "value": {"stringValue": "failure"}},
        {"key": "pulsewatch.latency_ms", "value": {"intValue": "342"}},
        {"key": "pulsewatch.status_code", "value": {"intValue": "503"}}
      ]
    }
  ]
}
```

returns `200` with:

```json
{
  "accepted": 1,
  "rejected": 0,
  "records": [
    {
      "index": 0,
      "eventType": "check_result",
      "checkResult": {
        "checkedAt": "2025-12-31T23:00:01Z",
        "targetId": "11111111-1111-1111-1111-111111111111",
        "success": false,
        "latencyMs": 342,
        "statusCode": 503
      }
    }
  ]
}
```

A record that fails validation is reported inline (`"error": "..."`) rather
than failing the whole request — the same partial-success convention
`POST /v1/logs` uses. Only a malformed request body (not valid JSON) returns
a non-200 status (`422`).

## Local development

```sh
go build ./...
go vet ./...
go test ./... -v
```

No network access, database, or AWS credentials are needed for any of the
above — every test calls the handler function directly with constructed
`events.APIGatewayV2HTTPRequest` values, the same shape API Gateway would
send.

## Local AWS SAM verification

Install the SAM CLI once (`pip install aws-sam-cli`), then from this
directory:

```sh
sam build            # compiles the real deployable provided.al2023 artifact
sam validate --lint  # checks template.yaml
sam local invoke NormalizerFunction -e events/valid-batch.json
sam local invoke NormalizerFunction -e events/invalid-latency.json
sam local start-api  # POST http://127.0.0.1:3000/normalize
```

`sam build` and `sam validate` need no AWS credentials and no network access
beyond the Go module proxy. `sam local invoke`/`sam local start-api` also
need none — they run the built binary inside a local container that
emulates the real Lambda runtime — but they do need Docker able to pull
`public.ecr.aws/sam/emulation-provided.al2023` once. In a sandboxed CI/dev
environment whose egress policy blocks that pull (this repo's own
CI-adjacent sandbox does; see ADR-0009's Decision §3), the `go test`
invocations above exercise the identical `handleRequest` entry point
`lambda.Start` wires up and are the verification of record in this repo's
CI job (`.github/workflows/ci.yml`'s `serverless-otlp-normalizer` job runs
`go test` + `sam build` + `sam validate`, not `sam local invoke`).

## Deploying for real (not exercised by this repo)

```sh
sam deploy --guided
```

This requires a real AWS account and is out of scope for this repo's CI and
this session's verification, same as ADR-0005's Terraform/Kubernetes target
is not applied by CI against a live cluster.

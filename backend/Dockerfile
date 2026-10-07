FROM golang:1.27-alpine AS build

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o pulsewatch .

FROM alpine:3.24

RUN adduser -D -u 10001 appuser
COPY --from=build /app/pulsewatch /usr/local/bin/pulsewatch

USER appuser
EXPOSE 8080

CMD ["pulsewatch"]

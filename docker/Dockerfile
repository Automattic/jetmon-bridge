FROM golang:1.26-alpine3.23 AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X main.version=${VERSION}" -o jetmon-bridge ./cmd/jetmon-bridge

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /build/jetmon-bridge .
ENTRYPOINT ["/app/jetmon-bridge"]

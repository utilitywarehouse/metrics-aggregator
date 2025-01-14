# Build the manager binary
FROM golang:1.23-alpine AS builder


WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum

# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY . .

RUN go test -v -cover ./... && \
    CGO_ENABLED=0 go build -a -o metrics-aggregator

FROM alpine:3.20

ENV USER_ID=65532

RUN adduser -S -H -u $USER_ID app-user \
      && apk --no-cache add ca-certificates

WORKDIR /

COPY --from=builder /workspace/metrics-aggregator .

ENV USER=app-user

USER $USER_ID

ENTRYPOINT ["/metrics-aggregator"]
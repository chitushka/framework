FROM golang:1.26.10@sha256:670bb5aa5349ae03b8516764718486f4f16399134bdc9acd0c46a23f043ab05c AS test
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go test ./...


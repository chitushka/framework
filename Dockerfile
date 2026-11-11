FROM golang:1.26.11@sha256:7af425da11f4fc1a9308eb704369e3f6d1c44e35bac0f874a59ade3f20fdc07e AS test
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go test ./...


FROM golang:1.26.11@sha256:7af425da11f4fc1a9308eb704369e3f6d1c44e35bac0f874a59ade3f20fdc07e
WORKDIR /workspace
COPY go.mod go.sum ./
COPY integration/go.mod integration/go.sum ./integration/
COPY adapters/go.mod adapters/go.sum ./adapters/
WORKDIR /workspace/integration
RUN go mod download
WORKDIR /workspace
COPY . .
WORKDIR /workspace/integration
CMD ["go", "test", "-mod=readonly", "-race", "-count=1", "-timeout=6m", "-v", "./..."]

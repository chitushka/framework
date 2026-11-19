FROM golang:1.27.3@sha256:e519d3b0a5b285e5aa53ab9eef02bc6bbb9cb04608389b743a136a9ce75f0d1e
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

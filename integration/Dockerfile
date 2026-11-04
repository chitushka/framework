FROM golang:1.26@sha256:670bb5aa5349ae03b8516764718486f4f16399134bdc9acd0c46a23f043ab05c
WORKDIR /workspace
COPY go.mod ./
COPY integration/go.mod integration/go.sum ./integration/
WORKDIR /workspace/integration
RUN go mod download
WORKDIR /workspace
COPY . .
WORKDIR /workspace/integration
CMD ["go", "test", "-mod=readonly", "-race", "-count=1", "-timeout=3m", "-v", "./..."]

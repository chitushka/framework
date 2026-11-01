FROM golang:1.26 AS test
WORKDIR /workspace
COPY go.mod ./
COPY . .
RUN go test ./...


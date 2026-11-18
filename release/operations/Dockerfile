FROM golang:1.26.11@sha256:7af425da11f4fc1a9308eb704369e3f6d1c44e35bac0f874a59ade3f20fdc07e AS build
ENV CGO_ENABLED=0 GOTOOLCHAIN=local GOMAXPROCS=2 GOFLAGS="-p=2 -mod=readonly"
WORKDIR /src/integration
COPY candidate/ /src/
COPY app/ /src/integration/operations/
RUN go build -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o /operations ./operations
FROM scratch
COPY --from=build /operations /operations
USER 65532:65532
ENTRYPOINT ["/operations"]

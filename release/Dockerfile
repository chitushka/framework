FROM mcr.microsoft.com/playwright:v1.64.1-noble@sha256:47b510dd9230bf0b9bfc9b20102241e577ea7a99334ab4c3ca19f19770e9b8ea AS assets
WORKDIR /workspace
ARG SOURCE_DATE_EPOCH=0
ENV SOURCE_DATE_EPOCH=$SOURCE_DATE_EPOCH
COPY frontend/package.json frontend/package-lock.json ./frontend/
RUN cd frontend && npm ci --no-audit --no-fund
COPY frontend/ ./frontend/
COPY release/ ./release/
RUN cd frontend && npm run build
RUN node release/licenses.mjs frontend /out/licenses/npm \
 && node release/assets.mjs public /out/basic/public src/basic.js \
 && node release/assets.mjs public /out/admin/public src/admin.js

FROM golang:1.26.11@sha256:7af425da11f4fc1a9308eb704369e3f6d1c44e35bac0f874a59ade3f20fdc07e AS backend
WORKDIR /workspace
ENV GOMAXPROCS=2 GOFLAGS=-p=2 CGO_ENABLED=0 GOTOOLCHAIN=local
COPY go.mod go.sum ./
COPY examples/advanced-admin/go.mod examples/advanced-admin/go.sum ./examples/advanced-admin/
RUN go mod download all && cd examples/advanced-admin && go mod download all
COPY . .
COPY --from=assets /usr/bin/node /usr/local/bin/node
RUN go build -mod=readonly -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o /out/basic ./examples/basic \
 && go build -mod=readonly -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o /out/rest ./examples/rest-api \
 && cd examples/advanced-admin && go build -mod=readonly -trimpath -buildvcs=false -ldflags='-s -w -buildid=' -o /out/admin .
RUN node release/licenses.mjs go /out/licenses/go \
 && cp /usr/share/doc/ca-certificates/copyright /out/licenses/CA-certificates-copyright

FROM scratch AS common
ARG VERSION
ARG REVISION
LABEL org.opencontainers.image.version=$VERSION org.opencontainers.image.revision=$REVISION org.opencontainers.image.licenses=MIT
WORKDIR /app
COPY LICENSE NOTICE /app/
COPY --from=backend /out/licenses /app/licenses
COPY --from=backend /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
USER 65532:65532
ENV APP_ENV=production

FROM common AS basic
COPY --from=backend /out/basic /app/basic
COPY --from=assets /out/basic/public /app/public
COPY --from=assets /out/licenses /app/licenses
ENV BASIC_ADDRESS=0.0.0.0:8080
EXPOSE 8080
ENTRYPOINT ["/app/basic"]

FROM common AS admin
COPY --from=backend /out/admin /app/admin
COPY --from=assets /out/admin/public /app/public
COPY --from=assets /out/licenses /app/licenses
ENV ADMIN_ADDRESS=0.0.0.0:8081
EXPOSE 8081
ENTRYPOINT ["/app/admin"]

FROM common AS rest
COPY --from=backend /out/rest /app/rest
ENV REST_ADDRESS=0.0.0.0:8082
EXPOSE 8082
ENTRYPOINT ["/app/rest"]

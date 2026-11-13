FROM golang:1.26.11@sha256:7af425da11f4fc1a9308eb704369e3f6d1c44e35bac0f874a59ade3f20fdc07e AS backend
WORKDIR /workspace
ENV GOMAXPROCS=2 GOFLAGS=-p=2
COPY go.mod go.sum ./
COPY examples/advanced-admin/go.mod examples/advanced-admin/go.sum ./examples/advanced-admin/
WORKDIR /workspace/examples/advanced-admin
RUN go mod download
WORKDIR /workspace
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build cd examples/advanced-admin && CGO_ENABLED=0 go build -trimpath -o /out/admin .

FROM backend AS integration
WORKDIR /workspace/examples/advanced-admin
CMD ["go", "test", "-mod=readonly", "-tags=integration", "-race", "-count=1", "-timeout=4m", "-v", "./..."]

FROM mcr.microsoft.com/playwright:v1.64.1-noble AS assets
WORKDIR /workspace
COPY frontend/package.json frontend/package-lock.json ./frontend/
RUN cd frontend && npm ci --no-audit --no-fund
RUN cd frontend && npm audit --audit-level=high
COPY . .
RUN cd frontend && npm run build && npm test

FROM assets AS browser
COPY --from=backend /out/admin /workspace/.cache/admin/admin
ENV ADMIN_BINARY=/workspace/.cache/admin/admin GOMAXPROCS=2 CI=1
CMD ["node", "scripts/admin.mjs", "test-built"]

FROM scratch AS runtime
WORKDIR /app
COPY --from=backend /out/admin /app/admin
COPY --from=backend /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=assets /workspace/public /app/public
USER 65532:65532
ENV APP_ENV=production ADMIN_ADDRESS=0.0.0.0:8081
EXPOSE 8081
ENTRYPOINT ["/app/admin"]

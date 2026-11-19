FROM golang:1.27.3@sha256:e519d3b0a5b285e5aa53ab9eef02bc6bbb9cb04608389b743a136a9ce75f0d1e AS backend
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

FROM mcr.microsoft.com/playwright:v1.64.1-noble@sha256:47b510dd9230bf0b9bfc9b20102241e577ea7a99334ab4c3ca19f19770e9b8ea AS assets
WORKDIR /workspace
COPY frontend/package.json frontend/package-lock.json ./frontend/
RUN cd frontend && npm ci --no-audit --no-fund
COPY . .
RUN cd frontend && npm run lint && npm run build && npm test
RUN node scripts/npm-audit.mjs
RUN node release/licenses.mjs frontend /out/licenses/npm

FROM assets AS browser
COPY --from=backend /out/admin /workspace/.cache/admin/admin
ENV ADMIN_BINARY=/workspace/.cache/admin/admin GOMAXPROCS=2 CI=1
CMD ["node", "scripts/admin.mjs", "test-built"]

FROM backend AS notices
COPY --from=assets /usr/bin/node /usr/local/bin/node
RUN node release/licenses.mjs go /out/licenses/go \
 && cp /usr/share/doc/ca-certificates/copyright /out/licenses/CA-certificates-copyright

FROM scratch AS runtime
WORKDIR /app
COPY --from=backend /out/admin /app/admin
COPY --from=backend /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=assets /workspace/public /app/public
COPY LICENSE NOTICE /app/
COPY --from=notices /out/licenses /app/licenses
COPY --from=assets /out/licenses /app/licenses
USER 65532:65532
ENV APP_ENV=production ADMIN_ADDRESS=0.0.0.0:8081
EXPOSE 8081
ENTRYPOINT ["/app/admin"]

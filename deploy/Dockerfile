FROM golang:1.26.8-alpine AS backend-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=0 go build -trimpath -o /out/ ./cmd/...

FROM alpine:3.23 AS backend
RUN apk add --no-cache ca-certificates
COPY --from=backend-build /out/ /usr/local/bin/
USER 65532:65532
ENTRYPOINT ["api-server"]

FROM node:24-alpine AS node-deps
WORKDIR /src
COPY package.json package-lock.json ./
COPY apps/console/package.json apps/console/package.json
COPY apps/agent-runner/package.json apps/agent-runner/package.json
RUN npm ci --ignore-scripts

FROM node-deps AS console-build
COPY apps/console apps/console
RUN npm run build --workspace @mcp-gateway/console

FROM nginxinc/nginx-unprivileged:1.29-alpine AS console
COPY deploy/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=console-build /src/apps/console/dist /usr/share/nginx/html

FROM node-deps AS pi-runner
COPY apps/agent-runner apps/agent-runner
RUN mkdir -p /var/lib/pi/config /var/lib/pi/state && chown -R 65532:65532 /var/lib/pi
ENV PI_CODING_AGENT_DIR=/var/lib/pi/config GATEWAY_STATE_DIR=/var/lib/pi/state
USER 65532:65532
ENTRYPOINT ["node", "--import", "tsx", "apps/agent-runner/src/worker-cli.ts"]

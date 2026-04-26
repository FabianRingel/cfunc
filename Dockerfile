# SPDX-License-Identifier: Apache-2.0
#
# Multi-stage Dockerfile for cfunc. Produces a single image carrying
# all three binaries (cfunc, gateway, builder) — the entrypoint is
# selected via `command:` in compose / `args:` in Helm. Keeping one
# image avoids duplicating the dashboard bundle and Go module cache
# across three near-identical builds.

# --- stage 1: build the React dashboard bundle ---------------------
FROM node:22-alpine AS dashboard
WORKDIR /src
COPY internal/dashboard/web/package.json internal/dashboard/web/package-lock.json ./
RUN npm ci --silent
COPY internal/dashboard/web/ ./
RUN npm run build

# --- stage 2: build Go binaries ------------------------------------
FROM golang:1.25-alpine AS gobuild
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Replace the (possibly empty) dist directory with the freshly built one
# so the //go:embed in internal/dashboard picks it up.
COPY --from=dashboard /src/dist ./internal/dashboard/web/dist
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/cfunc       ./cmd/cfunc \
   && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/cfunc-gateway ./cmd/gateway \
   && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" \
      -o /out/cfunc-builder ./cmd/builder

# --- stage 3: runtime image ----------------------------------------
# alpine (not distroless) because the builder needs python3 + pip at
# runtime to materialise pip-layers. Gateway/CLI work in distroless
# but a uniform image keeps the compose/Helm story simple.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tini python3 py3-pip \
 && addgroup -S cfunc && adduser -S -G cfunc -h /var/lib/cfunc cfunc \
 && mkdir -p /var/lib/cfunc/layers /var/lib/cfunc/state /etc/cfunc \
 && chown -R cfunc:cfunc /var/lib/cfunc /etc/cfunc
COPY --from=gobuild /out/cfunc          /usr/local/bin/cfunc
COPY --from=gobuild /out/cfunc-gateway  /usr/local/bin/cfunc-gateway
COPY --from=gobuild /out/cfunc-builder  /usr/local/bin/cfunc-builder
USER cfunc
WORKDIR /var/lib/cfunc
ENTRYPOINT ["/sbin/tini", "--"]
CMD ["cfunc-gateway", "-addr=:8080", "-admin-addr=0.0.0.0:8081"]
EXPOSE 8080 8081 9090

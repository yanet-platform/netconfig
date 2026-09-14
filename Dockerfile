# syntax=docker/dockerfile:1
# Copyright 2026 YANDEX LLC
# SPDX-License-Identifier: Apache-2.0

FROM golang:1.26.2 AS test
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -o /tmp/netconfig ./cmd/netconfig
ENV NETCONFIG_BINARY=/tmp/netconfig
CMD ["go", "test", "-race", "-count=1", "./..."]

FROM --platform=$BUILDPLATFORM golang:1.26.2-alpine3.23 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
    go build -trimpath -ldflags='-s -w' -o /out/netconfig ./cmd/netconfig

FROM alpine:3.23 AS runtime
LABEL org.opencontainers.image.title="netconfig" \
      org.opencontainers.image.description="Startup interface configuration for YANET" \
      org.opencontainers.image.source="https://github.com/yanet-platform/netconfig" \
      org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /out/netconfig /usr/bin/netconfig
COPY LICENSE NOTICE /usr/share/licenses/netconfig/
ENTRYPOINT ["/usr/bin/netconfig"]
CMD ["-config", "/etc/netconfig/config.yaml"]

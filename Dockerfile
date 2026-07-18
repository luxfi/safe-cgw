# Multi-arch build: cross-compile a static binary on the native BUILDPLATFORM
# (no QEMU emulation of the compiler), then ship it on a minimal Alpine base
# that carries CA certificates for the outbound HTTPS JSON-RPC calls.
FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /safe-cgw .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 app
USER app
ENV PORT=3000
EXPOSE 3000
COPY --from=build /safe-cgw /usr/local/bin/safe-cgw
ENTRYPOINT ["/usr/local/bin/safe-cgw"]

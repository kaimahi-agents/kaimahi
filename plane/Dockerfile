# kaimahi-proxy: static binary on distroless (CA certs included for the
# keyed Copilot upstream; nonroot by default).
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# The git revision for kaimahi_build_info: the build context has no
# .git, so the Makefile passes it in. Defaults to "unknown", never fails.
ARG VERSION=unknown
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-X github.com/kaimahi-agents/kaimahi/plane/internal/metrics.buildVersion=${VERSION}" \
    -o /kaimahi-proxy ./cmd/kaimahi-proxy

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /kaimahi-proxy /kaimahi-proxy
ENTRYPOINT ["/kaimahi-proxy"]

# kaimahi-erp: the demo's fixture ERP — a static binary on distroless,
# nonroot. No CA certificates are copied in: this server dials nothing,
# and a base image that cannot verify a certificate is one more reason it
# never will. The corpus arrives as a mounted ConfigMap, never baked in.
#
# Build context is the repository root (the module root):
#   docker build -f cmd/kaimahi-erp/Dockerfile -t kaimahi-erp:dev .
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /kaimahi-erp ./cmd/kaimahi-erp

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /kaimahi-erp /kaimahi-erp
ENTRYPOINT ["/kaimahi-erp"]

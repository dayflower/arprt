FROM --platform=$BUILDPLATFORM golang:1.26.8-bookworm AS builder
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG REVISION=unknown
ARG GO_BUILD_TAGS=
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api/ api/
COPY internal/ internal/
COPY cmd/ cmd/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -tags="$GO_BUILD_TAGS" \
    -ldflags="-s -w -X main.version=$VERSION -X main.revision=$REVISION" -o /arprt ./cmd/controller

FROM scratch
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/dayflower/arprt" \
      org.opencontainers.image.version="$VERSION" \
      org.opencontainers.image.revision="$REVISION" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.description="ARPRT is an independent, unofficial Kubernetes controller for TRMNL BYOS. It is not affiliated with or endorsed by TRMNL."
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /arprt /arprt
USER 1000:1000
ENTRYPOINT ["/arprt"]

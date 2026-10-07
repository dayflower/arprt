FROM golang:1.26.8-bookworm AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api/ api/
COPY internal/ internal/
COPY cmd/ cmd/
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /arprt ./cmd/controller

FROM scratch
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /arprt /arprt
USER 1000:1000
ENTRYPOINT ["/arprt"]

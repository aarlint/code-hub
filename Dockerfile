FROM golang:1.23-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOARCH=arm64 go build -ldflags="-s -w" -o code-hub .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates curl && \
    curl -LO "https://dl.k8s.io/release/v1.31.4/bin/linux/arm64/kubectl" && \
    install kubectl /usr/local/bin/kubectl && rm kubectl && \
    curl -L -o /usr/local/bin/vcluster \
    "https://github.com/loft-sh/vcluster/releases/latest/download/vcluster-linux-arm64" && \
    chmod +x /usr/local/bin/vcluster
WORKDIR /app
COPY --from=builder /build/code-hub .
COPY index.html /app/
COPY js/ /app/js/
EXPOSE 8080
CMD ["./code-hub"]

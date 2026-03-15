FROM golang:1.23-alpine AS builder
WORKDIR /build
COPY go.mod go.sum main.go ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o code-hub .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && \
    wget -q -O /usr/local/bin/k3d \
    "https://github.com/k3d-io/k3d/releases/download/v5.7.2/k3d-linux-amd64" && \
    chmod +x /usr/local/bin/k3d && \
    wget -q -O /usr/local/bin/kubectl \
    "https://dl.k8s.io/release/v1.31.4/bin/linux/amd64/kubectl" && \
    chmod +x /usr/local/bin/kubectl
WORKDIR /app
COPY --from=builder /build/code-hub .
COPY index.html /app/
COPY js/ /app/js/
EXPOSE 8080
CMD cp /usr/local/bin/kubectl /app/kube-tools/kubectl && exec ./code-hub

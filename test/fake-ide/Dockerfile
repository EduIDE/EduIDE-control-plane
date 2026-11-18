# Build context is this directory: docker build -t ghcr.io/eduide/eduide-control-plane/fake-ide:e2e test/fake-ide
FROM golang:1.27.4 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /fake-ide .

# busybox instead of distroless: the pull init container of the prepull
# DaemonSet runs "sh -c true", and the network policy test uses wget.
FROM busybox:1.37-musl
COPY --from=build /fake-ide /fake-ide
USER 65532:65532
ENTRYPOINT ["/fake-ide"]

# Build context is this directory: docker build -t ghcr.io/eduide/eduide-control-plane/fake-ide:e2e test/fake-ide
FROM golang:1.27.4 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /fake-ide .

# alpine instead of distroless: the pull init container of the prepull DaemonSet runs "sh -c true", the
# network policy tests use nc and wget, and the Git test endpoint runs git. /work is the home of the
# unprivileged user: git writes its global config there.
FROM alpine:3.22
RUN apk add --no-cache git && mkdir /work && chown 65532:65532 /work
COPY --from=build /fake-ide /fake-ide
USER 65532:65532
ENV HOME=/work
ENTRYPOINT ["/fake-ide"]

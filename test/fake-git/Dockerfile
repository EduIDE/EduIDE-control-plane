# Build context is this directory: docker build -t ghcr.io/eduide/eduide-control-plane/fake-git:e2e test/fake-git
FROM golang:1.27.4 AS build
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /fake-git .

# git-daemon provides "git http-backend", the smart HTTP server the fake serves its repositories with.
FROM alpine:3.22
RUN apk add --no-cache git git-daemon && mkdir /data && chown 65532:65532 /data
COPY --from=build /fake-git /fake-git
USER 65532:65532
ENV HOME=/tmp
ENTRYPOINT ["/fake-git"]

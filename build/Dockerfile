# One Dockerfile for the Go binaries (operator, service, authz, agent, git-proxy): docker build --build-arg CMD=operator -f build/Dockerfile .
FROM --platform=$BUILDPLATFORM golang:1.27.4 AS build
ARG TARGETOS TARGETARCH CMD VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api/ api/
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X github.com/EduIDE/EduIDE-control-plane/internal/logx.BuildVersion=${VERSION}" -o /app ./cmd/${CMD}

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /app /app
USER 65532:65532
ENTRYPOINT ["/app"]

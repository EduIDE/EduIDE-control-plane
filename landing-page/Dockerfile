# Build context: landing-page/
FROM node:26-alpine AS web
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY tsconfig.json tsconfig.node.json vite.config.ts index.html ./
COPY src ./src
COPY public ./public
RUN npm run build

FROM golang:1.27.4-alpine AS server
WORKDIR /src
COPY server/go.mod ./
COPY server/*.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /server .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=server /server /server
COPY --from=web /app/dist /app/dist
EXPOSE 8080
ENTRYPOINT ["/server"]

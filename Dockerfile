# Two images from one build:
#   docker build -t kryptikker/postitd .                  → sync service (default, last stage)
#   docker build --target web -t kryptikker/postit-web .  → web app
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/postitd ./cmd/postitd \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/postit-web ./cmd/postit-web \
 && mkdir -p /out/data

# postit-web – browser UI, syncs with postitd like any other device
FROM gcr.io/distroless/static-debian12:nonroot AS web
COPY --from=build /out/postit-web /usr/local/bin/postit-web
COPY --from=build --chown=65532:65532 /out/data /data
ENV POSTIT_WEB_DB=/data/web.db POSTIT_ADDR=:8080 POSTIT_SERVER=http://postitd:8080
VOLUME /data
EXPOSE 8080
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s CMD ["/usr/local/bin/postit-web", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/postit-web"]

# postitd – sync service for p
FROM gcr.io/distroless/static-debian12:nonroot AS postitd
COPY --from=build /out/postitd /usr/local/bin/postitd
COPY --from=build --chown=65532:65532 /out/data /data
ENV POSTIT_DB=/data/postit.db POSTIT_ADDR=:8080
VOLUME /data
EXPOSE 8080
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s CMD ["/usr/local/bin/postitd", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/postitd"]

# postitd – sync service for p
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/postitd ./cmd/postitd \
 && mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/postitd /usr/local/bin/postitd
COPY --from=build --chown=65532:65532 /out/data /data
ENV POSTIT_DB=/data/postit.db POSTIT_ADDR=:8080
VOLUME /data
EXPOSE 8080
USER nonroot:nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s CMD ["/usr/local/bin/postitd", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/postitd"]

# Build a static, dependency-free podkop-server binary.
# Build context must be the server/ module directory.
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY . .
# CGO disabled → static binary, trivially cross-compilable (e.g. GOARCH=arm64).
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /podkop-server ./cmd/podkop-server

FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 podkop \
    && mkdir -p /var/lib/podkop-server && chown podkop /var/lib/podkop-server
COPY --from=build /podkop-server /usr/local/bin/podkop-server
USER podkop
EXPOSE 8080
VOLUME ["/var/lib/podkop-server"]
ENTRYPOINT ["/usr/local/bin/podkop-server"]

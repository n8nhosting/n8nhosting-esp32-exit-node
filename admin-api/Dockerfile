# build
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /admin-api .
# run
FROM alpine:3.20
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 app && mkdir -p /data && chown app /data
USER app
COPY --from=build /admin-api /admin-api
VOLUME ["/data"]
EXPOSE 8088
ENTRYPOINT ["/admin-api"]

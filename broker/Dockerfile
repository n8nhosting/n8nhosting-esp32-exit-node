# build
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /broker .
# run
FROM alpine:3.20
# Fresh broker_data volumes inherit this directory's non-root ownership.
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 app && mkdir -p /data && chown app /data
USER app
COPY --from=build /broker /broker
EXPOSE 7000 1080 7001
ENTRYPOINT ["/broker"]

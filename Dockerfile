FROM golang:1.26-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go build -v -o XrayR -trimpath -ldflags "-s -w -buildid="

FROM alpine:latest
RUN apk --update --no-cache add tzdata ca-certificates \
    && cp /usr/share/zoneinfo/Asia/Shanghai /etc/localtime \
    && mkdir /etc/XrayR/

COPY release/config/geoip.dat /etc/XrayR/geoip.dat
COPY release/config/geosite.dat /etc/XrayR/geosite.dat
COPY --from=builder /app/XrayR /usr/local/bin

ENTRYPOINT [ "XrayR", "--config", "/etc/XrayR/config.yml"]

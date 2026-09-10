FROM golang:1.26.0-alpine3.23@sha256:d4c4845f5d60c6a974c6000ce58ae079328d03ab7f721a0734277e69905473e5 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go build -v -tags timetzdata -o XrayR -trimpath -ldflags "-s -w -buildid="

FROM scratch
ENV TZ=Asia/Shanghai

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY release/config/geoip.dat /etc/XrayR/geoip.dat
COPY release/config/geosite.dat /etc/XrayR/geosite.dat
COPY --from=builder /app/XrayR /usr/local/bin/XrayR

ENTRYPOINT [ "/usr/local/bin/XrayR", "--config", "/etc/XrayR/config.yml"]

FROM golang:1.18-alpine AS builder

WORKDIR /src
COPY main.go go.mod go.sum /src/
RUN CGO_ENABLED=0 go build -ldflags="-s -w"


FROM alpine
COPY --from=builder /src/HeatingMqttBridge /bin/HeatingMqttBridge

USER nobody
ENV BROKER= HEATING=

CMD ["/bin/HeatingMqttBridge", "-env"]

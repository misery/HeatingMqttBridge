FROM golang:alpine AS builder

WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w"


FROM alpine
COPY --from=builder /src/HeatingMqttBridge /bin/HeatingMqttBridge

USER nobody
ENV BROKER= HEATING=

CMD ["/bin/HeatingMqttBridge", "-env"]

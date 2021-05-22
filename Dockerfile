FROM golang:alpine AS builder

WORKDIR /src
COPY . .
RUN go build -ldflags="-s -w"


FROM alpine
COPY --from=builder /src/HeatingMqttBridge /bin/HeatingMqttBridge

USER nobody
ENV BROKER= HEATING=

CMD ["/bin/HeatingMqttBridge", "-env"]

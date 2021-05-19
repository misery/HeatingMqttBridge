FROM golang:alpine AS builder

WORKDIR /src
COPY . .
RUN go build -ldflags="-s -w"


FROM alpine
RUN apk --no-cache add tini
COPY --from=builder /src/HeatingMqttBridge /bin/HeatingMqttBridge
COPY --from=builder /src/cmd.sh /bin/cmd.sh

USER nobody
ENV BROKER= HEATING= POLLING=

ENTRYPOINT ["/sbin/tini", "--"]
CMD ["/bin/cmd.sh"]

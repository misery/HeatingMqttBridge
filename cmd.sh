#!/bin/sh

heating=${HEATING:-""}
broker=${BROKER:-""}
polling=${POLLING:-90}

/bin/HeatingMqttBridge  \
    -heating $heating   \
    -broker $broker     \
    -polling $polling


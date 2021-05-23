[![MIT License](https://img.shields.io/badge/license-MIT-blue.svg)](https://github.com/misery/HeatingMqttBridge/blob/main/LICENSE)
[![Docker pulls](https://img.shields.io/docker/pulls/aklitzing/heatingmqttbridge.svg)](https://hub.docker.com/r/aklitzing/heatingmqttbridge/)
[![Go Report Card](https://goreportcard.com/badge/github.com/misery/HeatingMqttBridge)](https://goreportcard.com/report/github.com/misery/HeatingMqttBridge)
[![Total alerts](https://img.shields.io/lgtm/alerts/g/misery/HeatingMqttBridge.svg?logo=lgtm&logoWidth=18)](https://lgtm.com/projects/g/misery/HeatingMqttBridge/alerts/)

# Heating Mqtt Bridge
This tiny bridge polls the central station of Roth EnergyLogic and
pushes all information to an Mqtt broker.


## Parameters
All parameters can be passed via cmdline arguments or via environment variables. If both are passed the cmdline argument has precedence.

- ``-env`` Allow environment variables if provided, otherwise they will be ignored. (optional)
- ``-heating`` / ``HEATING`` IP or hostname of your Energy Logic. (**required**)
- ``-broker`` / ``BROKER`` IP or hostname with port of your MQTT broker. (*example: 192.168.1.2:1883*) (**required**)
- ``-user`` / ``BROKER_USER`` Username of your MQTT broker. (optional)
- ``-password`` / ``BROKER_PSW`` Password of your MQTT broker. (optional)
- ``-topic`` / ``TOPIC`` Topic-Prefix of provided information. (optional, "roth" as default)
- ``-clean`` / ``CLEAN`` Set clean session for MQTT. (optional)
- ``-polling`` / ``POLLING`` Refresh interval in seconds. (optional, 90 seconds default)
- ``-full`` / ``FULL`` Provide any information to broker, most times this is not necessary. (optional)


## Information
The EnergyLogic provide some system information and information about any wireless sensor. Those wireless sensors are prefixed by a consecutive number of the central station like ``G0``, ``G1`` and so on. The number of wireless sensors are indicated by ``totalNumberOfDevices``.

### Example
```
  isMaster: 1
  totalNumberOfDevices: 3

  ...

  hw/HostName: ROTH-0111A1
  hw/IP: 192.168.1.2

  ...

  R0/numberOfPairedDevices: 1

  ...

  R1/numberOfPairedDevices: 2

  ...

  devices/G0/name: LivingRoom
  devices/G0/RaumTemp: 22.55
  devices/G0/SollTemp: 23.00
  
  ...
  
  devices/G1/name: Kitchen
  devices/G1/RaumTemp: 21.35
  devices/G1/SollTemp: 21.00
```

### Set values
The following room values are settable and will be propagated to the EnergyLogic.
Be aware that ``Gx`` needs a valid room number like ``G0``, ``G1`` and so on.

- ``name`` settable via ``devices/Gx/set/name``. This changes the room name.
- ``SollTemp`` settable via ``devices/Gx/set/SollTemp``. This changes the target temperature.
- ``OPMode`` settable via ``devices/Gx/set/OPMode``. This changes heating mode for this device.
  - ``0`` Day (normally **On**)
  - ``1`` Night
  - ``2`` Holiday (normally **Off**)

### Docker
You can run this bridge in a container with Docker.

``docker run --rm --name heating -e HEATING=192.168.1.3 -e BROKER=192.168.1.2:1883 aklitzing/heatingmqttbridge``


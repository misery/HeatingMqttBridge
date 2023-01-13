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
- ``-heating`` / ``HEATING`` IP or hostname of your EnergyLogic. (**required**)
- ``-broker`` / ``BROKER`` IP or hostname with port of your MQTT broker. (*example: 192.168.1.2:1883*) (**required**)
- ``-user`` / ``BROKER_USER`` Username of your MQTT broker. (optional)
- ``-password`` / ``BROKER_PSW`` Password of your MQTT broker. (optional)
- ``-topic`` / ``TOPIC`` Topic-Prefix of provided information. (optional, "roth" as default)
- ``-clean`` / ``CLEAN`` Set clean session for MQTT. (optional)
- ``-polling`` / ``POLLING`` Refresh interval in seconds. (optional, 300 seconds as default)
- ``-tempchange`` / ``TEMPCHANGE`` Temperature change warning in hours. (optional, 12 hours as default)
- ``-full`` / ``FULL`` Provide any information to broker, most times this is not necessary. (optional)
- ``-dns`` / ``DNS`` Use internal DNS cache. (optional, default: true)
- ``-verbose`` / ``VERBOSE`` Provide more verbose logging. (optional)


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

  G0/name: LivingRoom
  G0/RaumTemp: 22.55
  G0/SollTemp: 23.00
  
  ...
  
  G1/name: Kitchen
  G1/RaumTemp: 21.35
  G1/SollTemp: 21.00
```

### Set values
The following room values are settable and will be propagated to the EnergyLogic.
Be aware that ``Gx`` needs a valid room number like ``G0``, ``G1`` and so on.

- ``name`` settable via ``Gx/set/name``. This changes the room name.
- ``SollTemp`` settable via ``Gx/set/SollTemp``. This changes the target temperature.
- ``OPMode`` settable via ``Gx/set/OPMode``. This changes heating mode for this device.
  - ``0`` Day (normally **On**)
  - ``1`` Night
  - ``2`` Holiday (normally **Off**)

### Available topic
If this bridge is ``online`` or ``offline`` can be checked with ``available`` topic.
The topic ``available`` under ``Gx`` indicates "no battery detection". This bridges
exposes both ``available`` topics to home assistant auto discovery. So all ``available``
needs to be ``online``. Otherwise all or a single climate is ``N/A``. This depends
on ``bridge not running`` or ``no battery``.

### Low / no battery detection
The EnergyLogic has no indicator to show low or no battery on a wireless controller.
It just stops sending temperature values. So we send a ``Gx/RaumTempLastChange``
warning if the tempatures of a room has no changes in specified time (see above).
This is configurable with the ``-tempchange`` parameter.
The ``Gx/available`` topic will be switched to offline after that.

### Auto discovery
It is possible to use auto-discovery support of Home Assistant and openhab (https://github.com/openhab/openhab-addons/issues/10764).

### Docker
You can run this bridge in a container with Docker.

``docker run --rm --name heating -e HEATING=192.168.1.3 -e BROKER=192.168.1.2:1883 aklitzing/heatingmqttbridge``


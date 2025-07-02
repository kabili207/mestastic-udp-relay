# Meshtastic UDP Relay

This is a designed to bridge across poorly connected mesh networks using the Mesh over LAN (also called Mesh via UDP) that was introduced
with Meshtastic firmware version 2.6 using an MQTT server as the backhaul connection

## Using the relay

When running for the first time the relay will prompt for the MQTT broker url and user credentials and persist them to `config.yml`,
which is saved in the same location as the relay executable

If you would rather create the file yourself or plan to use it in a containerized environment like docker, you may use the example below

```yaml
broker: ssl://mqtt.wpamesh.net:8883
user: meshRelayUser
password: totally_a_secret
root_topic: mesht/relay
```

### Why not just use the built-in MQTT?

The built-in MQTT support in Meshtastic has a lot of restrictions, which make perfect sense for privacy, but become a large annoyance when
trying to bridge disparate or poorly connected meshes. Many of these restrictions can be circumvented by directly connecting to and MQTT
server that's hosted on a private IP address, but this is obviously not possible without using a VPN and still limits nodes to retransmitting
traffic on channels it already knows about.

### Okay, so why not use a VPN?

While this can be done with a VPN and some firewall rules, it needs to be done very carefully to ensure private traffic stays isolated and requires
a good level of trust in whoever you allow to connect to it.

Additionally, a standard firewall or UDP proxy does not have any knowledge of the underlying protocol of the packets being sent across. Writing
dedicated software to act as a relay or bridge ensures that only the necessary traffic is routed and allows it to perform things like packet
de-duplication or spam filtering that is not available with a basic VPN and firewall.
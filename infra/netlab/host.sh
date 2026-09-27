#!/bin/sh
# Holds the network of a host on a LAN and routes it through the LAN's
# router. Nodes join it with network_mode: service:<host>.
# Usage: host.sh <router ip>
set -e
ip route replace default via "$1"

trap 'exit 0' TERM INT
sleep 2147483647 &
wait

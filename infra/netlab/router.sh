#!/bin/sh
# NAT router: hides the LAN behind the router's address and drops
# connections from outside that the LAN didn't start. Extra arguments limit
# which outbound traffic is allowed, e.g. "-p tcp --dport 443".
# Usage: router.sh <lan subnet> [iptables match...]
set -e
lan=$1
shift
iptables -t nat -A POSTROUTING -s "$lan" ! -d "$lan" -j MASQUERADE
iptables -P FORWARD DROP
iptables -A FORWARD -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
iptables -A FORWARD -s "$lan" "$@" -j ACCEPT

trap 'exit 0' TERM INT
sleep 2147483647 &
wait

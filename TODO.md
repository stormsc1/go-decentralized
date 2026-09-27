# TODO

## Relay traffic is not encrypted or authenticated
Nodes talk plain HTTP, also through relays, so a relay can read and alter
what it relays. Reservations aren't authenticated either: a node can reserve
under another node's ID and receive its traffic.

Fix: mutual TLS keyed by the node keys, end to end through relays. Relays
then only see ciphertext, and only the owner of an ID can reserve it.

## Peers on the same LAN
Nodes find their LAN peers via mDNS and reach them directly when looking
them up by ID. Other lookups, such as capability providers, return the
addresses a node advertises, so traffic between LAN peers found that way
still goes through the relay.

Fix: prefer a LAN peer's LAN address wherever its contact is used, e.g. in
the network layer.

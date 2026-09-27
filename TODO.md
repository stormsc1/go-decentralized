# TODO

## Relays aren't verified
A relay address names the node behind the relay, not the relay, so a caller
can't tell a relay from an impostor at its address. End-to-end TLS keeps an
impostor from reading or altering traffic, but it could drop it.

Fix: put the relay's ID in relay addresses, and verify it too.

## Connections aren't reused
Every message opens a new connection and TLS handshake, twice over for
relayed ones. Fine for discovery, but it adds up for busy traffic.

Fix: keep connections to peers open and multiplex messages over them.

## Peers on the same LAN
Nodes find their LAN peers via mDNS and reach them directly when looking
them up by ID. Other lookups, such as capability providers, return the
addresses a node advertises, so traffic between LAN peers found that way
still goes through the relay.

Fix: prefer a LAN peer's LAN address wherever its contact is used, e.g. in
the network layer.

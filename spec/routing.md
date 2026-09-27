# Routing

How nodes find each other, and the nodes that provide a capability, without a central registry: over a Kademlia DHT ([Maymounkov & Mazières, 2002](https://www.scs.stanford.edu/~dm/home/papers/kpos.pdf)), and on the local network with mDNS. Every node does this; it's part of the wire protocol. Its capabilities, built into every node as `routing.*`, and their schemas are in [internal/routing/routing.module.yaml](../internal/routing/routing.module.yaml).

Modules call other nodes by ID alone. The node reaches the other node over its session with it, if any. Otherwise it looks the node up: its LAN addresses first, then the addresses in its record or routing table contact, and if it knows neither, a DHT lookup.

## IDs and distance

- Node IDs and keys are 256 bits, as 64 lowercase hex digits. The key of a capability is the SHA-256 of its ref, e.g. `sha256("debug.report")`.
- The distance between two IDs is their XOR, compared as big-endian unsigned integers.
- K = 20: the size of a bucket, and of a lookup's result. α = 3: how many calls a lookup keeps in flight.

## Routing table

- Bucket i holds nodes whose IDs share exactly i leading bits with ours, at most K of them. Full buckets keep their longest-known contacts. A contact is dropped when a call to it fails.
- Only nodes with direct addresses enter routing tables. Nodes without, in client mode, still use the DHT, and are found through their records.

## Protocol

`dht_find_node`, `dht_find_providers` and `dht_add_provider` are network capabilities, internal to the DHT.

- Every call carries the caller's contact in `from`, and every result the callee's. The callee adds the caller to its routing table only if `from.id` is the ID the caller proved. The caller adds the callee only if it proved the ID asked for.
- `dht_find_node {target}` returns the K contacts the callee knows closest to `target`.
- `dht_find_providers {target}` also returns the records the callee stores for `target`.
- `dht_add_provider {target, record}` stores `record` as a provider of `target`, if it's the caller's own record (its key's ID is the caller's), validly signed and fresh.

## Records

A record, `{data, sig}`, describes a node. `data` is JSON: `{public_key, name, addrs, time}`, with every address the node has, relays included, and `time` in Unix milliseconds. `sig` signs `data` as is, for the purpose `routing.record` ([modules.md](modules.md), "Signing").

Records expire 30 minutes after `time`, and nodes reject records dated more than 30 minutes ahead. A newer record of a node replaces an older one.

## Lookups

A lookup is iterative. It calls the α closest contacts it hasn't asked yet, merges the contacts they return, drops the ones that fail, and repeats until the K closest contacts it knows have all answered. Lookups for providers collect the valid records they see on the way.

## Joining and announcing

- A node joins by calling `dht_find_node` for its own ID on any node it knows: a configured bootstrap address, or a peer found on the local network (see "mDNS"). Then it looks up its own ID, and does so again every minute.
- Every 10 minutes, and whenever its addresses change, a node stores its record on the K nodes closest to its own ID, so it can be found by ID even in client mode. It also stores it on the K nodes closest to the key of every capability it provides: those with network access that aren't internal.

## mDNS

Nodes find each other on the local network with multicast DNS service discovery ([RFC 6762](https://www.rfc-editor.org/rfc/rfc6762), [RFC 6763](https://www.rfc-editor.org/rfc/rfc6763)). A studio's nodes find each other with no configuration, even without internet.

A node that accepts connections answers queries for the service `_go-decentralized._tcp.local` on 224.0.0.251, port 5353:

- Its instance is the first 16 hex digits of its ID: `<instance>._go-decentralized._tcp.local`.
- The answer is the service's PTR record, naming the instance. Additional records give the rest: an SRV record with the port it accepts connections on, at `<instance>.local`; a TXT record with `id=<its ID>` and `name=<its name>`; and an A record for each of its IPv4 addresses, other than loopback ones.
- A query from port 5353 gets its answer by multicast, with records that live 120 seconds. Any other query is one-shot, and gets its answer directly, with its ID and question, and records that live 10 seconds (RFC 6762, section 6.7).

Every 30 seconds, a node asks for the service's PTR records with a one-shot query, and keeps the nodes that answer within a second, at their LAN addresses, `<A>:<SRV port>`. These LAN peers serve as bootstrap nodes, and are tried at their LAN address first when looked up by ID.

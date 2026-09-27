# Discovery

The discovery module finds nodes, and the nodes that provide a capability, over a Kademlia DHT ([Maymounkov & Mazières, 2002](https://www.scs.stanford.edu/~dm/home/papers/kpos.pdf)), without a central registry. Its capabilities and their schemas are in [modules/discovery/module.yaml](../../modules/discovery/module.yaml). This is how they behave, so other implementations can join the same DHT.

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

A record, `{data, sig}`, describes a node. `data` is JSON: `{public_key, name, addrs, time}`, with every address the node has, relays included, and `time` in Unix milliseconds. `sig` signs `data` as is, for the purpose `discovery.record` ([modules.md](../modules.md), "Signing").

Records expire 30 minutes after `time`, and nodes reject records dated more than 30 minutes ahead. A newer record of a node replaces an older one.

## Lookups

A lookup is iterative. It calls the α closest contacts it hasn't asked yet, merges the contacts they return, drops the ones that fail, and repeats until the K closest contacts it knows have all answered. Lookups for providers collect the valid records they see on the way.

## Joining and announcing

- A node joins by calling `dht_find_node` for its own ID on any node it knows: a configured bootstrap address, or a peer found by mDNS. Then it looks up its own ID, and does so again every minute.
- Every 10 minutes, and whenever its addresses change, a node stores its record on the K nodes closest to its own ID, so it can be found by ID even in client mode. It also stores it on the K nodes closest to the key of every capability it provides: those with network access that aren't internal.

## mDNS

Nodes advertise `_go-decentralized._tcp` on the local network, with the TXT records `id=<id>` and `name=<name>`, and browse for it every 30 seconds. Peers found this way serve as bootstrap nodes, and are tried at their LAN address first when looked up by ID.

# Stores, pools and transport

Status: draft for review, from the design Q&A on 2026-09-27. The transport and the stores are built; pools aren't. Today a node has one SQLite store, namespaced by module ([spec/modules.md](../../spec/modules.md), "Storage"), and nodes authenticate with self-signed mutual TLS. **Open** marks what's still to decide.

Two needs drive this: organisations too big for one node, and running nodes on Cloud Run.

## Stores

**Decided**
- **Many stores per node, of different types.** Each is backed by a driver compiled into the node, e.g. entity stores on SQLite or Postgres, and a blob store on disk or GCS.
- **Modules declare, nodes bind.** A module declares the stores it needs in `module.yaml`, by name and type, with their entity types. The node definition binds each to one of its stores, explicitly: there's no default, so nothing lands in a store by accident. Only `local`, the node's own store, is implicit.
- **Modules never see drivers.** They reach stores through built-in capabilities, as now, and name the store by their own name for it.
- **Writes:** batches (all or nothing) and conditional writes (only if the record's version is still N). No transactions spanning calls.
- **Sharing:** data is namespaced by module in every store, and other modules reach it through capabilities. The same module on several nodes, bound to the same store, shares its data; that's how pools work. Declared read access, e.g. search reading chat's events, can come later without breaking anything.
- **Postgres:** the driver uses pgx.

**Sketch**

```yaml
# module.yaml (chat)
stores:
  - name: events
    type: entity
    entities:
      - name: event
        schema: {$ref: "#/$defs/event"}
        indexes: [channel, time]
  - name: attachments
    type: blob

# node definition
stores:
  main:  {driver: postgres, url: "${DATABASE_URL}"}
  files: {driver: gcs, bucket: org-files}
modules:
  - name: chat
    stores: {events: main, attachments: files}
```

- **Types:**
  - `entity`: records with schemas, queried by indexed fields;
  - `kv`;
  - `blob`: files by content hash;
  - `sql`: opt-in;
  - later, `graph` and `vector`.
- **Capabilities:** each type has its own built-in capabilities, e.g. `store.put {store, entity, id, record}` and `blob.put`. `store` can be left out when the module has one store of that type.

**Open**
- **Migrations** when an entity type's schema changes.

## Pools

**Decided**
- **A replica is its own node,** with its own node ID. A pool is a set of nodes that share stores. An organisation too big for one node runs, say, 20 Cloud Run instances on one Postgres.
- **On Cloud Run,** a replica makes a new key at start, so its node ID changes on every start.

**Needed**
- **A node-local store.** Every node has its own local store for its own state: peers, routing table and caches. Built-in modules use it, and it's never shared. It's a SQLite file, or memory where nothing persists, such as on Cloud Run.
- **Concurrent writers.** Every driver supports batches and conditional writes. SQLite files can't be shared between machines, so pools need a networked database; Postgres first.
- **Events across the pool.** An app subscribed on member 3 must hear events emitted on member 7. Cloud Run instances can't reach each other directly, so events go through the pool's shared store, e.g. Postgres `LISTEN`/`NOTIFY`.
- **Once-per-pool work.** Some background work must run once per pool, not on every member, e.g. syncing a channel with another organisation. Members take leases in the shared store.
- **Cloud Run settings:**
  - at least one instance, and CPU always allocated, so relay reservations and sessions live on;
  - WebSocket requests end after at most 60 minutes, and sessions redial.

**Open**
- **Proving pool membership (deferred):** how a member proves it belongs to its pool, or acts for the organisation's DID. For example, a signing key the organisation delegated to, kept as a secret, signs a delegation for each replica. It gets designed with finding an organisation's nodes by DID, for the chat.
- **Finding a pool:** how other nodes find one. Through its members' own addresses, or through the service URL, which any member may answer.

## Transport: Noise inside WebSockets

Cloud Run, like corporate proxies that inspect TLS, ends TLS before the node, so client certificates never reach it.

**Decided**
- **Noise handshake, not mutual TLS.** Nodes prove their identity with a [Noise](https://noiseprotocol.org/noise.html) handshake inside the WebSocket. It authenticates both nodes' keys and encrypts the session end to end, so proxies that end TLS only see ciphertext.
- **Ordinary outer connection:** plain HTTP or HTTPS, with ordinary certificates (the platform's or Let's Encrypt), or none on a LAN.
- **Our own implementation** on Go's standard crypto, with no new dependencies.

**Sketch**
- **Pattern:** `Noise_XX_25519_AESGCM_SHA256`. Its primitives are all in Go's standard library.
- **Node keys stay Ed25519,** so node IDs don't change. In the handshake, each side sends its Ed25519 public key and its signature of its Noise static key, as libp2p's Noise does.
- **Framing:** after the handshake, each WebSocket message carries Noise transport messages. Noise limits those to 64 KiB, so a larger JSON-RPC message spans several.
- **Relays** forward WebSocket messages instead of splicing raw bytes, and Noise runs end to end through them.
- **Browsers** can run the same handshake with WebCrypto. That makes "the browser is a light node" (open in [identity-storage-chat.md](identity-storage-chat.md)) possible.

**Proposed: addresses are WebSocket URLs**

A Cloud Run service is only reachable through its URL, and `host:port` can't say whether to use TLS or which path to dial, so addresses change either way.
- **An address is what the caller dials,** e.g. `wss://chat-abc.a.run.app` or `ws://10.0.0.5:8080`.
- **A relayed address** is the relay's URL with the target in it: `wss://relay.example.com/v1/relay/<id>`.
- **Node IDs stay out of addresses:** the Noise handshake proves who answered. A pool's service URL is answered by any member, so it can't name one.
- **Precedent:** Nostr relays and AT Protocol service endpoints are plain URLs, as are DID documents' `serviceEndpoint`s. libp2p's multiaddrs (`/dns4/…/tcp/443/wss/p2p/<id>`) and Ethereum's `enode://<key>@host:port` put the node's key in the address. Multiaddrs describe stacks of protocols, and WebSocket is our only one.

**Open**
- **Handshake latency:** `XX` takes 1.5 round trips. `IK` saves one when the caller already knows the other's Noise key.

## Order of work

1. **Transport:** Noise in WebSockets, URL addresses, and relays forwarding messages. Cloud Run needs this.
2. **Stores:** named, typed stores that modules declare and nodes bind; a node-local store for built-ins, which also saves peers; batches and conditional writes.
3. **Pools:** a Postgres driver, events through the shared store, and once-per-pool work.
4. **The chat.**

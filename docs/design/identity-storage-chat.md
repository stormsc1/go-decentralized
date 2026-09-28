# Identity, storage and chat

Status: draft for review, from the design Q&A on 2026-09-27. Storage, events, `did:key` with device keys, and the chat's event graph (`modules/chat/graph`) are built; the rest isn't. **Open** marks what's still to decide.

The first app on the platform is a realtime chat. It's small enough to build soon, and it exercises what the platform lacks: identities for people, storage, pushed events and data copied between nodes.

## Identity

**Decided**
- **Who gets DIDs:** people, organisations and other entities, such as projects. Nodes keep their node IDs.
- **Method:** `did:key` (Ed25519) to start. A method with updatable documents can come later, if we need key rotation.
- **Devices:** a person's root key signs a delegation for each device key: "device key X may act as me until date T". A lost device is revoked on its own, and the DID never changes. A device isn't necessarily a node.
- **Verification:** open verifiers issue binding credentials: "this DID belongs to a verified person, with these attributes".
  - Verifiers can be an EU Digital Identity Wallet presentation (its PID, over OpenID4VP), an organisation's HR, email, phone or document checks, or vouching by verified people.
  - Each node or app picks which verifiers it trusts; nothing is central.
  - Google and email sign-in must be possible, as one kind of verifier or sign-in method on a node.
- **Membership:** an organisation's DID signs a membership credential that the person holds and presents. It's revoked through the organisation's revocation list.
- **Keys:** on devices first. A vault service is opt-in, later.
- **Chat v1:** a `did:key` per person, with no verification. People are who they say.

**Open**
- **How a device that isn't a node reaches the network,** where its messages wait while it's offline, and where the web app's key lives:
  - (a) The browser talks only to the node that serves the web app. That node stores the person's copies and passes their calls on. The key stays in the browser (WebCrypto, not exportable), which signs every event, so the node never holds it.
  - (b) The browser is a light node itself. That needs node identity inside the session, e.g. a Noise handshake, because browsers can't do our mutual TLS, and it still needs some node to hold messages while the browser is offline.

  I recommend (a): it keeps keys on devices, needs nothing new in the transport, and makes the serving node the person's home.
- **Recovering a lost root key:** backup, social recovery (M of N trusted people), or re-binding a new DID through a national wallet.
- **People without an organisation:** where their home node comes from (a node they run, rent, or a provider's), once (a) is settled.

## Storage

**Decided**
- **Stores the node offers modules:**
  - EntityStore: typed records, with GetById, Put, Delete and queries on indexed fields.
  - KeyValueStore.
  - BlobStore: files by content hash.
  - SQL, opt-in for modules that declare they need it, in SQLite's dialect.
  - Later: graph and vector stores, and an organisation's own Postgres.
- **Queries:** structured first (filters on indexed fields, sort, limit), with SQL opt-in.
- **Entity types:** declared in `module.yaml`, with JSON Schemas and indexes. The node validates records and builds the indexes in any backend.
- **Drivers:** compiled into the node, native only, and shaped like modules. SQLite first, in pure Go (`modernc.org/sqlite`). Each module's data is namespaced.
- **Local first:** stores belong to one node. Ownership, borrowing and replication get designed separately; the chat needs a first version of replication (below).

**Sketch**

```yaml
# module.yaml
entities:
  - name: channel
    schema: {$ref: "#/$defs/channel"}
    indexes: [created]
  - name: event
    schema: {$ref: "#/$defs/event"}
    indexes: [channel, time]
```

Modules reach the stores through built-in capabilities, like `node.*` and `routing.*`: `store.get`, `store.put` and `store.query` for entities, `store.kv_*` for key-value. The node scopes each call to the calling module, so process modules get storage without anything extra.

**Open**
- How SQL opt-in looks (a SQLite file per module, reached through a capability?), and whether stores have transactions.
- Migrations when an entity type's schema changes.
- How a node definition maps modules to backends: now decided in [stores-pools-transport.md](stores-pools-transport.md).

## Chat

**Decided**
- **v1 features:** direct messages, channels, channels across organisations, edits, reactions, read receipts, presence and typing.
- **Not yet:** deleting (a decision point in TODO.md), threads, attachments.
- **Clients:** a web app, and chat in the CLI.
- **Copies:** every member's node keeps a full copy of each channel it's in, so no party loses a conversation when another closes the channel. That matters for legal reasons.
- **Encryption:** between nodes (TLS) only, for now.
- **Signatures:** every event is signed by its author's key: a device key their root key delegated to.
- **Order:** causal. Every event names the latest events its author had seen, forming an event graph, and every node sorts the graph the same way: topologically, ties by timestamp, then ID.
- **Invites:** anyone in a channel can invite. Admins and removing people are step 2.
- **History:** a new member's node copies a channel's full history from any member's node.
- **Finding people and channels:** by invites and links only.
- **Presence and typing:** live events, not stored.
- **Identity, for now (2026-09-28):** the chat has a user of its own, `{id, name, external}`: `id` is a UUID the web app picks and keeps in the browser, `name` is chosen freely, and `external` is where the person's identity outside the chat, a DID, goes later. Events carry the chat user as author and aren't signed yet. Identity gets looked at again after the chat works on one node.

**Sketch**
- **An event:** `{channel, author, kind, body, parents, time}`, and `sig` once events are signed. Its ID is its hash; the create event is hashed without a channel, since its ID becomes the channel's. `kind` is one of: create, invite, join, message, edit, reaction, receipt.
- **A channel:** its ID is the hash of its create event. A direct message is a channel of two.
- **Sync (built):** a member's node pushes new events to the other members' nodes, retrying a few times, and a node fetches any parents it's missing from the pusher. Nodes also catch up by comparing heads: at start and every minute, a node tells each member node its heads for a channel, fetches the ones it lacks, and gets the other's heads back to do the same, so nodes apart for a while converge on their own. Typing and presence (client heartbeats) cross nodes as notices, never stored.
- **What the platform needs first:**
  - storage, for events and channels;
  - pushed events on links, for live updates between nodes;
  - pushes from a node's local API to the web app.

**Open**
- **Deleting** (see TODO.md).
- **Step 2:** admins, removing people, moderation.
- **Access:** who in an organisation may read the copies its node keeps.
- **Spam and abuse,** with open invites.
- **End-to-end encryption,** later.

## Order of work

1. **Storage:** the EntityStore and KeyValueStore on SQLite, with entity types in `module.yaml`.
2. **Pushed events:** on links between nodes, and from the local API to web apps.
3. **Identity v1:** `did:key`, device keys and signed events.
4. **The chat module:** events, causal order and copying between member nodes, with the CLI and web app.
5. **Later:** verifiers and Google/email sign-in, the vault, end-to-end encryption, admins, deleting, and attachments (the BlobStore and file transfers).

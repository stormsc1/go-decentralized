# Modules

What modules declare, what nodes check and give them, and how nodes run them. Version 1.

## Manifest

Every module has a manifest, usually a `module.yaml` ([JSON Schema](module.schema.json)):

```yaml
name: greeter              # [a-z][a-z0-9_]*, unique in a node
version: 1.0.0             # semantic version
description: Says hello across the network.
capabilities:
  - name: hello            # [a-z][a-z0-9_]*, unique in the module
    description: Greets the caller.
    access: network        # local (default) or network
    input:                 # schema of the input
      type: object
      properties:
        name: {type: string}
    output: {$ref: "#/$defs/greeting"}   # schema of the result
events:                    # what the module tells subscribers as it happens
  - name: greeted          # unique among capabilities and events
    schema: {type: object, properties: {name: {type: string}}}
stores:                    # stores the module needs; the node binds them to its own
  - name: visits           # [a-z][a-z0-9_]*, unique in the module
    type: entity           # entity (records) or kv (key-value pairs)
    entities:              # types of records, for entity stores
      - name: visit
        schema:            # schema of a record
          type: object
          properties: {name: {type: string}, time: {type: integer}}
        indexes: [time]    # fields queries select and sort by
$defs:                     # schemas others refer to, as #/$defs/<name>
  greeting: {type: object, properties: {greeting: {type: string}}}
```

- Schemas are JSON Schema 2020-12. Inputs and results are objects; an unset schema means any object. Byte strings are `{type: string, contentEncoding: base64}`.
- `local` capabilities are for the node itself: its modules and its local tools. `network` ones are for other nodes too.
- `internal: true` marks capabilities of a protocol between modules, such as the DHT's. Nodes don't announce them and tools don't list them. Access still applies.
- `config` is the JSON Schema of the module's block in the node definition, see "Configuration".
- The names `module` and those of the node's own modules, `node`, `network`, `routing` and `store`, are reserved.

## Configuration

A module's environment is its block in the node definition, `modules[].config`: whatever the module needs from where it runs, such as a connection string for tables of its own. The manifest's `config` schema says what the block may hold, and the node checks the block against it before serving the module, so a wrong block keeps the node from starting; an unset schema means any object. The module gets the block as its config (`module.start`'s `config`; Go's `Factory` decodes it). Nodes also tell modules their data directory, for files a module keeps of its own; it's empty if the node keeps nothing on disk.

```yaml
# module.yaml
config:
  type: object
  required: [database_url]
  properties:
    database_url: {type: string, format: uri, description: Postgres, for the module's own tables.}

# node definition
modules:
  - name: greenlight
    config: {database_url: "${DATABASE_URL}"}
```

## Calls

A call names a capability by its ref and carries an input. It ends with a result or an error ([wire.md](wire.md), "Messages" and "Errors"). The node running the module dispatches every call to it: from another node, from a module, or from a local tool. Before it hands a call over, the node:

1. fails it with `unimplemented` if no module has the capability;
2. fails it with `permission_denied` if the caller is another node and the capability is local;
3. fails it with `invalid_argument` if the input doesn't match the capability's input schema. An empty input is `{}`.

The module learns who called: the ID the calling node proved in its session's handshake, or its own node's ID for local calls.

## What nodes give modules

Modules call capabilities of their own node's modules, and of other nodes, which they name by ID alone: the node finds them ([routing.md](routing.md)). They call as the node: other nodes see the node's ID. They may also call without waiting for the end, for news that may as well get lost, such as someone typing ([wire.md](wire.md), "Messages").

Nodes have capabilities of their own, for their modules and tools. Schemas: `internal/node/node.module.yaml`.

| Capability | Access | Does |
|---|---|---|
| `node.info` | local | Describes the node: ID, public key, addresses, modules and their capabilities. |
| `node.sign` | local, internal | Signs data with the node's key, see "Signing". |
| `node.emit` | local, internal | Emits one of the calling module's events, see "Events". |
| `node.traces` | local, internal | Lists the calls the node made, for debugging. |
| `node.inspect` | local, internal | Reports the state of the node's network and modules, for debugging. |

## Events

Modules tell subscribers what happens, such as a message arriving, with the events their manifest declares. The node checks each event's body against its schema, and fails `node.emit` with `invalid_argument` if it doesn't match.

Subscribers are local tools, such as apps, on the local API: `GET /v1/events?ref=<module>.<event>&ref=...` streams the events named as [server-sent events](https://html.spec.whatwg.org/multipage/server-sent-events.html), each with its ref as the event type and its body as the data. Events arrive in the order they happened. A subscriber that falls 64 events behind loses its subscription: it should subscribe again, then catch up by calling capabilities.

## Storage

A module declares the stores it needs in its manifest, each of a kind: `entity`, records of the entity types it lists, or `kv`, key-value pairs. The node definition declares the node's stores, each on a driver compiled into the node with the options that driver takes (SQLite so far), and each module's block binds every store the module declares to one that keeps that kind of data; a store left unbound, or bound to one of another kind, keeps the node from starting. Every node also has a store called `local`, for its own parts, which modules can't use, so nodes sharing a store never share it. Each module's data is apart from the others', in a namespace per module and store.

```yaml
# node definition
stores:
  main: {driver: sqlite}                     # <node name>.main.db, in the data directory
  cache: {driver: sqlite, path: ":memory:"}
modules:
  - name: chat
    stores: {events: main, seen: cache}      # the module's names for its stores
```

A module with data of a shape of its own, such as its own Postgres tables, takes what it needs to connect as configuration (see "Configuration") and connects itself; the node isn't involved.

Modules reach their records and pairs through the `store` capabilities (schemas: `internal/node/store.module.yaml`), which only modules can call, naming the store as their manifest does, or not at all when they have one of that kind:

- Records of the entity types in their manifest: `store.put`, `get`, `delete` and `query`. A record is a JSON object with an ID, up to 256 characters, and the node rejects records that don't match their type's schema with `invalid_argument`. Queries select and sort by indexed fields and the ID, and return up to 100 records unless they say, at most 1000.
- Key-value pairs, of any JSON value: `store.kv_get`, `kv_put`, `kv_delete` and `kv_list`, which lists keys by prefix.
- Batches: `store.batch` applies writes of both kinds in order, all or none.

Every record and pair has a version, counting its writes from 1, which reads return and writes return anew. A write may require a version with `if_version`, 0 for "none yet": if the record or pair has another, the write, or the whole batch, fails with `store.conflict`. So several writers, e.g. the nodes of a pool sharing a store, don't overwrite each other unawares.

`get` and `kv_get` fail with `not_found` if there's nothing there. Go's API is `Env.Store`, and `Env.Entities`, `Env.KV` and `Env.Batch` for a module's only store of each kind.

## Signing

Modules sign with the node's key through `node.sign`, and only for purposes that start with their name, such as `greeter.token`. The node signs, with Ed25519:

```
"decentralized-signature" 0x00 <purpose> 0x00 <data>
```

So a signature made for one purpose can't pass for another, nor for the session handshake's. Signatures cover data as it's carried, so verifying one never needs re-encoding.

## Runtimes

| Runtime | Status |
|---|---|
| Native: compiled into the node | Supported. Each node implementation has its own API for them; Go's is package `module`. |
| Process: a process of its own, one per module | Supported, in any language. See below. |
| Shared library | Not supported yet. |
| WebAssembly | Not supported yet. |

### Process modules

A node definition runs a module in a process of its own with `run`, the command and its arguments:

```yaml
modules:
  - name: greeter
    run: [node, examples/greeter-ts/greeter.ts]
    config: {greeting: Hi}
```

- The node starts the command with the environment variable `DECENTRALIZED_PROTOCOL=1`, the version of this protocol. A module MUST exit if it doesn't speak that version.
- Messages ([wire.md](wire.md), JSON-RPC 2.0) travel over the process's stdin, from the node, and stdout, from the module, one per line, as in MCP's stdio transport. Nothing else may be written to stdout. stderr is the module's log, which the node keeps.
- Both sides make calls and pick the `id`s of their own. Calls run concurrently, and end in any order. Either side can cancel its own call.
- On calls the node sends, `_meta.from` is the ID of the node that made the call.
- On calls the module sends, `_meta.to`, a node ID, asks the node to call that node. Without it, the call is to the node's own capabilities. A call without an `id` goes on as one, and the node doesn't answer it.

The node's first call is `module.start`, with input `{node: {id, name, data_dir}, config}`, where `config` is the module's block from the node definition (see "Configuration"). It returns `{manifest}`, and from then on the node serves the module's capabilities. The node may also call `module.inspect`, which returns the module's state for debugging, or `{}`.

The node stops a module by closing its stdin, and the module MUST then exit. The node kills modules that haven't within 5 seconds, and restarts modules whose process exits. Calls to a module fail with `unavailable` while it's down.

SDKs hide all of this from modules: Go's `module.Serve`, and [sdk/typescript](../sdk/typescript/module.ts).

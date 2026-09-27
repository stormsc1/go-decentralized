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
$defs:                     # schemas others refer to, as #/$defs/<name>
  greeting: {type: object, properties: {greeting: {type: string}}}
```

- Schemas are JSON Schema 2020-12. Inputs and results are objects; an unset schema means any object. Byte strings are `{type: string, contentEncoding: base64}`.
- `local` capabilities are for the node itself: its modules and its local tools. `network` ones are for other nodes too.
- `internal: true` marks capabilities of a protocol between modules, such as the DHT's. Nodes don't announce them and tools don't list them. Access still applies.
- The names `node`, `network` and `module` are reserved.

## Calls

A call names a capability by its ref and carries an input. It ends with a result or an error ([wire.md](wire.md), "Messages" and "Errors"). The node running the module dispatches every call to it: from another node, from a module, or from a local tool. Before it hands a call over, the node:

1. fails it with `unimplemented` if no module has the capability;
2. fails it with `permission_denied` if the caller is another node and the capability is local;
3. fails it with `invalid_argument` if the input doesn't match the capability's input schema. An empty input is `{}`.

The module learns who called: the ID the calling node proved over TLS, or its own node's ID for local calls.

## What nodes give modules

Modules call capabilities of their own node's modules, and of other nodes, which they name by ID alone: the node finds them ([routing.md](routing.md)). They call as the node: other nodes see the node's ID.

Nodes have capabilities of their own, for their modules and tools. Schemas: `internal/node/node.module.yaml`.

| Capability | Access | Does |
|---|---|---|
| `node.info` | local | Describes the node: ID, public key, addresses, modules and their capabilities. |
| `node.sign` | local, internal | Signs data with the node's key, see "Signing". |
| `node.traces` | local, internal | Lists the calls the node made, for debugging. |
| `node.inspect` | local, internal | Reports the state of the node's network and modules, for debugging. |

## Signing

Modules sign with the node's key through `node.sign`, and only for purposes that start with their name, such as `greeter.token`. The node signs, with Ed25519:

```
"decentralized-signature" 0x00 <purpose> 0x00 <data>
```

So a signature made for one purpose, or for TLS, can't pass for another. Signatures cover data as it's carried, so verifying one never needs re-encoding.

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
- On calls the module sends, `_meta.to`, a node ID, asks the node to call that node. Without it, the call is to the node's own capabilities.

The node's first call is `module.start`, with input `{node: {id, name}, config}`, where `config` is the module's block from the node definition. It returns `{manifest}`, and from then on the node serves the module's capabilities. The node may also call `module.inspect`, which returns the module's state for debugging, or `{}`.

The node stops a module by closing its stdin, and the module MUST then exit. The node kills modules that haven't within 5 seconds, and restarts modules whose process exits. Calls to a module fail with `unavailable` while it's down.

SDKs hide all of this from modules: Go's `module.Serve`, and [sdk/typescript](../sdk/typescript/module.ts).

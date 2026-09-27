# Specification

The contract between independently built nodes and modules. A node written in Rust, or a module written in TypeScript, works with this repo's Go ones by following it. Nothing in it depends on Go.

Status: draft, version 1. Anything may change until it's frozen.

| Document | Covers |
|---|---|
| [wire.md](wire.md) | Identity, TLS, addresses, messages, calls and relays between nodes |
| [modules.md](modules.md) | Manifests, capabilities, what nodes give modules, runtimes |
| [routing.md](routing.md) | How nodes find each other: the DHT, and the local network |
| [module.schema.json](module.schema.json) | JSON Schema of `module.yaml` |

## Model

- A **node** runs modules and carries their calls. Its ID is the SHA-256 of its Ed25519 public key.
- A **module** declares **capabilities** in its manifest, each with JSON Schemas for its input and result, and handles calls to them. Modules never see the transport or the encoding.
- A **capability** is named by its **ref**, `<module>.<capability>`, e.g. `greeter.hello`. Its **access** is local (for the node itself) or network (for other nodes too).
- Everything that crosses a link, between nodes or between a node and a process module, is a **message**, in JSON-RPC 2.0: a call, how it ended, or its cancellation.

```
other nodes ──WebSocket──► network ──┐
CLI, local API ──────────────────────┼──► dispatcher ──► native modules
modules ─────────────────────────────┘    access,    ──► process modules (stdio)
                                           schemas
```

## Conventions

- MUST, SHOULD and MAY are as in RFC 2119.
- JSON is UTF-8 (RFC 8259). Byte strings in JSON are base64 (RFC 4648, padded).
- Times are RFC 3339 strings, unless a schema says otherwise.

## Versions

- Links carry the version of their protocol: the WebSocket subprotocol between nodes (wire.md), an environment variable on process pipes (modules.md). A later version can change the encoding, e.g. to a binary one, without modules noticing.
- Manifests carry a semantic version. Within a major version, capabilities change only compatibly: new optional input properties, new result properties. So result schemas must not forbid additional properties.

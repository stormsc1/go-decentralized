# Wire protocol

How nodes talk to each other. Version 1.

## Identity

- Every node has an Ed25519 key pair. Its ID is the SHA-256 of the 32-byte public key, as 64 lowercase hex digits.
- Nodes keep their key, and so their ID, across restarts.

## Connections

- TCP, then TLS 1.3 in which both sides present a certificate: self-signed X.509 with the node's Ed25519 public key. Nothing else in it matters, and peers MUST NOT check it against certificate authorities.
- Each side derives the other's ID from its certificate's key. A caller expecting a particular node MUST abort if the ID differs.
- ALPN is `http/1.1`, and every connection becomes a WebSocket ([RFC 6455](https://www.rfc-editor.org/rfc/rfc6455)), for a session or a relay. Connections look like HTTPS to firewalls and proxies, and pass those that only let HTTPS out on port 443.

## Addresses

| Form | Example | Meaning |
|---|---|---|
| `host:port` | `203.0.113.10:443` | Directly, over TCP. |
| `relay/<host:port>/<id>` | `relay/203.0.113.10:443/9f2c…` | The node `<id>`, through the relay at `host:port`. |

A node may have several addresses. Callers try them in order until one answers.

## Messages

Messages are [JSON-RPC 2.0](https://www.jsonrpc.org/specification):

| Message | JSON-RPC | Contents |
|---|---|---|
| A call | Request | `method` is the capability's ref, `<module>.<capability>`. `params` is its input, an object. `id` is an integer the caller picks, unique among its calls in flight on the link. |
| Its end | Response | `result` is the call's result, or `error` why it failed (see "Errors"). |
| A call without an end | Notification | As a call, without `id`: the caller doesn't wait, and the callee handles it like a call but never answers, even with an error. For news that may as well get lost. |
| A cancellation | Notification `$/cancelRequest` | `params` is `{"id": <the call's id>}`, as in LSP. The callee SHOULD stop, and still answers. |

What a call needs besides its input goes in `params._meta`, which isn't part of the input:

| Key | Meaning |
|---|---|
| `timeout` | How long the caller waits, in milliseconds. The callee SHOULD give up then. |
| `from`, `to` | Only between a node and its process modules, see [modules.md](modules.md). |

- Messages are at most 1 MiB, encoded. Large data doesn't travel in messages; a transfer protocol for it is planned.
- Batches aren't used. Methods starting with `$/` are the protocol's own; receivers ignore those they don't know.

## Sessions

Calls travel over sessions: lasting WebSockets between two nodes, over which both make calls.

- The dialing node opens a WebSocket at `/v1/session` with the subprotocol `decentralized.v1`, the version of this protocol. Each text message carries one JSON-RPC message.
- Both nodes make calls over a session, whichever dialed it. Calls run concurrently and end in any order, matched by `id`.
- The caller is the node the connection proved; nothing in a message says who called. Before handling a call, the callee checks access and validates the input ([modules.md](modules.md), "Calls").
- Nodes SHOULD keep one session per peer and reuse it, including one the peer dialed. That way a node behind NAT that dialed a peer can be called back without a relay. A node whose only session with a peer goes through a relay SHOULD dial the peer directly if it has a direct address.
- Nodes ping every 30 seconds, with WebSocket pings, and close a session if the peer doesn't answer within 10. Either node closes a session that carried no calls for 5 minutes.

## Errors

An error is a JSON-RPC error whose `data.code` is one of these, or a module's own, `<module>.<code>`. Its `code` is the closest JSON-RPC one, for generic JSON-RPC clients.

| `data.code` | `code` | Meaning |
|---|---|---|
| `invalid_argument` | -32602 | The input is malformed, or doesn't match the capability's schema. |
| `unimplemented` | -32601 | No such capability. |
| `not_found` | -32000 | The thing asked for doesn't exist. |
| `permission_denied` | -32000 | The caller may not make this call, e.g. another node calling a local capability. |
| `unavailable` | -32000 | The callee can't be reached, or can't handle the call now. Callers that reach none of a node's addresses report this. |
| `deadline_exceeded` | -32000 | The call timed out. |
| `canceled` | -32000 | The caller cancelled the call. |
| `unknown` | -32000 | Anything else. |

## Relays

Nodes that accept no connections, e.g. behind NAT, stay reachable through a relay: a node configured to serve as one.

1. The node opens a WebSocket at `/v1/relay/reserve` on the relay, and pings it every 30 seconds. The relay keys the reservation by the ID the node proved, and holds it while the WebSocket lasts. The node advertises `relay/<relay host:port>/<its ID>`.
2. A caller opens a WebSocket at `/v1/relay/connect?id=<target ID>`. The relay answers 404 if it holds no reservation for the target. Otherwise it names the connection and tells the target over its reservation, in a text message: `{"connection": "<name>"}`.
3. The target opens a WebSocket at `/v1/relay/accept?connection=<name>`, which only it may do. The relay then completes the caller's WebSocket and splices the two, which carry raw bytes, in binary messages. It answers the caller 504 if the target doesn't take the connection within 10 seconds.
4. The caller runs TLS over the spliced connection, end to end with the target, and opens a session over it. The relay only sees ciphertext.

Relays don't prove their identity to callers yet. End-to-end TLS keeps a fake relay from reading or altering traffic, but not from dropping it.

## Reachability

Nodes learn which of their addresses others can reach by asking peers for dial-backs. These capabilities are built into every node (schemas: `internal/network/network.module.yaml`):

- `network.ping` answers `{}`. Reaching a node proves it's reachable at the address used.
- `network.dial_back` `{port}` → `{addr, reachable}`. The callee calls `network.ping` at the IP the call came from and the given port, expecting the caller's ID, and reports whether it answered. It MUST dial a new connection for this: reaching the caller over a session the caller dialed proves nothing. It refuses to dial back its own ID.

A node that listens asks up to 3 peers it reached at public addresses, every minute, and advertises the addresses they reached it at. Only public addresses count, unless it's configured for a private network. The IPs peers saw are its observed addresses: nodes behind the same NAT share them.

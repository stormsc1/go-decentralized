# Wire protocol

How nodes talk to each other. Version 1.

## Identity

- Every node has an Ed25519 key pair. Its ID is the SHA-256 of the 32-byte public key, as 64 lowercase hex digits.
- Nodes keep their key, and so their ID, across restarts.

## Connections

- A connection is a WebSocket ([RFC 6455](https://www.rfc-editor.org/rfc/rfc6455)), for a session or a relay. Nothing that carries it is trusted: nodes prove themselves and encrypt inside it, with the handshake under "Sessions".
- The outer layer is dressing. A node serving `wss` SHOULD present a self-signed certificate with its Ed25519 key, so its traffic looks like HTTPS to firewalls; callers MUST NOT verify the certificate, nor derive anything from it. A node behind a platform's own TLS, such as Cloud Run, serves plain HTTP behind it instead.

## Addresses

An address is the WebSocket URL a caller dials.

| Form | Example | Meaning |
|---|---|---|
| `wss://host:port` | `wss://203.0.113.10:443` | Directly; the outer TLS is unverified dressing. |
| `ws://host:port` | `ws://10.20.0.5:8080` | Directly, in the clear outside the session's own encryption. |
| `<relay address>/v1/relay/<id>` | `wss://203.0.113.10:443/v1/relay/9f2c…` | The node `<id>`, through the relay at that address. |

A node may have several addresses. Callers try them in order until one answers. Addresses never carry a node's ID, except a relayed address the target's: who answered is proved inside, so one URL, such as a platform's, may be answered by any node of a pool.

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

Calls travel over sessions: lasting WebSockets between two nodes, over which both make calls. The dialing node opens a WebSocket at `/v1/session` (or at a relayed address as it is) with the subprotocol `decentralized.v1`, the version of this protocol.

### Handshake

A session starts with a [Noise](https://noiseprotocol.org/noise.html) `Noise_XX_25519_AESGCM_SHA256` handshake inside the WebSocket, which proves both nodes' keys and encrypts everything after, end to end: platforms, proxies and relays that end or lack TLS only see ciphertext.

- The prologue is the subprotocol, `decentralized.v1`. Each handshake message travels in one binary WebSocket message. The dialer is the initiator.
- Each side proves which node holds its Noise static key with its handshake payload, in the second (responder's) and third (initiator's) messages: `{"key": <its Ed25519 public key>, "sig": <a signature>}`, as JSON with base64 bytes. `sig` signs the sender's Noise static public key, 32 bytes, for the purpose `network.noise` ([modules.md](modules.md), "Signing"). The other side MUST verify the signature against the static key the handshake proved, and derives the node's ID from `key`.
- A caller expecting a particular node MUST abort if the ID differs. The first message's payload is empty and ignored.

### Messages

- After the handshake, each binary WebSocket message carries one JSON-RPC message, encrypted with the session's transport ciphers: split into chunks of at most 65519 bytes, each sealed as a Noise transport message and prefixed with its encrypted length as a big-endian `uint16`.
- Both nodes make calls over a session, whichever dialed it. Calls run concurrently and end in any order, matched by `id`.
- The caller is the node the handshake proved; nothing in a message says who called. Before handling a call, the callee checks access and validates the input ([modules.md](modules.md), "Calls").
- Nodes SHOULD keep one session per peer and reuse it, including one the peer dialed. That way a node behind NAT that dialed a peer can be called back without a relay. A node whose only session with a peer goes through a relay SHOULD dial the peer directly if it has a direct address.
- Nodes ping every 30 seconds, with WebSocket pings, and close a session if the peer doesn't answer within 10. Either node closes a session that carried no calls for 5 minutes. A failed decryption ends the session.

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

1. The node opens a WebSocket at `/v1/relay/reserve` on the relay and runs the session handshake over it, as the initiator, which proves its ID to the relay and encrypts the relay's notices. It pings every 30 seconds; the relay holds the reservation while the WebSocket lasts. The node advertises `<relay address>/v1/relay/<its ID>`.
2. A caller dials the relayed address like any address: a WebSocket at `/v1/relay/<target ID>`. The relay answers 404 if it holds no reservation for the target. Otherwise it names the connection with an unguessable secret and tells the target over its reservation, encrypted: `{"connection": "<name>"}`.
3. The target opens a WebSocket at `/v1/relay/accept?connection=<name>`, which only it can, since only it was told the name. The relay then completes the caller's WebSocket and forwards WebSocket messages between the two, as they are. It answers the caller 504 if the target doesn't take the connection within 10 seconds.
4. The caller runs the session handshake over the forwarded messages, end to end with the target, who answers it as it would any inbound session. The relay only sees ciphertext.

Relays prove their identity to the nodes reserving on them, but not to callers, who a fake relay could still cut off; end to end, the handshake keeps it from reading or altering anything.

## Reachability

Nodes learn which of their addresses others can reach by asking peers for dial-backs. These capabilities are built into every node (schemas: `internal/network/network.module.yaml`):

- `network.ping` answers `{}`. Reaching a node proves it's reachable at the address used.
- `network.dial_back` `{port, plaintext?}` → `{addr, reachable}`. The callee builds the caller's address from the IP the call came from, the given port and the scheme (`ws://` if `plaintext`, else `wss://`), calls `network.ping` there expecting the caller's ID, and reports whether it answered. It MUST dial a new connection for this: reaching the caller over a session the caller dialed proves nothing. It refuses to dial back its own ID.

A node that listens asks up to 3 peers it reached at public addresses, every minute, and advertises the addresses they reached it at. Only public addresses count, unless it's configured for a private network. The IPs peers saw are its observed addresses: nodes behind the same NAT share them.

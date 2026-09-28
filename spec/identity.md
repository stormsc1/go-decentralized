# Identity

How people, organisations and other entities are identified, and how they sign. Version 1.

## DIDs

People, organisations and entities such as projects have [DIDs](https://www.w3.org/TR/did-core/). Nodes don't: they have node IDs ([wire.md](wire.md), "Identity").

The only method so far is [`did:key`](https://w3c-ccg.github.io/did-method-key/), with Ed25519 keys:

```
did:key:z<base58btc(0xed 0x01, the 32-byte public key)>
```

`0xed 0x01` is the multicodec `ed25519-pub`, as a varint, and base58btc uses Bitcoin's alphabet. These DIDs start with `did:key:z6Mk`. The key from 32 zero bytes of seed is `did:key:z6MkiTBz1ymuepAQ4HEHYSF1H8quG5GLVVQR3djdX3mDooWp`.

## Signed data

Data an identity signs travels as:

```json
{"data": "<base64>", "signer": "<did>", "sig": "<base64>", "delegation": {...}}
```

- `sig` is the signer's signature of `data` for a purpose, as in [modules.md](modules.md), "Signing". The purpose isn't carried: the verifier knows which it expects, such as `chat.event`.
- Signatures cover `data` as carried, so verifying never re-encodes it.
- `delegation` is only there if the signer is a device, see below.

## Devices

A person's root key signs for their devices, which sign with keys of their own. A device isn't necessarily a node.

- A delegation is signed data, for the purpose `did.delegation`, whose data is `{"device": "<the device's DID>", "expires": "<time>", "scope": "<scope>"}`. The root signs it. `scope` is what the device may sign for: a purpose, or a module name covering all its purposes (`chat` covers `chat.event` and `chat.user`); `*`, or none, covers any.
- Data a device signs for the root carries the delegation in `delegation`, and the device as `signer`.
- Verifiers check the delegation's signature, that it names the signer, that it covers the purpose, and that it hadn't expired when the data was signed, as far as they know: for instance, when they first received it. The data is then the root's.
- Delegations don't nest: devices can't delegate.
- Delegations can't be revoked yet, so their expiry should be short.

## Custody

A person without a device that holds their root keeps it in a vault: a node running the `vault` module ([modules/vault/module.yaml](../modules/vault/module.yaml)). The vault stores and serves blobs it can't read, and signs nothing. The client derives everything from a 32-byte secret only the person can produce, either a passkey's PRF output for the salt `go-decentralized vault` or PBKDF2-SHA-256 of a passphrase, 600 000 iterations, with the salt `go-decentralized vault:<handle, trimmed and lower-cased>`:

```
wrap  = HKDF-SHA-256(secret, salt "go-decentralized vault", info "wrap"), 256 bits, an AES-GCM key
proof = HKDF-SHA-256(secret, salt "go-decentralized vault", info "proof"), 32 bytes
```

- `vault.open {proof}` returns the account the proof names, the SHA-256 of the proof: its root's DID, its blob and the node it's on. The proof grants reading only.
- `vault.save {signed}` creates the account or replaces its blob. `signed` is `{proof, blob}` signed by the root itself, not a device, for the purpose `vault.save`; only the root an account was created by may replace its blob.
- Both take `vault`, a node ID: the node called forwards to the vault there, so an app reaches a person's vault through whatever node serves it.
- The blob, version 1, is `{"v": 1, "pub": "<base64 raw public key>", "iv": "<base64, 12 bytes>", "ct": "<base64>"}`: `ct` is the root's PKCS#8 private key under AES-256-GCM with `wrap` and `iv`.

The client unwraps the root in memory only to sign a delegation to the device, then forgets it.

## Profiles

A person's profile — name, avatar, bio — is signed data for the purpose `profile.set`, whose data is `{"name": "…", "bio": "…", "avatar": "<sha-256 of the image, hex>", "time": "<when signed>"}`; `bio` and `avatar` are optional. The node the person gives it to (`profile.set`, [modules/profile/module.yaml](../modules/profile/module.yaml)) is their home: it keeps the newest, announces the DID as a key in the DHT ([routing.md](routing.md), "Joining and announcing"), and serves the profile and the avatar's bytes to other nodes, which check the signature and the hash. A DID thus resolves to its home nodes with `routing.find_providers {key: <DID>}`. See docs/design/profiles.md.

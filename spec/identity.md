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

- A delegation is signed data, for the purpose `did.delegation`, whose data is `{"device": "<the device's DID>", "expires": "<time>"}`. The root signs it.
- Data a device signs for the root carries the delegation in `delegation`, and the device as `signer`.
- Verifiers check the delegation's signature, that it names the signer, and that it hadn't expired when the data was signed, as far as they know: for instance, when they first received it. The data is then the root's.
- Delegations don't nest: devices can't delegate.
- Delegations can't be revoked yet, so their expiry should be short.

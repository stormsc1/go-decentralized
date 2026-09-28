# Identity and auth: roots, app identities, credentials

Status: proposal for review, 2026-09-28, from notes with the system architect; steps 1 and 2 of the order of work are built. **Open** marks what's still to decide.

## The architect's points

1. Each system needs its own internal idea of the user: the chat's sender is a *chat identity*, not the root. There may be a cryptographic way to do this.
2. Later, a root identity can be backed by a national or EU digital ID (MitID, the EUDI wallet), and a service that needs an attribute — age, address, nationality — asks for it with a **presentation request**.
3. People should be able to link OAuth accounts. Whether the platform ever stores passwords or auth data is open; perhaps it's a credential, perhaps both, so that logging in with a credential is a choice.

## Precedents

**One root, many working keys.** Every mature system separated the key that *is* you from the keys that *act* for you:
- **Farcaster:** a custody key (root) authorizes app keys ("signers") with a signed request; apps sign messages with their own key, and hubs check the key was authorized. Revoking an app revokes one key.
- **Nostr NIP-26:** the root delegates event signing to an app key with conditions and an expiry; NIP-46 keeps the root in a "bunker" so apps never see it.
- **Matrix cross-signing:** a master key signs a self-signing key, which signs device keys; the master key can live offline.
- **AT Protocol (Bluesky):** rotation keys control the DID; a separate signing key signs content.
- **Passkeys (WebAuthn):** the key is device-bound and synced by the OS vendor; the person never handles it. The PRF extension can derive a secret from a passkey, which is how one wraps a root key without a password.

**Identities per relationship.** Hyperledger Indy and DIDComm use a **pairwise DID** per relationship, so no party can correlate a person across relationships. The chat identity the architect describes is this, per app rather than per peer: what the chat's other members see is a key that only the person's own node can tie to a root.

**Presentation requests.** The EU wallet (EUDI ARF) uses **OpenID for Verifiable Presentations (OpenID4VP)**: the service sends an authorization request holding a *presentation definition* (DIF Presentation Exchange), the wallet asks the person, and answers with a presentation. Credentials are **SD-JWT VC** or **mdoc** (ISO 18013-5, the driving-licence format), both with **selective disclosure**: prove "over 18" without revealing the birthdate. The W3C **Verifiable Credentials** data model is the general form; **BBS+ / AnonCreds** are the cryptography for presentations that can't even be linked to each other.

**OAuth in a DID world.** An OAuth login becomes a *credential*: a verifier checks the Google or email account and issues "this DID controls account X", bound to the root. Nothing stores a password; OAuth never yields one. Using OAuth to *get back in* from a new device is a different thing, custody, and the custodial wallets (Web3Auth, Privy, Magic) do it with threshold key shares, at the price of a service you must trust.

## Decided (2026-09-28)

- **One identity across services, keys per device, one profile per person** — the model below — with a caveat: service nodes may be run by third parties, such as a project on another company's network, and whether identities should stay correlatable there is an open question for the system architect, see [questions.md](questions.md). The code takes the correlatable default until then.
- **Sign-in is the vault with passkeys**, from day one: a root wrapped under a passkey-derived key, stored in a vault that can't read it, unwrapped in the browser only to authorize a device key.
- **Credentials and presentations are OpenID4VP with SD-JWT VC**, EUDI-compatible, also for what we issue ourselves.
- **OAuth accounts and passwords:** open, see [questions.md](questions.md).

## The model: one product, many services

The platform is one product with many services (chat, task tracking, …), so people must be the same person in all of them. Hiding the person behind per-service identities protects against *other parties* correlating them; inside the product it only gets in the way. So:

| | What it is | Where it lives | Who sees it |
|---|---|---|---|
| **Person** | A key: the person's DID, the same in every service. Rarely used itself. | The vault (wrapped), or a phone later | Every service and everyone in them: it's how Alice in chat is Alice in tasks |
| **Device key** | A key per device, authorized by the person's key, signing what the person does on that device in any service | The device (IndexedDB now) | Nodes, to verify; people see the person |
| **Profile** | Name, avatar, bio: the same in every service, with fields a service may add (status in chat, a role in tasks) | Their home node, which serves it | Everyone who shares something with them |
| **Service record** | A service's own idea of the person: the chat user, the tasks user, with that service's state, keyed by the person's DID | The service's store | The service |
| **Credentials** | Facts about the person, signed by others: over 18, employee of X, verified email | Their wallet or vault | Whoever they're presented to |

Events are signed by device keys and carry the authorization; nodes verify the key was the person's, and show the person. A profile may carry a credential when the person chooses — "name verified by MitID". Per-service or per-organisation identities, for unlinkability towards other companies, are the same mechanism with one more key, as an opt-in later.

**Authorizing a device.** A new device makes a key and asks a *root holder* — the vault, or a phone later — to authorize it for a scope (all services by default) until a time. With the vault: the person signs in with their passkey, the browser derives the wrapping key from it (WebAuthn PRF; a passphrase where PRF isn't available), fetches the wrapped root from the vault, unwraps it in memory, signs the authorization and forgets the root. The vault only ever stores and serves blobs. Because passkeys sync through Apple and Google, a new laptop needs no phone app; one can come later for people who want to hold their own root, behind the same request.

An **authorization** is `did.Signed` with the purpose `did.delegation`, its grant `{key, expires, scope: "chat"}` — our existing delegation with a scope. `Verify` accepts an app key only for purposes under its scope (`chat.*`), so a chat key can't sign for scheduling.

**Privacy by default.** Other members see only the app identity. The link to the root — the authorization — is shown to whoever needs it: the person's own node at registration (so an organisation can later say "this chat identity is one of our people"), and to anyone the person chooses to prove it to. Nothing in an event ties it to the root. Per-organisation chat identities, for unlinkability across organisations too, are the same mechanism with one more key; not for v1.

**Sessions with the home node** (item 2 of the chat's list). The browser proves it holds the app key: the node sends a challenge, the browser signs it, the node sets a session. Capabilities then know who's asking, and `user` parameters go away; reads are the person's own channels. This is also the answer to "browser ↔ home node" in the chat design.

**Presentation requests.** A module that needs a claim asks the platform, not the person's device: `identity.request {presentation_definition}` on the node, which routes it to the person's wallet — an EUDI wallet by OpenID4VP over a QR code or deep link, or our own vault later — and returns the presentation. The node verifies issuer signatures and selective disclosure and gives the module the *claims*, so modules never parse SD-JWT or mdoc. A module declares in `module.yaml` which claims it may ask for, as it declares stores. The claim's answer can be kept as a credential bound to the app identity ("over 18, verified by issuer Y on date Z"), so it's asked once.

**OAuth, both ways, no passwords.**
- *As a credential:* a verifier — the organisation's node, or a platform service — does the OpenID Connect dance and issues "verified email" or "controls Google account" bound to the root. Stored like any credential. The platform stores no passwords; provider tokens only if a module needs the provider's API, and then in the person's vault, encrypted.
- *As a way back in:* custody of the root, opt-in. The recommended form is **passkey custody**: the root key wrapped with a secret derived from a passkey (WebAuthn PRF), the wrapped blob kept on the home node. A new device with the same passkey (synced by Apple or Google) unwraps it. No password, no custodian holding a usable key. Social recovery (M of N people) and vault services are further options. A person who wants none of it keeps the root on one device and backs it up.

**What changes in the chat.** The browser makes a device key and gets it authorized by the person's root (through the vault). Registration and every event are signed by the device key and carry the authorization; the chat's `author` becomes the *person's* DID, which `did.Signed.Verify` already returns for a delegated signature, so the same person shows up the same from every device. The chat's user record stays the chat's own, keyed by the person. Profiles move to the person and gain an avatar.

**Root holders.** Whatever holds a root answers one kind of request: "authorize this device key for this scope until this time". The vault does it after a passkey sign-in, in the person's browser; a phone would do it by scanning a QR; the CLI can do it for development, holding a root in the node's data directory. The protocol between a device and a root holder is the same in all three.

## Order of work

1. **Device keys** (built 2026-09-29): the scoped authorization in `did/`, the chat signing with device keys and showing the person, the session login on the node (`node.login`, see spec/modules.md, "Signing in") and capabilities knowing who they're for (`module.User`), the CLI as the first root holder (`identity authorize`), and the web app asking to be authorized. The request a device makes still travels by copy and paste; the vault replaces that.
2. **The vault service** (built 2026-09-29), as a module: `vault.open {proof}` and `vault.save {signed}`, see spec/identity.md, "Custody". The browser (web/src/identity.ts) derives the wrapping key and the proof from the passkey's PRF, or from a passphrase where there's no PRF, and the root is unwrapped only to sign the device's authorization; the vault knows the root's DID and the blob, nothing else. Replacing a blob takes the root's own signature, so a proof, or a device, can't lock the person out. Not yet: the vault is the one on the node serving the app, so an identity made on one node's vault can't sign in through another's (the app could ask which vault, and the node forward to it); re-wrapping when the passkey changes; recovery when it's lost; and rate limits against guessing passphrases online.
3. **Profiles** at the person level, with avatars, served by home nodes.
4. **Credentials:** the node's `identity.request` with OpenID4VP and SD-JWT VC verification; the first verifiers, email and OAuth accounts, issuing credentials; an EUDI wallet as the first external wallet.
5. **A phone app** as a root holder, and **per-organisation identities** with BBS+ presentations, if either is wanted.

## Open

- **Rotation:** `did:key` can't change keys. A root that can rotate needs another method — `did:web` for organisations, `did:plc`- or `did:webvh`-style for people — before roots matter much.
- **Revocation** of app keys and credentials: status lists the root signs, and where nodes find them.
- **Which claims may a module ask for,** and does the person's consent live in the wallet only, or also in the node.
- **Organisations as issuers:** an organisation's DID issuing "employee of" to its people, and the chat showing it.

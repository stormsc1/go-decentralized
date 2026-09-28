# Design questions

Open questions the code takes a position on for now. Each names the current default, what depends on it, and what changing it would cost, so they can be taken to the system architect and revised. Decisions move to the design doc they belong to.

## Identity

### Should identities be correlatable across services?

**Context.** The platform is one product with many services (chat, task tracking, …), and people must be the same person across them. But services aren't all run by us: a third party may run a service node, such as a project one works for on another company's network. The identity design ([identity-auth.md](identity-auth.md)) is built for the first case: one DID per person, shown as-is in every service.

**Current default.** Correlatable: a person's DID is the same everywhere, and every service and node they touch learns it. Only credentials (over 18, employee of X) are shown selectively.

**The alternative.** A different identity per service, or per organisation one deals with, tied to the person only by an authorization they hold and can present. Other parties then can't correlate someone across services or companies unless they choose. The mechanism is one more key and the same authorization; profiles would exist per identity, and "linking two users in chat and task tracking" would need the person to present the link.

**What changing it would cost.** The chat's user model (`author` is the person's DID) and profiles per person would become per identity; nothing in the transport, stores or signatures changes. Cheap now, expensive once data references DIDs across services.

**Asked of.** The system architect, 2026-09-28.

### Should the platform store OAuth tokens or passwords?

**Current default.** Neither: an OAuth login becomes a credential (verified email, controls account X) bound to the person, issued by a verifier; signing in from a new device is passkey custody through the vault. Provider tokens only if a module needs the provider's API, in the person's vault, encrypted.

**The alternative.** A custodial sign-in (email and password, or social login) holding usable keys for the person, for easier onboarding and support-driven recovery, at the price of a party that can act as anyone.

### How does a root identity rotate its key?

**Current default.** `did:key`: the identity *is* the key, and can't change it. A compromised or lost root is a new person.

**The alternative.** A DID method with a document and rotation keys (`did:web` for organisations; `did:plc`- or `did:webvh`-style for people), or binding the root to a national ID so it can be re-established. Needed before roots carry much.

## Chat

### May an organisation's node read its people's channels?

**Context.** Every member's node keeps a full copy of each channel, partly for legal reasons. Today anyone reaching a node's API can read any channel it holds; sessions will scope reads to the person.

**Current default.** Undecided in code: reads will be the person's own once sessions exist. Whether the organisation itself (its node's operator) may read, and under what policy, isn't decided.

### Deleting messages

See [TODO.md](../../TODO.md), "Decision: deleting chat messages". Current default: no deletes.

### Do invites need consent?

**Current default.** Anyone in a channel adds anyone, outright; the invitee's node copies the channel. The event schema has a `join` kind, unused.

**The alternative.** An invite is an offer the invitee accepts with a `join` event, before their node copies anything. This is the spam question from the chat design.

## Nodes and pools

### How does a pool member prove it acts for its organisation?

**Current default.** Not at all: pool members are nodes sharing stores, and nothing says which organisation a node speaks for. See [stores-pools-transport.md](stores-pools-transport.md).

### How are a person's nodes found from their DID?

**Current default.** Invites carry `did@node`. A signed record in the DHT, like a node's routing record, would make the address just the DID.

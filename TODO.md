# TODO

## Callers don't verify relays
A node reserving on a relay sees the relay's ID in their handshake, but
nothing says which ID to expect, and callers never see it: a caller can't
tell a relay from an impostor at its address. The end-to-end handshake keeps
an impostor from reading or altering traffic, but it could drop it.

Fix: expected relay IDs in the relay config and in relayed addresses.

## Streams, and events between modules
Messages carry calls, their ends and calls without one; events only reach
local tools. Progress and logs need streamed results, with flow control per
call on process pipes, and modules may want each other's events. Large files
need a transfer protocol of their own: blobs travel base64 in one call, so
`store.blob_*` and avatars stop at 512 KiB and 256 KiB.

## Profiles' loose ends
Avatars nobody names any more and copies of strangers are never cleaned up.
A home node announces every person's DID in the DHT every 10 minutes: with
thousands of people that needs batching. A profile could name its home
nodes, signed, so an old home can't serve a stale one. See
docs/design/profiles.md.

## Revoking devices
A person's root key delegates to device keys, which can't be revoked: a lost
device acts for its person until its delegation expires. Fix: revocation
lists the root signs, and a way to find them.

## Who may call what
Any node may call any network capability, e.g. `debug.traces`, which shows
who talks to whom. Process modules may call any of their node's
capabilities, and the local API has no authentication.

Fix: per-node grants for network capabilities, a list of what each module
may call, and tokens for the local API.

## Conformance tests
The spec has no test suite, so another implementation can't check itself
against it. Fix: a runner that drives any node over WebSocket and any process
module over stdio, with golden messages and signatures.

## Decision: deleting chat messages
Every member's node keeps a copy of each channel it's in, so deleting can't
reach other organisations' copies. For now, messages can't be deleted.
Options: hide everywhere and let each org's retention policy decide what it
keeps, ask every node to erase (not enforceable), or hide only. See
docs/design/identity-storage-chat.md.

## The vault's gaps
Re-wrapping when a passkey changes, recovery when it's lost, and rate
limits on `vault.open` against guessing passphrases online. A person also
has to know their vault's node ID to sign in through another node's app;
finding it from the person, or from the passkey, would be kinder.

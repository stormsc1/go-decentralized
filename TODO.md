# TODO

## Relays aren't verified
A relay address names the node behind the relay, not the relay, so a caller
can't tell a relay from an impostor at its address. End-to-end TLS keeps an
impostor from reading or altering traffic, but it could drop it.

Fix: put the relay's ID in relay addresses, and verify it too.

## Streams and events
Messages only carry calls and their ends. Progress, logs and events between
modules need `item` and `event` messages, with flow control per call on
process pipes. Large files need a transfer protocol of their own.

## Who may call what
Any node may call any network capability, e.g. `debug.traces`, which shows
who talks to whom. Process modules may call any of their node's
capabilities, and the local API has no authentication.

Fix: per-node grants for network capabilities, a list of what each module
may call, and tokens for the local API.

## Conformance tests
The spec has no test suite, so another implementation can't check itself
against it. Fix: a runner that drives any node over TLS and any process
module over stdio, with golden messages and signatures.

## Decision: deleting chat messages
Every member's node keeps a copy of each channel it's in, so deleting can't
reach other organisations' copies. For now, messages can't be deleted.
Options: hide everywhere and let each org's retention policy decide what it
keeps, ask every node to erase (not enforceable), or hide only. See
docs/design/identity-storage-chat.md.

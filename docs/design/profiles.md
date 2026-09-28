# Profiles

Status: built 2026-09-29, step 3 of [identity-auth.md](identity-auth.md). Decisions from the questions asked before building are marked **Decided**.

A profile is what people see of a person in every service: a name, an avatar and a bio. It belongs to the person, not to a service: the chat, task tracking and whatever comes next show the same one. Module: `modules/profile`.

## Decided (2026-09-29)

- **Avatars in a blob store**, the third store kind (`blob`), through the Go CDK (`gocloud.dev/blob`): the `file` driver now, S3 and the like later on the same package. Attachments will use it too.
- **Anyone may read a profile** from its home node. Name, avatar and bio are what you'd show anyone; unlinkability towards other organisations stays an open question ([questions.md](questions.md)) and would be another key anyway.
- **Apps ask their own node's profile module**, which serves the people at home there, keeps copies of others and fetches from their home nodes. Services stay independent: the chat stopped storing names, its user record is DID and node.
- **A DID resolves through the DHT** to the person's home node, the way a capability resolves to its providers: the home node announces the DID as a key (`routing.provide`). A hint of where the person is, such as a chat membership's node, is tried first.

## How it works

- The person signs `{name, bio, avatar, time}` with their device key, purpose `profile.set` ([spec/identity.md](../../spec/identity.md), "Profiles"), and gives it to the node they're signed in to with `profile.set`. That node is their **home**: it keeps the profile, announces their DID in the DHT and serves the profile to any node with `profile.fetch`. A newer `time` replaces an older; an older is refused.
- Avatars are images up to 256 KiB, kept in the home node's blob store under the SHA-256 of their bytes (`profile.set_avatar`), and named by that key in the profile. Other nodes fetch them with `profile.fetch_avatar`, check the hash, and keep them.
- `profile.get {id, node?}` on any node: their own if they're the home; else a copy less than `fresh` old (10 minutes by default); else fetched from the home, found through the hint, the node the copy came from, or `routing.find_providers {key: <DID>}`. The signature is checked, so a node can't forge a profile, only serve a stale one. A stale copy is served if nobody answers. `lookup` does this for many at once.
- `profile.updated {id}` tells apps on the node when a profile changed, set here or fetched anew.

The chat web app asks for the profiles of the people in its channels, with their nodes as hints, shows avatars beside messages, and has the person fill in their profile the first time.

## Not yet

- Fields a service adds (status in chat, a role in tasks), and a profile's visibility beyond public.
- Garbage: avatars nobody names any more, copies of people no longer shared with.
- A home node with many people announces each DID every 10 minutes: batched announces, or a per-node index, before that's thousands.
- Moving home: the profile could name its home nodes, signed, so a replay from an old home is caught, and a person could have several.
- Blobs bigger than one message: streams, for attachments.

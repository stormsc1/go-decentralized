# go-decentralized

```sh
tilt up                               # network lab + explorer, with hot reload (or F5 in VS Code)
docker compose run --build --rm cli   # CLI on the lab's home network
> debug.map_network
> greeter.greet id=<ID of gl or kg>
```

Network explorer: http://localhost:5173 · Tilt: http://localhost:10350

Nodes and modules follow a language-neutral contract: [spec/](spec/README.md). Modules can run compiled into a node, or in a process of their own in any language, e.g. [the TypeScript greeter](examples/greeter-ts/greeter.ts).

Goals:
 - Provide a modular framework for building decentralized ecosystems.
 - Every user of the ecosystem needs to be able to run their own nodes, and use them without ANY central authority. For one, this means we cannot have a single registry for nodes; each node must be discoverable in a decentralized manner.
 - P2P communication should be possible, even for nodes behind NATs, using techniques like relays and hole punching.
 - Distributed storage and computation should be supported, and structures should be in place to ensure data integrity and availability across the network.

## Problems
Q: How does a node enter the network without relying on a central authority?
A: We have multiple mechanisms for node discovery:
 - Ship a default list of well-known public nodes, which anyone can edit or replace in their node definition.
 - Save every peer you've seen to disk. After the first run, a node reconnects to peers it already knows and doesn't need the bootstrap list.
 - mDNS finds peers on the same LAN with no configuration, so a studio's internal nodes find each other even with no internet.
 - Direct connections: nodes can connect to each other directly if they know each other's addresses, bypassing the need for discovery mechanisms.

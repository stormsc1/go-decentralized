# Discovery Module

Finds nodes without a central authority, using a [Kademlia](https://www.scs.stanford.edu/~dm/home/papers/kpos.pdf) DHT (as in BitTorrent and IPFS).

Why Kademlia? Because it's highly fault-tolerant and lookups are only O(log n) of n nodes, making it efficient for large networks.

Protocol, for other implementations: [spec/modules/discovery.md](../../spec/modules/discovery.md).

## First Contact

New node needs one existing node to join. To get this first contact, it can use a bootstrap list of known nodes or rely on mDNS for local network discovery.

## NAT Traversal

Nodes behind NATs (Network Address Translators) can't be reached directly from the public internet. Almost all nodes in typical corporate or home networks are behind NATs. This will be solved with IPv6, but for now, NAT traversal techniques are necessary.

Relays are the bulletproof solution that always works, as they allow nodes behind NATs to communicate with each other through an intermediary node that has a public IP address. This is an awesome fallback when direct peer-to-peer connections are not possible.

However, relays introduce additional latency and reliance on intermediary nodes, so direct peer-to-peer connections are preferred whenever possible. For this we need hole punching techniques to establish direct connections between nodes behind NATs. This is not implemented yet, but it is the intended approach for future development- and requires a public relay server to facilitate the initial connection. (Maybe QUIC-based)

## Security Considerations
### Sybil/eclipse attacks (flooding)
Plain Kademlia is vulnerable to these. Nothing is implemented yet.

## References
 * [Kademlia paper](https://www.scs.stanford.edu/~dm/home/papers/kpos.pdf): Maymounkov & Mazières, 2002.
 * *S/Kademlia: A Practicable Approach Towards Secure Key-Based Routing*: Baumgart & Mies, 2007.
 * [BEP 5](https://www.bittorrent.org/beps/bep_0005.html): BitTorrent Mainline DHT spec.
 * [anacrolix/dht](https://github.com/anacrolix/dht): Go Mainline DHT. Candidate for global first contact.
 * [go-ethereum p2p/discover](https://github.com/ethereum/go-ethereum/tree/master/p2p/discover) and [discv5 spec](https://github.com/ethereum/devp2p/blob/master/discv5/discv5.md): Kademlia discovery with signed node records.
 * [go-libp2p-kad-dht](https://github.com/libp2p/go-libp2p-kad-dht): libp2p's Kademlia (reference only, not used).

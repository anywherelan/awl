# Public Relay Policy

awl uses public relays as a fallback when direct peer-to-peer connections
cannot be established (for example, when hole punching fails). This document
describes the policy for third-party volunteer public relays: what we expect
from operators, how relays are trusted, and how users can report misbehaving
relays.

This policy is complementary to the ability to use your own trusted and
reachable peers as relays. If you can run your own relay, prefer that over a
public one.

## Threat model

End-to-end encryption protects the content of traffic from relays. A relay
can still observe metadata: the IP addresses of the peers it connects and the
timing and volume of the traffic it forwards. The same metadata is visible to
ISPs, and to anyone on the path of a direct peer-to-peer connection.

Relays are therefore treated as untrusted for confidentiality, but they are
trusted to forward traffic correctly and to not actively attack the network.

## Privacy policy

Relay operators must:

- Not record, log, or retain connection logs, IP addresses, or traffic
  metadata.
- Not inspect, modify, or block the content of relayed traffic.
- Publish a privacy policy describing what, if anything, they collect.

## Technical enforcement

Because a malicious relay can scrape the IP addresses of the clients it
relays, awl limits the exposure of any single relay:

- Rate limits on the number of connections and clients a relay may serve.
- Limits on how many clients and IP addresses a relay can observe.

## Operator tiers

Trust is not binary. Relays are assigned to tiers:

- New volunteer relays start in a low tier and are rate limited.
- Relays earn trust over time and move to higher tiers with higher limits.
- Relays that misbehave are demoted or removed.

## Reporting misbehaving relays

Users can report a misbehaving relay to the maintainers. Reports should
include the relay's peer ID and a description of the problem. Reported relays
are reviewed and may be demoted or removed from the list.

## Curation of the relay list

The relay list is curated by the maintainers. This adds a degree of
centralization, but the list is already centralized today: bootstrap relay
nodes are hard coded, and public relays are only a fallback. Anyone can host
a public relay and share it directly, without being on the curated list.
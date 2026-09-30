# Preview networks cannot reach the host

Date: 2026-09-30

Exact base: `2848ad9` (PR #65)

## What was found

While planning Repo C's A5.2 (a Shopify app's allow-listed way out to Shopify), a trial on the live host showed that a
service on a preview's network could open TCP connections to the host.

- The network is `--internal`: no internet, no default route. That part held.
- But the host has an address on every Docker bridge, the network's gateway, and it answers there. From a gVisor
  container, these host ports accepted a connection through the gateway: 22, 443, 8001–8003, 8101, 8444, 8445, 8766,
  18081 and 18082. The same held under Docker's default runtime.
- The probe proved only the absence of the internet (a request to `1.1.1.1`), never the host.

A Node app's server code is written by the builder from a user's description. Repo C had just opened such apps to everyone
signed in (its P0175). It closed them again to administrators at 14:11Z, and no app was built while they were open.

## What was tried

- **Docker's isolated gateway mode** for internal networks (`com.docker.network.bridge.gateway_mode_ipv4=isolated`,
  Docker 29.7). The bridge gets no address, so the service reaches no host address. But the host no longer reaches the
  service either, and ABCP's presentation connects to it from the host. Not usable.
- **A rule on the host** (owner, 2026-09-30, systemd unit `abcp-preview-isolation`, active 14:18:35Z):
  `iptables -I INPUT -s 10.213.0.0/16 -m conntrack --ctstate NEW -j DROP`. New connections from that range to the host
  are dropped; replies to the host's own connections still pass. A trial network in the range, under runc and runsc:
  - host to service: answered;
  - service to host, through the gateway, the default bridge or the public address, on every port above: none opened.

## What changes

- **Every preview network is one /28 of `10.213.0.0/16`.** `docker network create --subnet` asks for the group's first
  candidate, a place given by its id, and tries the next when Docker refuses an overlap: 64 at most, then the start fails.
- **The inspection checks the range everywhere it looks at a network:**
  - the network has exactly one address range, a /28 on its boundary inside `10.213.0.0/16`, with its gateway inside;
  - each service's address is in the range;
  - under gVisor, hosts entries point into it.
- **The probe proves the host out of reach.** ABCP opens a listener on the network's gateway for the probe. Each service
  asks it for a page (`busybox wget -T 2`). Any answer, or any connection the listener sees, and the profile is not proved.
- A host without the rule therefore leaves every profile unavailable rather than exposed.

## Tests

`internal/preview/host_isolation_linux_test.go`:
- the candidate /28s cover the range once, each on its boundary;
- a taken subnet moves the network to the next one, and a full range refuses the start;
- the inspection refuses:
  - Docker's default pool, a wider or misaligned range, a gateway elsewhere, no range or two ranges;
  - a network that is no longer internal or is another owner's;
  - a service address and a hosts entry outside the range;
- the probe proves a profile whose services cannot reach the host, and refuses one whose services can.

The fixture now records the subnet the controller asks for and reports it back as Docker does. Its services reach the
probe's listener only when a test says the host rule is missing. Existing tests use addresses in the range.

## Rollout

Build from the merge commit, announce on Repo C's board #93, and restart. Check that:
- every profile is proved;
- a Node preview and the hosted Team Kudos come up in `10.213.0.0/16` and still cannot reach the host.

Then Repo C opens web apps with a server to everyone again.

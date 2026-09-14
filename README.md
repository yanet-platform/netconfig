# Netconfig

> **Note:** This project is currently in active development.

Netconfig is a standalone Linux interface configurator for YANET deployments.
It configures KNI, VLAN, loopback and dummy interfaces in the network namespace
where it runs.

The implementation is extracted from the
[YANET2 netlink dataplane sidecar](https://github.com/yanet-platform/yanet2/pull/2628).
It is an independent Go module: the executable does not depend on YANET2,
DPDK, a gateway, the neighbour API, Netplan executables or a service manager.

## Lifecycle and ownership

- The dataplane creates `kni[0-9]+`; the kernel supplies `lo`. Netconfig waits for
  them and creates only explicitly configured VLAN and dummy interfaces.
- Configuration is read and validated once, before opening netlink sockets.
  Missing or malformed files fail startup. File edits, replacements and deletion
  have no effect on a running instance, including its pending retries.
- Every setup pass attempts available links even when another link is missing or
  fails. Partial progress is retained. Both phases retry with exponential backoff
  until all configured links succeed, including IPv6 duplicate-address detection.
- After success, netconfig closes its netlink handle and waits for SIGTERM/SIGINT
  without further mutations. `-once` exits successfully instead; `-check` only
  validates the selected source and needs no network privileges.
- Shutdown never tears down interfaces or flushes addresses. Restart reconciles
  observed kernel state, reuses compatible links and preserves unrelated addresses.
  An incompatible type, VLAN parent, ID or protocol is an error, not a migration.
- There is no continuous drift repair. Restart netconfig to apply a new config or
  restore a managed interface deleted after successful bootstrap.

Netconfig is the only **interface configurator** in its network namespace.
Concurrent address/MTU changes by another configurator are unsupported. Dataplane
creation of KNI is expected. Routing and neighbour discovery belong to separate
processes: netconfig does not program Linux routes/neighbours or start DHCP clients.
Kernel connected/local/RA routes and ordinary ARP/NDP are normal side effects of
interface configuration.

## Configuration

The CLI defaults to `/etc/netconfig/config.yaml`. The file selects exactly one
source; all paths are resolved in the process filesystem, relative to its working
directory when not absolute. See [Netplan](examples/netplan.yaml) and
[native](examples/native.yaml) examples.

```yaml
source: netplan
netplan_path: /etc/netplan/00-interfaces.yaml
retry:
  initial_backoff: 250ms
  max_backoff: 30s
```

`source` is required: `netplan` or `native`. For Netplan, an omitted path uses
`/etc/netplan/00-interfaces.yaml`; an explicitly empty path is rejected. Native
mode requires a `native` mapping and forbids `netplan_path` entirely. Retry defaults
are 250ms and 30s; both must be positive, with the initial delay no greater than
the maximum. Unknown root keys and multiple YAML documents are rejected.

Native input is embedded directly, without a `network` wrapper:

```yaml
source: native
native:
  ethernets:
    kni0: {mtu: 9000, link-local: [], accept-ra: false}
    lo: {addresses: [192.0.2.241/32]}
  vlans:
    kni0.100:
      link: kni0
      id: 100
      mtu: 9000
      addresses: [192.0.2.2/24, '2001:db8:100::2/64', 'fe80::f1/64']
      link-local: []
      accept-ra: false
  dummy-devices:
    dummy0: {addresses: [198.51.100.1/32], link-local: []}
```

### Interface subset

| Section or field | Contract |
| --- | --- |
| `ethernets` | Only declared `kni[0-9]+` and existing `lo` are managed; neither is created. |
| `vlans` | Directly on a declared KNI; required `id` (0..4094) and `link` (parent name). |
| `dummy-devices` | Explicitly declared dummy interfaces. Reserved base-interface names are rejected. |
| `addresses` | IPv4/IPv6 prefix strings. An omitted or empty list does not remove other addresses. Unspecified addresses and IPv4-mapped IPv6 prefixes are rejected. |
| `mtu` | Omitted or 0 preserves existing MTU; otherwise 1280..2147483647, subject to kernel and parent limits. |
| `link-local` | Omitted enables automatic IPv6LL. `[]` disables generation; `[ipv6]` enables it. IPv6 and NDP stay enabled. |
| `accept-ra` | Optional boolean; omission leaves kernel policy untouched. False writes 0, true writes 2 to permit RA with forwarding enabled. |
| `dhcp4`, `dhcp6` | Optional booleans, false-only. Existing DHCP-assigned addresses are not removed. |

MTU increases on a parent precede child changes; parent decreases follow them.
A new VLAN inherits the configured parent MTU, or the observed parent MTU if
unspecified. Oversized existing children, including unmanaged children, block
parent decreases instead of being silently clamped. Existing interfaces with an
unspecified MTU keep their current value.

With automatic IPv6LL disabled, unlisted IPv6 link-local addresses are removed
only from managed non-loopback links, after explicit addresses are ensured.
Explicitly listed link-local addresses are retained. A configured IPv6 address
with failed DAD is removed and re-added for retry; tentative addresses prevent
bootstrap completion. A conflicting IPv6 prefix length is rejected.

Native input strictly rejects unknown sections/fields, duplicate keys, null
values and incorrect YAML types. Numeric fields require unquoted decimal integers;
avoid leading zeros. There are no routes, policy rules, bridges, bonds, tunnels,
`renderer`, `version`, arbitrary Ethernet names or administrative-state fields.

The Netplan adapter reads a single version-2 document and preserves the extracted
parser's compatibility rules, including aliases and decimal strings for MTU/VLAN
IDs. Unrelated Ethernet interfaces, VLANs on them and known unrelated network
sections are ignored. `routes` and `routing-policy` are ignored on managed links.
Unsupported settings on managed interfaces fail validation. This is a parser for
the documented subset, not an implementation of all Netplan features.

## Build and run

Build with Go 1.24.13 or newer on Linux; CI and container builds use Go 1.26.2:

```bash
go build -trimpath -o build/netconfig ./cmd/netconfig
./build/netconfig -config examples/native.yaml -check
```

The multi-stage image has an Alpine 3.23 runtime and a statically linked binary:

```bash
docker build -t netconfig:local .
docker run --rm --network none --cap-drop ALL --read-only \
  --mount type=bind,src="$PWD/examples",dst=/config,readonly \
  netconfig:local -config /config/native.yaml -check
```

The image workflow publishes `ghcr.io/yanet-platform/netconfig` for Linux amd64
and arm64 on pushes to `main` and `v*` tags, with branch/tag and commit-SHA tags.
Use an immutable tag for deployment. The image contains no default topology;
mount the config and, when selected, the external Netplan file.

### Dataplane Pod

Run netconfig in the dataplane Pod's private namespace (`hostNetwork: false`).
It operates in its current namespace and never switches namespaces itself.
Kernel mutations require `CAP_NET_ADMIN` and writable per-interface IPv6 sysctls.
Adding that capability alone does not make a container's read-only `/proc/sys`
writable. The privileged container mode below supplies the required access;
deployment policy must provide equivalent isolated access if using a narrower
security context. Do not mount the host's `/proc/sys` into the container.

Merge this fragment into the existing dataplane Pod, providing the named
ConfigMap volumes and replacing the image tag:

```yaml
spec:
  hostNetwork: false
  initContainers:
    - name: netconfig
      image: ghcr.io/yanet-platform/netconfig:sha-REPLACE
      restartPolicy: Always
      args: ["-config", "/etc/netconfig/config.yaml"]
      securityContext:
        privileged: true
      volumeMounts:
        - {name: netconfig, mountPath: /etc/netconfig, readOnly: true}
        - {name: netplan, mountPath: /etc/netplan, readOnly: true}
```

Use a restartable init container as shown or an ordinary sidecar. A traditional
init container waiting for KNI would prevent the dataplane from starting and
creating KNI. Do not gate startup of the dataplane on completed configuration.
The default idle-after-success lifecycle is intended for `restartPolicy: Always`;
`-once` is for explicitly supervised one-shot execution.

## Tests

```bash
go test -race -count=1 ./...
go vet ./...
docker build --target test -t netconfig-test .
docker run --rm netconfig-test
docker run --rm --privileged --network none \
  -e NETCONFIG_NETNS_TESTS=1 netconfig-test \
  go test -race -count=1 -p=1 ./...
```

Kernel tests require a disposable network namespace, TAP support and writable
IPv6 sysctls. Never run them in the host network namespace. The Docker test target
builds the executable and sets `NETCONFIG_BINARY` so subprocess tests exercise the
real CLI; without that variable they explicitly skip. Kernel cases additionally
require `NETCONFIG_NETNS_TESTS=1` and run serially across packages.

Coverage includes strict parsing, address-family validation, replacement identity,
MTU ordering, IPv6LL/DAD, delayed KNI, partial progress, cancellation, immutable
input during retries, preservation of foreign addresses, SIGTERM without teardown,
restart reconciliation and no mutation after successful bootstrap.

## License

Netconfig is licensed under the [Apache License, Version 2.0](LICENSE).
The original YANET2 copyright notice is preserved. See [NOTICE](NOTICE) for
upstream attribution and the exact source revision.

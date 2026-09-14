# home-dev NodePort and NFSv4-to-NFSv3 architecture gate (2026-09-15)

This follow-up used the preserved namespace
`longhorn-nfs-gateway-acceptance-official` in context `home-dev`. The existing
RWO/RWX claims and PVs were retained throughout:

```text
rwo-gate -> pvc-147192e9-a010-47a8-b934-ab25b4b44e9f (Bound, ReadWriteOnce)
rwx-gate -> pvc-4ab99a23-742e-4c6e-bc6c-fc0dc9172372 (Bound, ReadWriteMany)
```

No PVC or PV was deleted.

## Immutable images

- Controller: `docker.io/initialed85/longhorn-nfs-gateway@sha256:745ecc2f50ca75b94f0788fe0088e7ebdf003ac59c99cb06a23c209a3bce29c4`
  (`linux/amd64`, `linux/arm64`)
- Official Ganesha: `docker.io/longhornio/nfs-ganesha@sha256:e633d9f2aa0281c6def298651a1b83a5dbb19f03f435f049aa1a757a53aa882b`
  (`linux/amd64`, `linux/arm64`, Ganesha 3.3)
- FSAL proxy assessment image:
  `docker.io/initialed85/nfs-ganesha-proxy-v4@sha256:d6ce0ab841c4f353aa4745007baa5f3b45c3dfceeb0f69a05edd6ba88dfaed1e`
  (`linux/amd64`, `linux/arm64`, Debian Bookworm `nfs-ganesha-proxy-v4` 4.3 package)

## RWO real NodePort gate: PASS

The controller generated its normal ClusterIP Service. A separately named,
disposable NodePort Service was created against the controller-owned helper
selector to test actual LAN reachability without a port-forward:

```text
NodePort Service: rwo-gate-nfs-nodeport
NFS TCP:          32049
mountd TCP:       32050
Test node:        192.168.1.114 (k3s-bee-2)
```

macOS mounted the real node address:

```sh
sudo -n mount_nfs -3 -o tcp,resvport,port=32049,mountport=32050 \
  192.168.1.114:/export /tmp/longhorn-nfs-nodeport
```

Verified:

- macOS write/read succeeded;
- unmount/remount succeeded;
- deleting the helper Pod caused Deployment recreation;
- after recreation, the same NodePort address mounted successfully and read
  the existing marker;
- the export CR, helper, generated Service, and disposable NodePort Service
  were removed afterward.

This proves the network path, but the controller API currently has no Service
type/external-address fields, so the NodePort Service was an explicit
acceptance overlay rather than a controller-produced production endpoint.
That API/deployment gap remains before claiming native macOS reachability.

## FSAL_PROXY_V4 architecture gate: PASS as an independent prototype

The prior VFS helper failed because it cannot re-export the Longhorn
share-manager's NFSv4 mount. For this isolated architecture test, a manual
proxy Deployment used the Debian `nfs-ganesha-proxy-v4` plugin and connected
directly to the Longhorn share-manager NFS endpoint:

```text
Backend NFSv4 endpoint: 10.43.13.190:2049
Remote path:            /pvc-4ab99a23-742e-4c6e-bc6c-fc0dc9172372
FSAL:                   PROXY_V4
NodePort NFS:           32051
NodePort mountd:        32052
```

The proxy config set `mount_path_pseudo = true`, allowing the NFSv3 mount path
`/export` to map to the remote NFSv4 path. With a temporary RWX probe Pod keeping
Longhorn's share-manager active, macOS mounted the real node address:

```sh
sudo -n mount_nfs -3 -o tcp,resvport,port=32051,mountport=32052 \
  192.168.1.114:/export /tmp/longhorn-nfs-proxy
```

Verified:

- macOS read the marker written through the Longhorn RWX mount;
- macOS wrote `from-mac.txt`, readable from the RWX probe Pod;
- unmount/remount succeeded and read the marker again;
- the proxy connected after the share-manager became active.

This is an independent architecture proof, not controller support: the proxy
was a manual Deployment and the share-manager endpoint is dynamic. The proxy,
probe, NodePort, ConfigMap, and NetworkPolicy were removed after testing; the
PVCs/PVs remain Bound.

## Remaining release blockers

1. Add an explicit, digest-pinned NodePort/LoadBalancer endpoint model to the
   controller API and handoff status, instead of requiring a manual Service.
2. Integrate and lifecycle-manage FSAL_PROXY_V4 (or an equivalent kernel proxy)
   for RWX, including dynamic share-manager endpoint discovery, retries, and
   failure cleanup.
3. Repeat both paths with controller-owned resources and full failure-injection
   coverage before a Macgrubernetes Longhorn release.

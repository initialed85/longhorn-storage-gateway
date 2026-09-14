# home-dev official-image acceptance gate (2026-09-15 local)

This rerun used Kubernetes context `home-dev` and a fresh isolated namespace:
`longhorn-nfs-gateway-acceptance-official`. The prior acceptance namespace and
its claims were not touched.

The code under test is committed on `origin/main` at `e9e51a4` (official image
pin/provenance change). The acceptance controller image was built from that
source and pushed as a multi-architecture manifest.

## Immutable image provenance

| Purpose | Source/tag | Immutable digest | Platforms |
| --- | --- | --- | --- |
| Controller | `docker.io/initialed85/longhorn-nfs-gateway:acceptance-official-20260915` | `sha256:745ecc2f50ca75b94f0788fe0088e7ebdf003ac59c99cb06a23c209a3bce29c4` | linux/amd64, linux/arm64 |
| Ganesha | Docker Hub `longhornio/nfs-ganesha:latest` | `sha256:e633d9f2aa0281c6def298651a1b83a5dbb19f03f435f049aa1a757a53aa882b` | linux/amd64, linux/arm64 |
| RWX writer | Docker Hub `debian:bookworm-slim` | `sha256:88200866dfff7ea7f5cbcb6ec7c8a701889efe6fe859fe64d6990e4b07ea4171` | multi-arch manifest |

Docker Hub inspection reported Ganesha platform manifests
`sha256:b6cf0a81144464c6253de5f26594da89d7f2c82a5d7c9d0e6145d6ca4c78160b`
(amd64) and `sha256:b47e5b68d4f8d34825a880e8c30d4902272b28967911bcaf02489fe992922e70`
(arm64), under manifest-list digest `sha256:e633d9f2...`. The image contains
NFS-Ganesha 3.3 and its upstream `/opt/start_nfs.sh`; the controller runs the
pinned image with an explicit mounted configuration and rpcbind startup.

The repository POC manifests and controller default now use this immutable
Ganesha digest. The image is fully pinned in all committed samples and
controller defaults. PVC-name placeholders remain intentionally in static POC
samples.

## Render/apply

Passed:

- Rendered and applied the CRD, RBAC, controller Deployment, and manager
  namespace from a temporary digest-pinned overlay.
- Controller pulled and ran on the amd64 home-dev nodes and reconciled both
  exports.
- Controller, CRD, RBAC, and manager namespace were removed after testing.
- The acceptance namespace remains because its PVCs/PVs were explicitly not
  deleted.

## RWO: PASS

Fresh claim retained after cleanup:

```text
PVC rwo-gate  Bound  pvc-147192e9-a010-47a8-b934-ab25b4b44e9f  ReadWriteOnce
PV  pvc-147192e9-a010-47a8-b934-ab25b4b44e9f  Bound  longhorn-nfs-gateway-acceptance-official/rwo-gate
```

The `LonghornNFSExport/rwo-gate` status reached `Ready`; its helper Pod and
Service were Ready with the official pinned Ganesha image.

macOS NFSv3 tests used a local port-forward on 3049/30048 (the normal local
2049/20048 ports were unavailable from a stale previous port-forward):

```sh
kubectl -n longhorn-nfs-gateway-acceptance-official \
  port-forward svc/rwo-gate-nfs 3049:2049 30048:20048
sudo -n mount_nfs -3 -o tcp,resvport,port=3049,mountport=30048 \
  127.0.0.1:/export /tmp/longhorn-nfs-official-rwo
```

Verified:

- macOS read/write marker succeeded;
- unmount/remount succeeded;
- helper deletion caused Deployment recreation, and a restarted port-forward
  allowed a successful reconnect and read;
- deleting only the export removed the helper Pod, Deployment, Service,
  ConfigMap, and NetworkPolicy;
- the PVC and PV remained Bound.

## RWX NFSv4-to-v3: BLOCKED / FAIL

Fresh claim retained after cleanup:

```text
PVC rwx-gate  Bound  pvc-4ab99a23-742e-4c6e-bc6c-fc0dc9172372  ReadWriteMany
PV  pvc-4ab99a23-742e-4c6e-bc6c-fc0dc9172372  Bound  longhorn-nfs-gateway-acceptance-official/rwx-gate
```

The controller and helper reached `Ready`. A second Pod mounted the RWX PVC
and wrote a marker; the Ganesha helper read it, confirming the Longhorn
share-manager NFSv4.1 side:

```text
10.43.172.249:/pvc-4ab99a23-742e-4c6e-bc6c-fc0dc9172372 /export nfs4 ...
```

The macOS NFSv3 bridge mount failed with exit 13 (`Permission denied`).
Official Ganesha 3.3 logged:

```text
resolve_posix_filesystem(/export) returned Resource temporarily unavailable
Could not create export for (/export) to (/export)
No export entries found in configuration file
```

This is the same independent NFSv4-to-v3 re-export blocker as the prior gate.
RWX Longhorn support remains unaccepted.

Cleanup removed only the RWX writer Pod, export CR, helper, Deployment,
Service, ConfigMap, and NetworkPolicy. Both acceptance PVCs and their PVs
remain Bound; no PVC/PV deletion was requested or performed.

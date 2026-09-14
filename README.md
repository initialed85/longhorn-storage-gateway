# Longhorn NFS gateway

Cluster-side gateway for exposing selected Longhorn PVCs to macOS native
workloads through a generic NFS endpoint. This repository deliberately keeps
Longhorn-specific discovery and helper lifecycle out of maclet. Maclet consumes
only the generic handoff contract documented in [`docs/handoff.md`](docs/handoff.md).

> **Status: design/POC.** The `v1alpha1` API contract and manual helper POC
> manifests are present. The controller, macOS NFSv3 validation, and production
> readiness gates are not complete. Do not advertise Longhorn gateway support in
> a Macgrubernetes release yet.

## Design boundary

The future controller will reconcile an explicitly created namespaced
`LonghornNFSExport` into:

- one deterministic helper workload using `Recreate` semantics;
- one Service exposing NFSv3 TCP 2049 and a fixed mountd TCP port;
- one NetworkPolicy; and
- a generic NFS handoff on opted-in native Pods.

The controller must never delete a PVC or PV. Helper resources are owned by the
`LonghornNFSExport` and are additionally labeled for orphan cleanup. A finalizer
may remove only controller-owned helpers and handoff metadata.

RWO and RWX are separate acceptance paths:

- **RWO**: the helper must be the sole consumer and must never overlap during
  replacement.
- **RWX**: the helper mounts Longhorn's share-manager NFSv4 export and
  re-exports it as NFSv3 through NFS-Ganesha. This is an independent
  compatibility experiment with separate locking, caching, identity, and
  performance risks.

## Generic handoff

The handoff contains only generic NFS endpoint data. It does not expose
Longhorn API names or fields to maclet:

```text
storage.k8s-darwin.dev/nfs-server       <Service ClusterIP or reachable DNS name>
storage.k8s-darwin.dev/nfs-export       /export
storage.k8s-darwin.dev/nfs-version      3
storage.k8s-darwin.dev/nfs-mount-port   20048
storage.k8s-darwin.dev/nfs-generation   <controller generation>
```

A handoff is published only after the helper is Ready and the endpoint has
passed controller-side checks. Maclet must treat missing, incomplete, stale, or
unreachable handoffs as Pending. The handoff mechanism is not an admission
webhook and does not mutate Pod specs or volumes.

## Manual POC

The `config/samples/poc/` manifests are deliberately static and clearly marked
POC. Fill in existing claim names before applying. Apply exactly one of the RWO
or RWX examples at a time:

```sh
kubectl apply -f config/samples/poc/rwo/
# or
kubectl apply -f config/samples/poc/rwx/
```

The POC uses an NFS-Ganesha helper with NFSv3 TCP 2049 and mountd TCP 20048.
Pin the Ganesha image by digest before use. The helper image/configuration may
need adjustment for the target cluster's Pod Security and Ganesha build.

From a macOS host with passwordless sudo:

```sh
sudo -n mount_nfs -o vers=3,tcp,port=2049,mountport=20048 \
  <SERVICE_IP>:/export /tmp/longhorn-gateway-test
printf 'gateway smoke\n' | sudo tee /tmp/longhorn-gateway-test/maclet.txt
cat /tmp/longhorn-gateway-test/maclet.txt
sudo -n umount /tmp/longhorn-gateway-test
```

Acceptance requires read/write, unmount, remount/reconnect, helper deletion,
and cleanup verification. Longhorn's current macOS/NFSv4 direct compatibility
problem is not hidden by this bridge; RWX requires a separate NFSv4-to-v3 gate.

## Production gates

Do not publish gateway support until all of these are complete:

- controller implementation and focused reconciliation tests;
- CRD status Conditions for PVC binding, helper readiness, endpoint readiness,
  protocol compatibility, and cleanup failures;
- ownerReference/finalizer/orphan cleanup tests that never delete PVC/PV;
- independent RWO and RWX acceptance suites;
- macOS NFSv3 read/write/remount/reconnect tests;
- failure cleanup after helper, Service, controller, or node loss; and
- both-architecture Macgrubernetes integration validation.

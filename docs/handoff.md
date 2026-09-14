# Generic NFS handoff contract

The gateway's Longhorn implementation ends at a generic NFS endpoint. maclet
must not need Longhorn client types, CRDs, Volume names, share-manager fields,
or Longhorn-specific annotations.

## Pod annotations

The controller may patch only these controller-owned annotations on opted-in
native Pods after helper readiness:

| Annotation | Meaning |
|---|---|
| `storage.k8s-darwin.dev/nfs-server` | Reachable NFS server, normally the helper Service ClusterIP or DNS name |
| `storage.k8s-darwin.dev/nfs-export` | Export path, normally `/export` |
| `storage.k8s-darwin.dev/nfs-version` | Protocol version; the POC requires `3` |
| `storage.k8s-darwin.dev/nfs-mount-port` | TCP mountd port, normally `20048` |
| `storage.k8s-darwin.dev/nfs-generation` | Monotonic handoff generation |

The annotations are endpoint data only. They do not grant access, select a
PVC, or mutate the Pod spec. A missing or incomplete set is not a usable
handoff. A controller-owned readiness annotation may be added separately, but
maclet must still treat mount/connect failure as Pending.

## CRD status endpoint

`LonghornNFSExport.status.endpoint` mirrors the same generic fields for
operators and automation:

```yaml
endpoint:
  server: 10.43.1.70
  export: /export
  version: 3
  mountPort: 20048
  generation: "7"
```

The status must not expose Longhorn's internal share endpoint as the maclet
contract. Longhorn details belong only in controller diagnostics and Events.

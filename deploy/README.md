# Release deployment

This directory is the controller-owned release artifact. It installs the CRD,
controller ServiceAccount/RBAC, manager namespace, and pinned controller image.
The current release controller image is:

```text
docker.io/initialed85/longhorn-nfs-gateway@sha256:ce985e989543c0faa1e16b4660fe80f86576e08ae1fb1c8def34b8564e7ac4e3
```

Render or apply it with:

```sh
kubectl kustomize deploy
kubectl apply -k deploy
```

Create an export separately after creating a Longhorn PVC. For a reachable
native macOS endpoint, set the explicit service model in the export:

```yaml
service:
  type: NodePort
  externalAddress: 192.168.1.114 # reachable node address or DNS
  nfsNodePort: 32049
  mountNodePort: 32050
```

Use `LoadBalancer` instead when the cluster provides a reachable load-balancer
address. RWX requires `ReadWriteMany`; the controller discovers the Longhorn
share-manager Service and creates the pinned FSAL_PROXY_V4 helper only while a
Ready NFS endpoint exists.

## Safe uninstall

Do not delete the CRD/controller before deleting the exports; doing so bypasses
normal finalizer cleanup. First delete only the exports managed by this
installation and wait for their finalizers to complete:

```sh
kubectl get longhornnfsexports.storage.k8s-darwin.dev -A
kubectl delete longhornnfsexports.storage.k8s-darwin.dev \
  -n <namespace> <export-name> [<another-export-name>]
kubectl wait --for=delete \
  longhornnfsexports.storage.k8s-darwin.dev/<export-name> \
  -n <namespace> --timeout=180s
```

Then remove the controller-owned release resources:

```sh
kubectl delete -k deploy
```

`kubectl delete -k deploy` removes only the release CRD, controller namespace,
RBAC, and controller Deployment. It does **not** delete PVCs or PVs. Never
remove the acceptance/application namespace or run `kubectl delete pvc`/`kubectl
delete pv` as part of gateway cleanup.

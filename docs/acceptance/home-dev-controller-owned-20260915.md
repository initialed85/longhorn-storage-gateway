# home-dev controller-owned endpoint/proxy gate (2026-09-15)

This is the controller-owned follow-up gate in context `home-dev` and namespace
`longhorn-nfs-gateway-acceptance-official`. It validates the API/controller
changes from commits `4231208` and `be87f8c`:

- `spec.service.type`, `externalAddress`, and NodePort fields are reconciled;
- status endpoint reports the external server, service type, and NodePorts;
- RWX discovers the Longhorn share-manager Service and EndpointSlice;
- RWX creates an FSAL_PROXY_V4 helper only while the share-manager has a Ready
  endpoint;
- losing the share-manager endpoint cleans controller-owned proxy resources;
- endpoint return causes proxy resources to be recreated.

## Immutable images

- Controller:
  `docker.io/initialed85/longhorn-nfs-gateway@sha256:19d8e548514ef16e719e29fc90af5cf83eb1e57c4c8fc0d99109b866bc76c0ab`
  (`linux/amd64`, `linux/arm64`)
- RWO Ganesha:
  `docker.io/longhornio/nfs-ganesha@sha256:e633d9f2aa0281c6def298651a1b83a5dbb19f03f435f049aa1a757a53aa882b`
- RWX proxy:
  `docker.io/initialed85/nfs-ganesha-proxy-v4@sha256:d6ce0ab841c4f353aa4745007baa5f3b45c3dfceeb0f69a05edd6ba88dfaed1e`

## Controller-owned RWO NodePort: PASS

The export was created with:

```yaml
service:
  type: NodePort
  externalAddress: 192.168.1.114
  nfsNodePort: 32049
  mountNodePort: 32050
```

The controller created the NodePort Service and reported the export `Ready`
with endpoint server `192.168.1.114`. macOS mounted the actual node address
(no port-forward), wrote/read a marker, unmounted/remounted, and read the
marker after helper deletion/recreation.

## Controller-owned RWX FSAL_PROXY_V4: PASS

A temporary RWX probe Pod kept the Longhorn share-manager active. The
controller discovered its NFS Service and Ready EndpointSlice, then created a
proxy helper with `PROXY_V4`, remote path `/pvc-4ab99a23-742e-4c6e-bc6c-fc0dc9172372`,
and NodePort endpoint:

```text
NFS:     192.168.1.114:32051
mountd:  192.168.1.114:32052
export:  /export
```

macOS NFSv3 mounted the controller-owned NodePort, read share-manager data,
wrote `from-mac.txt` visible from the RWX probe, and remounted successfully.
The endpoint status reported `Ready`, `NodePort`, and the external address.

### Failure/retry cleanup

Deleting the RWX probe removed the share-manager Ready EndpointSlice. The
controller changed the export to `Pending / ShareManagerUnavailable` and
removed its Deployment, Service, ConfigMap, and NetworkPolicy. Recreating the
probe restored the share-manager endpoint; the controller recreated the proxy
resources and returned the export to `Ready`. This validates retry and cleanup
without touching the PVC or PV.

## Cleanup verification

The two export CRs, helper Deployments, Services, ConfigMaps, NetworkPolicies,
probe Pod, controller Deployment, CRD, RBAC, and controller namespace were
removed after the gate. The acceptance namespace remains because its claims
were explicitly preserved:

```text
rwo-gate  Bound  pvc-147192e9-a010-47a8-b934-ab25b4b44e9f  ReadWriteOnce
rwx-gate  Bound  pvc-4ab99a23-742e-4c6e-bc6c-fc0dc9172372  ReadWriteMany
```

No PVC or PV was deleted. Macgrubernetes integration remains held pending
broader failure-injection and release validation.

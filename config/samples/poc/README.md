# Manual Longhorn NFS-Ganesha POC

These manifests are **development-only POC artifacts**. They are not consumed
by the controller and must not be included in a Macgrubernetes release.

Replace the `REPLACE_WITH_*_PVC_NAME` placeholder in the selected Deployment
before applying. The helper image is pinned to Docker Hub's
`longhornio/nfs-ganesha:latest` multi-architecture manifest digest
`sha256:e633d9f2aa0281c6def298651a1b83a5dbb19f03f435f049aa1a757a53aa882b`
(Ganesha 3.3); verify the provenance before changing it. Use only one mode at
a time:

```sh
kubectl apply -k config/samples/poc/rwo
# or
kubectl apply -k config/samples/poc/rwx
```

The helper uses a Linux Pod-mounted PVC and re-exports `/export` as NFSv3 on
TCP ports 2049 and 20048. The image/config may require adjustment for the target Ganesha build and Pod
Security profile. This POC uses a privileged container because many Ganesha
builds need kernel filesystem and locking capabilities; it is not a production
security baseline.

# Manual Longhorn NFS-Ganesha POC

These manifests are **development-only POC artifacts**. They are not consumed
by the controller and must not be included in a Macgrubernetes release.

Replace both `REPLACE_WITH_*_PVC_NAME` and
`REPLACE_WITH_PINNED_NFS_GANESHA_IMAGE` placeholders in the selected
Deployment before applying. Use only one mode at a time:

```sh
kubectl apply -k config/samples/poc/rwo
# or
kubectl apply -k config/samples/poc/rwx
```

The helper uses a Linux Pod-mounted PVC and re-exports `/export` as NFSv3 on
TCP ports 2049 and 20048. The image must be pinned to a tested digest; the
placeholder is intentional so an unreviewed image cannot be deployed by
mistake. The image/config may require adjustment for the target Ganesha build
and Pod Security profile. This POC uses a privileged container because many
Ganesha builds need kernel filesystem and locking capabilities; it is not a
production security baseline.

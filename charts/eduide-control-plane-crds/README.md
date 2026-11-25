# eduide-control-plane-crds

The four CRDs of the EduIDE control plane: `IDETemplate`, `IDEPool`, `IDESession`, `IDEWorkspace` (group `eduide.github.io`).
The files in `templates/` are copies of `config/crd/*.yaml`, written by `make generate`.
Never edit them here.

## Install

Install this chart once per cluster, before any `eduide-control-plane` release:

```sh
helm install eduide-control-plane-crds oci://ghcr.io/eduide/eduide-control-plane/charts/eduide-control-plane-crds --version 0.2.5
```

Operators of different versions run side by side in one cluster (one per deployment namespace).
CRD changes are therefore additive only (ADR 0011).
Upgrade this chart before the control plane charts.
Note that `helm uninstall` deletes the CRDs and with them every custom resource in the cluster.

There are no values.

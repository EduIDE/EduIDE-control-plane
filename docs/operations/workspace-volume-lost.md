# Workspace volume lost

## Symptom

An `IDEWorkspace` shows `Ready=False` with reason `VolumeLost`, a Warning Event `VolumeLost` and the operator log line `workspace volume lost`.
`POST /sessions` on it answers `409` with code `workspace-lost`; a session that references it fails with `errorCode: workspace-lost`.
The phase stays as it was.

## Cause

The PVC of a workspace that held data (`status.claimName` is set) no longer exists, for example after a manual delete.
The operator does not create a new empty PVC for such a workspace, because the user would silently lose the old files.
A workspace that never had a bound PVC still gets its PVC created as usual.

## Recovery

1. Restore the PVC under the name in `status.claimName` (from a snapshot or a PV that still exists).
   The operator sees it, sets `Ready=True` again, and new sessions work.
2. Or give up the data: `kubectl -n <ns> delete ideworkspace <name>`.
   The user's next launch creates a fresh workspace and volume.

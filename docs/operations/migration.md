# Migration from the Java stack

Method: parallel run per deployment. Decisions: [ADR 0017](../adr/0017-migration-parallel-run.md) (G12), ADR 0003 for the separate CRD group.

## Rules

- Both stacks run side by side in a cluster. The CRD groups differ (`theia.cloud` and `eduide.github.io`).
- Order: test deployments, staging, pilot deployments, productive deployments. The phase exit criteria steer the order, not a calendar.
- Blackout: no productive deployment moves during an exam period.
- Way back: a deployment can return to the Java stack until the end of the month after the switch. Until then the Java stack gets only security fixes.
- Session data is not migrated. Running sessions end on the old stack.
- When all deployments run on the new stack, the Java stack and the workspace garbage collector are switched off, and the old repository is archived.

## Steps per deployment

1. **Prepare.** Install the CRD chart in the cluster if it is missing ([install.md](install.md)). Create the Keycloak clients, including the confidential gateway client with redirect `https://<instance host>/oauth2/callback`. Add the hosts, certificates and gateway listener (installation and webview host).
2. **Install the new stack** in its own namespace or next to the old one, with new hosts. Do not touch the Java stack.
3. **Check the IDE image.** The image must contain the `eduide-session` extension and the webview host settings. Images without them do not receive launch data.
4. **Smoke test** with the landing page switched on for test users only: launch warm and cold, open the IDE as owner and as a foreign user (expect 403), and run the NetworkPolicy negative test.
5. **Import workspaces** (below).
6. **Switch** in the landing page: the feature flag per deployment sends launches to the new API client. Watch [slow-launch](slow-launch.md), [authz-errors](authz-errors.md) and the metrics for a day.
7. **Keep the way back.** Switch the flag back if needed. Workspaces created on the new stack stay in their PVCs.
8. **Retire** the Java stack of the deployment after the grace period.

## Workspace PVC import

`IDEWorkspace` with `spec.persistence.mode: volume` can adopt an existing PVC through `spec.persistence.existingClaimName`.
The import sets `status.lastUsed` to the import time, so adopted data does not expire at once (retention default 14 days; G19).
The operator adopts the PVC: it sets the workspace as controller owner of the claim, so the retention deletes the claim together with the workspace, like one the operator created. Remove the owner reference from the PVC if the data must outlive the workspace.

Per old workspace:

1. List the old PVCs of the namespace: `kubectl -n <ns> get pvc`.
2. Create an `IDEWorkspace` that names the PVC. The workspace name follows the rule in [data-model.md](../architecture/data-model.md); set `spec.owner`, `spec.templateRef` and `spec.persistence.existingClaimName`.
3. Check `status.phase` is `Ready` and `status.claimName` equals the PVC name.

```yaml
apiVersion: eduide.github.io/v1alpha1
kind: IDEWorkspace
metadata:
  name: w-<16 hex>
  labels:
    app.kubernetes.io/managed-by: eduide
spec:
  owner: <user name from the token claim>
  templateRef: <template>
  persistence:
    mode: volume
    existingClaimName: <old PVC name>
```

**Planned:** an import job that does this for all PVCs of a namespace. Until it exists, import by hand or with a script you review first.
Make sure the old stack no longer mounts the PVC (no running old session) before the new session uses it.

## Differences users notice

- A session URL is `https://<instance host>/<instance>/` with a random id.
- Webviews run on one fixed host, `webview.<instance host>`.
- The landing page shows an overview of workspaces and sessions per user.
- Artemis passes the token in the URL fragment (`#artemisToken=...`). `?artemisToken` is still read during the migration and logged as deprecated.
- Workspace expiry runs after last use, not after creation date.

# Runbook: authz errors

Symptom: users get 403 or a redirect loop on `https://<instance host>/<instance>/`, or nobody reaches a new IDE.

How the decision works: [session-access.md](../architecture/session-access.md).

## Metrics

| Metric | Meaning |
|---|---|
| `eduide_authz_decisions_total{result,reason}` | `allow`, `deny`, `login` (redirect to sign in again) |
| `eduide_authz_check_seconds` | Latency of one check; high latency slows every request to every session |

Alert on a high `deny` rate, on a high `login` rate (token or cookie problem) and on check latency. **Planned:** alert names are set by the chart.

## Checks

```bash
kubectl -n <ns> get pods -l app.kubernetes.io/component=authz
kubectl -n <ns> logs deploy/<authz> --since=15m
kubectl -n <ns> get idesession <name> -o jsonpath='{.status.phase}{" "}{.status.instance}{" "}{.spec.owner}{"\n"}'
kubectl -n <ns> get securitypolicy
kubectl -n <ns> get httproute -l eduide.github.io/route=session
kubectl -n <ns> get networkpolicy
```

Log lines carry the decision reason and the trace id, never tokens or cookie values.

## Causes and fixes

| Symptom | Cause | Fix |
|---|---|---|
| 403 for the owner right after launch | Session not yet `Ready` in the authz informer | Wait a second; if it stays, check authz can list sessions (RBAC) and the informer is synced (`/readyz`) |
| 403 for the owner, session `Ready` | `spec.owner` differs from the token claim named by `oidc.userClaim` | Compare the claim in the token with `spec.owner`; fix `oidc.userClaim` in the config, or the Keycloak mapper |
| 403 for everyone | authz internal error (fail closed), or the instance is being deleted | Read the authz logs; restart authz; check the config (`oidc.issuerURL`, `oidc.gatewayClientID`) |
| 403 for a foreign user | Expected | None |
| Redirect loop | Token invalid: audience is not `oidc.gatewayClientID`, issuer mismatch, clock skew | Check the gateway client id in the config and Keycloak; check node time; authz expires all auth cookies and the login restarts |
| Redirect loop after Keycloak login | The cookie is not sent: cookie domain wrong, or cookie too large | Check the SecurityPolicy `cookieDomain` covers `hosts.instance`; check cookie size in the browser |
| `/oauth2/callback` returns 404 | The `/oauth2/` route is missing | Check the chart's `/oauth2/` HTTPRoute with label `eduide.github.io/route=session` and the redirect URI of the Keycloak gateway client |
| No access check happens | The route lacks the label, so the SecurityPolicy does not select it | Label `eduide.github.io/route=session` must be on the HTTPRoute; the operator sets it |
| Nobody reaches a new IDE | authz down (both replicas) | `kubectl get pods`; PodDisruptionBudget; restart. Open WebSockets stay up. Do not set `failOpen`. |
| JWKS errors in logs | authz cannot reach Keycloak | Check egress policy and DNS to the issuer |

## Never

Do not turn off authz or switch the policy to fail open to "get users in". A request without a login must never reach an IDE. After any change, request a session route without cookies and confirm you get a login redirect.

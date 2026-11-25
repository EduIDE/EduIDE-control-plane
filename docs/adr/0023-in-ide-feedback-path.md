# 0023. In-IDE feedback path with the instance token

- Status: Accepted
- Date: 2026-11-25
- Decisions: U18 (numbers from [decisions.md](decisions.md))
- Contracts: [api/openapi/v1.yaml](../../api/openapi/v1.yaml), [api/agent/v1.yaml](../../api/agent/v1.yaml), [internal/config/config.go](../../internal/config/config.go)

## Context and Problem Statement

Students report bugs and request features from inside the IDE.
The text must reach one central tracker, with technical context and without personal data.
The IDE runs in the browser of the student, but the agent proxy strips the login cookies before the IDE, so the IDE has no OIDC token it could send to the service host.
Session pods may reach the internet but not private ranges ([0019](0019-network-isolation.md)).
The service already reads the instance token Secret (`ide-<instance>-token`) that the operator creates ([0006](0006-agent-sidecar.md)).

## Decision Drivers

- No anonymous endpoint on the internet and no public key in the IDE.
- No new credential for the student and no new Secret per session.
- Per-user rate limit, so one account cannot flood the tracker.
- No user identity in the text that leaves the installation.
- An operator can turn the whole path off, and off is the default.
- Additive within `v1` ([0010](0010-rest-api-spec-first.md)).

## Considered Options

1. The IDE in the browser calls the service with the user's OIDC token.
2. The extension calls the agent on the loopback port, the agent calls the service with the instance token.
3. The agent posts to the central receiver directly with a per-installation key.

## Decision Outcome

Chosen option: "The extension calls the agent, the agent calls the service with the instance token".

Path: IDE dialog, command `eduide.feedback.send` of the `eduide-session` extension, agent `POST /v1/feedback` on the loopback listener, `POST /api/v1/feedback` on the public service host, central receiver, issue in the triage repository.

- **Toggle, default off.** `config.feedback.enabled` (chart `eduide-control-plane`) is `false` by default. Off: the service answers 404 on `/api/v1/feedback` before any other check, the operator does not set `AGENT_FEEDBACK_URL`, `LocalSession.feedback` is absent, and the IDE keeps its prefilled GitHub link. On needs an https `receiverURL` and a Secret with the installation key; the chart refuses to render without them. The change reaches pods created afterwards; pool pods keep their environment, so pools are recreated after toggling.
- **Operator.** With the switch on, the agent container gets `AGENT_FEEDBACK_URL=https://<hosts.service>/api/v1/feedback`.
- **Agent.** `LocalSession.feedback` is true when that variable is set. The loopback `POST /v1/feedback` forwards the JSON body unchanged (limit 16 KiB, timeout 10 s) with `Authorization: Bearer <instance token>` and `X-EduIDE-Instance: <instance>` and returns the status and `Retry-After` of the service. It follows no redirects.
- **Service.** The route is exempt from the OIDC token check and has its own: the instance header, a constant-time compare with the token Secret, and a claimed session for that instance. The owner of the session is the rate limit key, 5 per hour. The service validates the body, redacts tokens, email addresses and matriculation numbers from title and description, and forwards `{kind, title, description, context, installation, host}` to `receiverURL` with the installation key as bearer. No user, session or instance id goes along. The key reaches only the service pod. The service sets no CORS headers, so browsers cannot call the route from another origin.
- **Order of checks.** Per-IP limit, then the session of the instance from the cache, then the token Secret (one API read only for known instances). Whitespace is trimmed before the length check; after redaction the text is cut to the limits. Context values are redacted, reduced to `[A-Za-z0-9 ._()/+-]` and cut to 100 characters, and `appDefinition` comes from the template of the session, not from the client.
- **Errors.** 202, 400 `invalid-input`, 401 `unauthorized`, 404 `not-found`, 429 `rate-limited` with `Retry-After`, a 429 of the receiver is passed on with its `Retry-After`, other receiver failures give 502 new code `upstream-unavailable`. A failed forward gives the rate limit slot back.
- **Logs.** Neither text nor user name nor token is logged; a failed forward logs only the transport error.

### Consequences

- Good: the student needs no new login and no key; the owner is known only inside the service.
- Good: off by default, and the 404 makes the route invisible where it is not wanted.
- Good: the instance token already exists and is already trusted by the agent.
- Bad: the rate limit lives in memory per service replica, so n replicas allow n times 5 per hour. A shared store comes only if abuse shows up.
- Bad: the redaction is a pattern list and errs toward removing too much; it cannot find every personal detail in free text. The dialog tells students not to include any.
- Bad: every installation that turns it on needs an installation key at the central receiver.
- Follow-ups: the receiver binary and chart `eduide-feedback-receiver` ([0024](0024-feedback-receiver.md)), the IDE dialog in `EduIDE`, the landing page footer links. Metrics for the route come from the generic HTTP metrics (`operation=sendFeedback`).

## Pros and Cons of the Options

### 1. The IDE calls the service with the user's OIDC token

- Good, because the user is known to the service directly.
- Bad, because the IDE has no token: the proxy strips the cookies, and the service host is another origin.

### 2. Extension, agent, service with the instance token

- Good, because it needs no new credential and reuses the agent as the only egress of the pod.
- Bad, because the agent needs the internet route to the service host, which the session egress policy allows by default.

### 3. The agent posts to the receiver directly

- Good, because it has fewer hops.
- Bad, because there is no per-user rate limit and the installation key would sit in every session pod.

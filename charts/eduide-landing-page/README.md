# eduide-landing-page

Deployment, Service and HTTPRoute (`/` on the service host) of the EduIDE landing page.
The `config` value is served as `/config.json` (mounted at `/etc/eduide/landing/config.json`).
The route for `/api/` on the same host comes from the `eduide-control-plane` chart.

| Key | Default | Description |
|---|---|---|
| `host` | required | Service host, equal to `config.hosts.service` of the control plane |
| `gateway.name`, `.namespace`, `.sectionName` | `name` required | parentRef of the HTTPRoute |
| `config` | title, `/api/v1`, Keycloak at `auth.example.org` | Content of `config.json`. Set `config.legal.imprintUrl` and `config.legal.privacyUrl` to show Imprint and Privacy links in the footer |
| `image.repository`, `image.tag` | `ghcr.io/eduide/eduide-control-plane/landing-page`, appVersion | |
| `replicas`, `resources`, `imagePullSecrets` | 1 | |

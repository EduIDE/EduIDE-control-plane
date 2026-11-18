# eduide-landing-page

Deployment, Service and HTTPRoute (`/` on the service host) of the EduIDE landing page.
The `config` value is served as `/config.json` (mounted at `/etc/eduide/landing/config.json`).
The route for `/api/` on the same host comes from the `eduide-control-plane` chart.

| Key | Default | Description |
|---|---|---|
| `host` | required | Service host, equal to `config.hosts.service` of the control plane |
| `gateway.name`, `.namespace`, `.sectionName` | `name` required | parentRef of the HTTPRoute |
| `config` | title, `/api/v1`, Keycloak at `auth.example.org` | Content of `config.json`. `config.legal` is not allowed any more, use `legal` |
| `legal.imprint`, `legal.privacy` | required | Imprint and privacy page of the deployment. Each sets exactly one of `url` (http or https) or `markdown` (the text, served at `/imprint` or `/privacy`). Rendering fails when one is missing or both are set, also with `--skip-schema-validation` |
| `image.repository`, `image.tag` | `ghcr.io/eduide/eduide-control-plane/landing-page`, appVersion | |
| `replicas`, `resources`, `imagePullSecrets` | 1 | |

```yaml
legal:
  imprint:
    url: https://example.org/imprint
  privacy:
    markdown: |
      # Privacy
      ...
```

Markdown goes into the ConfigMap as `imprint.md` and `privacy.md`, and `config.json` points the page at `/legal/imprint.md` and `/legal/privacy.md`.

The landing page sends a Content Security Policy that allows images only from its own origin, so logos and images in legal Markdown must be served by the page itself.

Keycloak: the greeting needs the claim `given_name`, which comes from the `profile` client scope of the landing page client. Without it the page greets without a name.

## Upgrade notes

`config.legal.imprintUrl` and `config.legal.privacyUrl` are removed (hard switch, the chart is experimental). Move them to `legal.imprint.url` and `legal.privacy.url`, or provide the text as `markdown`.

"""EduIDE control plane (Go rewrite): architecture diagrams.

Plan: ../../2026-11-18-eduide-control-plane-go.md
Run: python3 diagrams.py [--png <preview dir>]
"""

from pathlib import Path

import drawio_dsl as dsl
from drawio_dsl import *  # noqa: F403

dsl.OUT_DIR = Path(__file__).resolve().parent


def fx(box_x, box_w, x):
    """Relative x on a box for absolute x, for exit/entry on top or bottom sides."""
    return round((x - box_x) / box_w, 4)


def object_model():
    """Domain of the new CRDs. Session in the middle, it touches everything."""
    d = Diagram("object-model")
    user, ub = uml_class(d, 470, 40, 260, "User", ["login: String", "email: String"])
    ws, wb = uml_class(d, 40, 220, 250, "Workspace",
                       ["title: String", "persistence: PersistenceMode", "size: Quantity",
                        "lastUsed: DateTime", "retention: Duration", "expired: Boolean"])
    sess, sb = uml_class(d, 470, 220, 290, "Session",
                         ["title: String", "source: String", "phase: SessionPhase",
                          "startStrategy: StartStrategy", "url: URL", "lastActivity: DateTime"],
                         ["stop()"])
    tpl, tb = uml_class(d, 940, 220, 300, "Template",
                        ["displayName: String", "image: String", "resources: Resources",
                         "idleTimeout: Duration", "maxLifetime: Duration",
                         "injectableKeys: KeyRule [*]", "persistenceModes: PersistenceMode [*]"])
    data, db = uml_class(d, 540, 520, 220, "LaunchData", ["values: Map<Key, String>"])
    pool, pb = uml_class(d, 940, 520, 300, "Pool",
                         ["minIdle: Integer", "targetIdle: Integer", "maxInstances: Integer",
                          "underPressure: Boolean"], ["scale(targetIdle: Integer)"])
    inst, ib = uml_class(d, 940, 760, 300, "Instance", ["state: InstanceState", "createdAt: DateTime"])

    hassoc(d, ws, wb, sess, sb, 270, "0..1", "mounts", "0..1")
    hassoc(d, sess, sb, tpl, tb, 270, "*", "instance of", "1")

    # User owns sessions (straight down) and workspaces (L to the left)
    d.edge(user, sess, ASSOC, exit=(fx(470, 260, 600), 1), entry=(fx(470, 290, 600), 0))
    d.text(606, 118, 40, 20, "1")
    d.text(606, 196, 40, 20, "*")
    d.text(510, 156, 84, 20, "<i>owns</i>", align="right")
    d.edge(user, ws, ASSOC, exit=(0, 0.5), entry=(fx(40, 250, 165), 0), points=[(165, 84)])
    d.text(434, 62, 30, 20, "1", align="right")
    d.text(170, 196, 30, 20, "*")
    d.text(240, 62, 120, 20, "<i>owns</i>", align="center")

    # Session carries its launch data (composition, straight down)
    d.edge(sess, data, COMPOSITION, exit=(fx(470, 290, 716), 1), entry=(fx(540, 220, 716), 0))
    d.text(722, 420, 30, 20, "1")
    d.text(722, 496, 30, 20, "1")
    d.text(600, 458, 110, 20, "<i>carries</i>", align="right")

    # Session runs on at most one claimed instance: down the gap between session and template
    d.edge(sess, inst, ASSOC, exit=(1, frac(sb, 370)), entry=(0, frac(ib, 800)), points=[(850, 370), (850, 800)])
    d.text(766, 346, 50, 20, "0..1")
    d.text(888, 776, 50, 20, "0..1")
    d.text(858, 600, 60, 40, "<i>runs<br/>on</i>")

    # Workspace remembers its template: around the top of everything
    d.edge(ws, tpl, ASSOC, exit=(fx(40, 250, 77), 0), entry=(fx(940, 300, 1195), 0), points=[(77, 20), (1195, 20)])
    d.text(82, 196, 30, 20, "*")
    d.text(1200, 196, 30, 20, "1")
    d.text(800, 0, 140, 20, "<i>based on</i>", align="center")

    # Pool prewarms one template, owns its instances
    d.edge(pool, tpl, ASSOC, exit=(0.5, 0), entry=(0.5, 1))
    d.text(1096, 390, 40, 20, "1")
    d.text(1096, 496, 40, 20, "0..1")
    d.text(1120, 446, 90, 20, "<i>prewarms</i>")
    d.edge(pool, inst, COMPOSITION, exit=(0.5, 1), entry=(0.5, 0))
    d.text(1096, 690, 30, 20, "1")
    d.text(1096, 736, 30, 20, "*")
    d.text(1120, 712, 90, 20, "<i>keeps</i>")

    uml_class(d, 40, 520, 180, "PersistenceMode", ["none", "volume", "snapshot (later)"], stereotype="enumeration")
    uml_class(d, 240, 520, 180, "StartStrategy", ["warm-ready", "warm-starting", "cold"], stereotype="enumeration")
    uml_class(d, 40, 690, 180, "SessionPhase", ["Pending", "Starting", "Ready", "Failed"],
              stereotype="enumeration")
    uml_class(d, 240, 690, 180, "InstanceState", ["starting", "idle", "claimed"], stereotype="enumeration")
    d.save()


def components():
    """Logical view, no technologies."""
    d = Diagram("components")
    # student lane across the top
    student = actor(d, 180, -140, "Student")
    artemis = component(d, 1100, -147, 200, 70, "Artemis", stereotype="subsystem", external=True)

    gw_box = (40, 40, 260, 100)
    gw = component(d, *gw_box, "Envoy Gateway", "login at the edge, routing",
                   stereotype="subsystem", external=True)
    kc = component(d, 400, 30, 240, 80, "Identity Provider", "logins, tokens",
                   stereotype="subsystem", external=True)

    landing = subsystem(d, 780, -20, 520, 200, "EduIDE\nlanding page")
    launcher = component(d, 800, 80, 230, 80, "Launcher", "launch from Artemis link")
    overview = component(d, 1050, 80, 230, 80, "Session Overview", "my sessions and workspaces")

    # control side around the cluster state hub
    service_box = (780, 260, 520, 190)
    service = subsystem(d, *service_box, "EduIDE\nService")
    launch_mgmt = component(d, 800, 350, 230, 80, "Launch Management", "validate, idempotent launch")
    feed = component(d, 1050, 350, 230, 80, "Overview Feed", "joined list, live updates")

    authz_box = (600, 520, 200, 100)
    authz = subsystem(d, *authz_box, "EduIDE\nSession Access", size=16)
    k8s = component(d, 980, 530, 220, 80, "Kubernetes API", "custom resources, watch",
                    stereotype="subsystem", external=True)
    scaler = subsystem(d, 1240, 520, 180, 100, "EduIDE\npool scaler", size=16)

    # session lane at the bottom, next to the operator
    session = subsystem(d, 40, 700, 480, 360, "EduIDE Session")
    agent = component(d, 100, 800, 220, 80, "Session Agent", "token check, data, activity")
    ide = component(d, 50, 960, 200, 80, "Student IDE", "editor, terminal, build")
    ext = component(d, 280, 960, 220, 80, "Session Extension", "data and warnings in the IDE")

    operator = subsystem(d, 660, 710, 680, 260, "EduIDE\nOperator")
    lifecycle = component(d, 680, 800, 200, 80, "Session Lifecycle", "start, timeouts, routes")
    pool_mgr = component(d, 940, 800, 190, 80, "Pool Manager", "prewarm, claim, pressure")
    ws_mgr = component(d, 1150, 800, 170, 80, "Workspace Manager", "volumes, retention")

    # Launch Entry Service: the student and Artemis' redirect use it
    entry = ball(d, 980, -112, "")
    d.text(785, -150, 180, 30, "<b>Launch Entry Service</b>", align="right", size=13)
    d.edge(launcher, entry, LINE, exit=(fx(800, 230, 980), 0), entry=(0.5, 1))
    d.edge(student, entry, SOCKET, exit=(1, 0.5), entry=(0, 0.5))
    d.edge(artemis, entry, SOCKET, exit=(0, 0.5), entry=(1, 0.5))

    # IDE Access Service above the gateway
    ide_access = ball(d, 100, -20, "")
    d.text(118, -40, 120, 40, "<b>IDE Access<br/>Service</b>", size=13)
    d.edge(gw, ide_access, LINE, exit=(fx(40, 260, 100), 0), entry=(0.5, 1))
    d.edge(student, ide_access, SOCKET, exit=(0, 0.5), entry=(0.5, 0), points=[(100, -112)])

    # Authentication Service between identity provider and landing page; one socket line, T junctions
    auth = ball(d, 700, 70, "")
    d.text(640, 18, 120, 40, "<b>Authentication<br/>Service</b>", align="center", size=13)
    d.edge(kc, auth, LINE, exit=(1, 0.5), entry=(0, 0.5))
    d.edge(landing, auth, SOCKET, exit=(0, frac((780, -20, 520, 200), 70)), entry=(1, 0.5))
    d.edge(gw, auth, SOCKET, exit=(1, frac(gw_box, 125)), entry=(1, 0.5), points=[(740, 125), (740, 70)])
    d.edge(service, auth, SOCKET, exit=(0, frac(service_box, 300)), entry=(1, 0.5),
           points=[(760, 300), (760, 70)])

    # Launch and Overview Service between landing page and service
    launch = ball(d, 980, 220, "Launch Service", label_w=120)
    d.edge(launch_mgmt, launch, LINE, exit=(fx(800, 230, 980), 0), entry=(0.5, 1))
    d.edge(launcher, launch, SOCKET, exit=(fx(800, 230, 980), 1), entry=(0.5, 0))
    ov = ball(d, 1165, 220, "Overview Service", label_w=130)
    d.edge(feed, ov, LINE, exit=(0.5, 0), entry=(0.5, 1))
    d.edge(overview, ov, SOCKET, exit=(0.5, 1), entry=(0.5, 0))

    # Session Access Service: the gateway asks it for every session request
    access = ball(d, 560, 570, "")
    d.text(440, 520, 120, 40, "<b>Session Access<br/>Service</b>", align="right", size=13)
    d.edge(authz, access, LINE, exit=(0, 0.5), entry=(1, 0.5))
    d.edge(gw, access, SOCKET, exit=(fx(40, 260, 280), 1), entry=(0, 0.5), points=[(280, 570)])

    # Proxied IDE Service: the agent is the only door into the pod
    proxied = ball(d, 210, 650, "")
    d.text(60, 630, 130, 40, "<b>Proxied IDE<br/>Service</b>", align="right", size=13)
    d.edge(agent, proxied, LINE, exit=(fx(100, 220, 210), 0), entry=(0.5, 1))
    d.edge(gw, proxied, SOCKET, exit=(fx(40, 260, 210), 1), entry=(0.5, 0))

    # inside the pod
    ide_svc = ball(d, 150, 920, "")
    d.text(50, 908, 85, 24, "<b>IDE Service</b>", align="right", size=12)
    d.edge(ide, ide_svc, LINE, exit=(fx(50, 200, 150), 0), entry=(0.5, 1))
    d.edge(agent, ide_svc, SOCKET, exit=(fx(100, 220, 150), 1), entry=(0.5, 0))
    data_svc = ball(d, 300, 920, "")
    d.text(316, 900, 110, 40, "<b>Session Data<br/>Service</b>", size=12)
    d.edge(agent, data_svc, LINE, exit=(fx(100, 220, 300), 1), entry=(0.5, 0))
    d.edge(ext, data_svc, SOCKET, exit=(fx(280, 220, 300), 0), entry=(0.5, 1))

    # Agent Control Service: the operator hands over the claim
    control = ball(d, 590, 840, "")
    d.text(535, 790, 110, 40, "<b>Agent Control<br/>Service</b>", align="center", size=13)
    d.edge(agent, control, LINE, exit=(1, 0.5), entry=(0, 0.5))
    d.edge(lifecycle, control, SOCKET, exit=(0, 0.5), entry=(1, 0.5))

    # Cluster State Service: the hub; authz straight, the others join with T junctions
    cluster = ball(d, 930, 570, "")
    d.text(905, 618, 120, 40, "<b>Cluster State<br/>Service</b>", size=13)
    d.edge(k8s, cluster, LINE, exit=(0, 0.5), entry=(1, 0.5))
    d.edge(authz, cluster, SOCKET, exit=(1, 0.5), entry=(0, 0.5))
    d.edge(service, cluster, SOCKET, exit=(fx(780, 520, 860), 1), entry=(0, 0.5), points=[(860, 570)])
    d.edge(scaler, cluster, SOCKET, exit=(fx(1240, 180, 1270), 0), entry=(0, 0.5),
           points=[(1270, 490), (860, 490), (860, 570)])
    d.edge(operator, cluster, SOCKET, exit=(fx(660, 680, 890), 0), entry=(0, 0.5), points=[(890, 570)])

    # operator internals
    claim = ball(d, 910, 840, "")
    d.text(850, 756, 120, 40, "<b>Instance Claim<br/>Service</b>", align="center", size=12)
    d.edge(pool_mgr, claim, LINE, exit=(0, 0.5), entry=(1, 0.5))
    d.edge(lifecycle, claim, SOCKET, exit=(1, 0.5), entry=(0, 0.5))
    volume = ball(d, 1235, 935, "")
    d.text(1000, 900, 190, 24, "<b>Volume Service</b>", align="right", size=12)
    d.edge(ws_mgr, volume, LINE, exit=(0.5, 1), entry=(0.5, 0))
    d.edge(lifecycle, volume, SOCKET, exit=(fx(680, 200, 800), 1), entry=(0, 0.5), points=[(800, 935)])

    legend(d, 340, 220, 300, [("subsystem", "Own subsystem"), ("component", "Component"),
                              ("external", "External system"), ("interface", "Required / provided service"),
                              ("responsibility", "Responsibility")])
    d.save()


def deployment():
    """One EduIDE deployment on the cluster; other deployments repeat the eduide namespace."""
    d = Diagram("deployment")
    kc_box = (40, 40, 220, 160)
    kc = node(d, *kc_box, "device", "Keycloak", "Keycloak · realm per deployment", fill=EXTERNAL_FILL)
    component(d, 55, 120, 190, 65, "Identity Provider", stereotype="subsystem", external=True)
    br_box = (40, 300, 220, 170)
    browser = node(d, *br_box, "executionEnvironment", "Browser", "React · TypeScript")
    subsystem(d, 55, 380, 190, 75, "EduIDE\nlanding page", size=14)
    artemis = node(d, 40, 600, 220, 150, "device", "Artemis", "Artemis", fill=EXTERNAL_FILL)
    component(d, 55, 670, 190, 60, "Artemis", stereotype="subsystem", external=True)

    node(d, 440, -40, 1500, 1090, "executionEnvironment", "Kubernetes Cluster (stud, parma, prod)",
         "Kubernetes · Calico or k3s NetworkPolicy")
    group(d, 480, 270, 290, 260, "<b>namespace envoy-gateway-system</b>")
    gw_box = (500, 310, 250, 200)
    gw = node(d, *gw_box, "pod", "envoy", "Envoy Gateway ≥ v1.8 · :443<br/>SecurityPolicy (OIDC, ext_authz)",
              fill=EXTERNAL_FILL)
    component(d, 515, 420, 220, 70, "Envoy Gateway", stereotype="subsystem", external=True)

    group(d, 860, 60, 650, 960, "<b>namespace eduide-&lt;deployment&gt;</b>")
    svc_box = (880, 110, 270, 170)
    svc = node(d, *svc_box, "pod", "service × 2", "Go · :8080 REST v1, SSE")
    subsystem(d, 895, 200, 240, 65, "EduIDE Service", size=14)
    az_box = (880, 330, 270, 170)
    authz = node(d, *az_box, "pod", "authz × 2", "Go · gRPC ext_authz :9001")
    subsystem(d, 895, 420, 240, 65, "EduIDE Session Access", size=14)
    se_box = (880, 580, 270, 220)
    sess = node(d, *se_box, "pod", "session (per session or pool instance)",
                "IDE image · IDE 127.0.0.1:3000<br/>agent (Go) :8080 proxy, :8081 control")
    subsystem(d, 895, 700, 240, 80, "EduIDE Session", size=14)
    sc_box = (1220, 110, 270, 170)
    scaler = node(d, *sc_box, "pod", "pool-scaler", "Go · :9090 metrics")
    subsystem(d, 1235, 200, 240, 65, "EduIDE pool scaler", size=14)
    op_box = (1220, 580, 270, 220)
    op = node(d, *op_box, "pod", "operator", "Go · controller-runtime<br/>leader election · :9464 metrics")
    subsystem(d, 1235, 700, 240, 80, "EduIDE Operator", size=14)

    gp_box = (1220, 850, 270, 150)
    gp = node(d, *gp_box, "pod", "git-proxy × 2 (with Git providers)", "Go · :8080 Git, :9090 metrics<br/>asks service :8083 (internal)")
    subsystem(d, 1235, 935, 240, 55, "EduIDE git-proxy", size=14)
    api_box = (1640, 330, 270, 170)
    api = node(d, *api_box, "executionEnvironment", "kube-apiserver", "Kubernetes · :6443", fill=EXTERNAL_FILL)
    component(d, 1655, 420, 240, 65, "Kubernetes API", stereotype="subsystem", external=True)

    # browser, Artemis, identity provider
    d.edge(artemis, browser, LINE, exit=(0.5, 0), entry=(0.5, 1))
    d.text(160, 500, 230, 40, "<b>Launch Entry Service</b><br/>HTTPS redirect, token in #fragment", size=12)
    d.edge(browser, kc, LINE, exit=(0.5, 0), entry=(0.5, 1))
    d.text(160, 220, 230, 40, "<b>Authentication Service</b><br/>HTTPS · OIDC", size=12)
    d.edge(browser, gw, LINE, exit=(1, frac(br_box, 420)), entry=(0, frac(gw_box, 420)))
    d.text(266, 350, 170, 66, "<b>IDE Access, Launch,<br/>Overview Service</b><br/>HTTPS :443", align="center", size=12)

    # gateway and service fetch keys and run OIDC against the identity provider
    d.edge(gw, kc, LINE, exit=(0, frac(gw_box, 335)), entry=(1, frac(kc_box, 160)),
           points=[(460, 335), (460, 160)])
    d.text(268, 162, 170, 40, "<b>Authentication Service</b><br/>HTTPS · OIDC", size=12)
    d.edge(svc, kc, LINE, exit=(fx(880, 270, 905), 0), entry=(1, frac(kc_box, 100)),
           points=[(905, 100)])
    d.text(268, 58, 170, 40, "<b>Authentication Service</b><br/>HTTPS · JWKS", size=12)

    # gateway into the namespace
    d.edge(gw, svc, LINE, exit=(fx(500, 250, 730), 0), entry=(0, frac(svc_box, 240)), points=[(730, 240)])
    d.text(740, 196, 116, 40, "<b>Launch, Overview</b><br/>HTTP :8080", size=12)
    d.edge(gw, authz, LINE, exit=(1, frac(gw_box, 410)), entry=(0, frac(az_box, 410)))
    d.text(772, 350, 86, 56, "<b>Session<br/>Access</b><br/>gRPC :9001", size=12, align="center")
    d.edge(gw, sess, LINE, exit=(0.5, 1), entry=(0, frac(se_box, 690)), points=[(625, 690)])
    d.text(636, 620, 220, 60, "<b>Proxied IDE Service</b><br/>HTTP + WebSocket :8080<br/>X-EduIDE-Instance-Token", size=12)

    # operator to the session pod
    d.edge(op, sess, LINE, exit=(0, frac(op_box, 650)), entry=(1, frac(se_box, 650)))
    d.text(1152, 600, 66, 48, "<b>Agent<br/>Control</b>", size=12, align="center")
    d.text(1152, 654, 66, 20, ":8081", size=12, align="center")

    # Git traffic of the session: agent to git-proxy, which calls GitHub or GitLab with the user's token
    d.edge(sess, gp, LINE, exit=(0.5, 1), entry=(0, 0.5), points=[(1015, 915)])
    d.text(1030, 880, 180, 36, "<b>Git Service</b><br/>HTTP :8080, instance token", size=12)

    # everyone watches the API server
    d.edge(svc, api, LINE, exit=(fx(880, 270, 1130), 0), entry=(fx(1640, 270, 1860), 0),
           points=[(1130, 20), (1860, 20)])
    d.text(1300, -2, 260, 20, "<b>Cluster State Service</b> · HTTPS :6443", size=12)
    d.edge(scaler, api, LINE, exit=(1, frac(sc_box, 200)), entry=(fx(1640, 270, 1700), 0),
           points=[(1700, 200)])
    d.text(1520, 156, 120, 40, "<b>Cluster State</b><br/>HTTPS :6443", size=12)
    d.edge(authz, api, LINE, exit=(1, frac(az_box, 420)), entry=(0, frac(api_box, 420)))
    d.text(1514, 376, 120, 40, "<b>Cluster State</b><br/>HTTPS :6443", size=12)
    d.edge(op, api, LINE, exit=(1, frac(op_box, 690)), entry=(fx(1640, 270, 1775), 1), points=[(1775, 690)])
    d.text(1514, 646, 120, 40, "<b>Cluster State</b><br/>HTTPS :6443", size=12)

    note(d, 1640, 730, 270, 120,
         "Observability (not drawn): all four Go binaries send OTLP traces to one Jaeger v2 per cluster "
         "and expose Prometheus metrics for Rancher Monitoring.")
    legend(d, 40, 800, 300, [("subsystem", "Own subsystem"), ("external", "External system"),
                             ("namespace", "Kubernetes namespace"), ("technology", "Technology, version, port"),
                             ("note", "Note")])
    d.save()


if __name__ == "__main__":
    object_model()
    components()
    deployment()

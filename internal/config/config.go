// Package config is the typed deployment configuration shared by operator,
// service and authz. The chart renders it into a ConfigMap (file config.yaml).
// It is a contract: change it only additively.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

type Config struct {
	// Deployment is a short name of this EduIDE deployment, used in metrics and traces.
	Deployment string `json:"deployment"`
	// Namespace the control plane manages; defaults to POD_NAMESPACE.
	Namespace string     `json:"namespace,omitempty"`
	Hosts     Hosts      `json:"hosts"`
	Gateway   GatewayRef `json:"gateway"`
	OIDC      OIDC       `json:"oidc"`
	// SessionsPerUser limits running sessions per user (G14). Default 3.
	SessionsPerUser int `json:"sessionsPerUser,omitempty"`
	// AllowedHosts for HTTPSURL keys such as GIT_URI and ARTEMIS_URL (G18).
	AllowedHosts []string   `json:"allowedHosts,omitempty"`
	Workspaces   Workspaces `json:"workspaces,omitempty"`
	Placement    Placement  `json:"placement,omitempty"`
	Capacity     Capacity   `json:"capacity,omitempty"`
	Telemetry    Telemetry  `json:"telemetry,omitempty"`
	Agent        Agent      `json:"agent"`
	// RateLimit per user for launches on the service.
	RateLimit RateLimit `json:"rateLimit,omitempty"`
	Overview  Overview  `json:"overview,omitempty"`
	// Admin tunes the read-only admin API (U16). The API is on only with oidc.admin.
	Admin Admin `json:"admin,omitempty"`
	// Security tunes response headers of the service.
	Security Security `json:"security,omitempty"`
}

// Security tunes response headers of the service.
type Security struct {
	// HSTSIncludeSubdomains adds includeSubDomains to the HSTS header of the service. Default true. It assumes
	// hosts.instance and hosts.webview are subdomains of hosts.service; set false when they are not.
	HSTSIncludeSubdomains *bool `json:"hstsIncludeSubdomains,omitempty"`
}

// Admin tunes the read-only admin API (U16).
type Admin struct {
	// PrometheusURL enables the figures and time series that need Prometheus. Empty hides them.
	PrometheusURL string     `json:"prometheusURL,omitempty"`
	Links         AdminLinks `json:"links,omitempty"`
	// RateLimitPerMinute per admin user over all admin routes. Default 60.
	RateLimitPerMinute int `json:"rateLimitPerMinute,omitempty"`
	// ScalerInterval is the decision interval of the pool scaler. Set it when a scaler runs; with PrometheusURL the
	// status then warns when the scaler's heartbeat metric is older than twice this time or missing.
	ScalerInterval *metav1.Duration `json:"scalerInterval,omitempty"`
}

// AdminLinks are shown in the admin panel. All optional.
type AdminLinks struct {
	Grafana string `json:"grafana,omitempty"`
	Jaeger  string `json:"jaeger,omitempty"`
	// TraceURLTemplate builds the link to one trace; {traceId} is replaced. Default: Jaeger URL + "/trace/{traceId}".
	TraceURLTemplate string `json:"traceUrlTemplate,omitempty"`
}

// Overview tunes the landing page overview (G15).
type Overview struct {
	// TitleStripPattern (RE2) is removed from the last path segment of display.source when the
	// service derives a title, e.g. a user suffix like "-[a-z]{2}[0-9]{2}[a-z]{3}$". Empty strips nothing.
	TitleStripPattern string `json:"titleStripPattern,omitempty"`
}

type Hosts struct {
	// Service is the host of the landing page and /api/v1.
	Service string `json:"service"`
	// Instance is the host all sessions share; sessions are paths on it (G2).
	Instance string `json:"instance"`
	// Webview is the fixed webview host (G3). Default "webview." + Instance.
	Webview string `json:"webview,omitempty"`
}

// GatewayRef is the parentRef of all session HTTPRoutes.
type GatewayRef struct {
	Name        string `json:"name"`
	Namespace   string `json:"namespace,omitempty"`
	SectionName string `json:"sectionName,omitempty"`
	// Port restricts the routes to listeners on this port (e.g. 443). 0 means any listener.
	Port int32 `json:"port,omitempty"`
}

type OIDC struct {
	IssuerURL string `json:"issuerURL"`
	// ClientIDs accepted as audience or azp of API tokens (landing page client).
	ClientIDs []string `json:"clientIDs"`
	// GatewayClientID is the confidential client of the SecurityPolicy; audience of the ID token cookie.
	GatewayClientID string `json:"gatewayClientID"`
	// UserClaim identifies the owner. Default preferred_username.
	UserClaim string `json:"userClaim,omitempty"`
	// NameClaim and EmailClaim feed the UserName and UserEmail key sources. Defaults name and email.
	NameClaim  string `json:"nameClaim,omitempty"`
	EmailClaim string `json:"emailClaim,omitempty"`
	// IDTokenCookie is the cookie name the SecurityPolicy stores the ID token in. Default eduide-id.
	IDTokenCookie string `json:"idTokenCookie,omitempty"`
	// Admin names the Keycloak role that opens the admin API (U16). Without it the admin API answers 404.
	Admin *AdminRole `json:"admin,omitempty"`
}

// AdminRole is a realm role or a client role in the access token.
type AdminRole struct {
	// RolePath is "realm_access.roles" or "resource_access.<client>.roles".
	RolePath string `json:"rolePath"`
	// Role is the role name that must be in the list at RolePath.
	Role string `json:"role"`
}

// A Keycloak client id may contain dots and URL characters, so the client part is any non-blank text.
var rolePathRE = regexp.MustCompile(`^(realm_access\.roles|resource_access\.\S+\.roles)$`)

type Workspaces struct {
	StorageClassName string             `json:"storageClassName,omitempty"`
	DefaultSize      *resource.Quantity `json:"defaultSize,omitempty"`
	// Retention after last use (G19). Default 14 days.
	Retention *metav1.Duration `json:"retention,omitempty"`
	// Grace between Expired and deletion. Default 24h.
	Grace *metav1.Duration `json:"grace,omitempty"`
}

// Placement is the deployment profile for session pods (U7).
type Placement struct {
	NodeSelector map[string]string   `json:"nodeSelector,omitempty"`
	Tolerations  []corev1.Toleration `json:"tolerations,omitempty"`
	Affinity     *corev1.Affinity    `json:"affinity,omitempty"`
	// NodePoolLabel is the node label key that names a node pool; capacity pressure is per value of it.
	NodePoolLabel string `json:"nodePoolLabel,omitempty"`
	// Prepull keeps template images warm on the nodes of this placement with one DaemonSet per template.
	Prepull Prepull `json:"prepull,omitempty"`
}

type Prepull struct {
	Enabled bool `json:"enabled,omitempty"`
	// PauseImage runs after the pull init container. Default registry.k8s.io/pause:3.10.
	PauseImage string `json:"pauseImage,omitempty"`
}

type Capacity struct {
	// ExhaustedAfter fails a session that cannot be placed for this long (G8). Default 45s.
	ExhaustedAfter *metav1.Duration `json:"exhaustedAfter,omitempty"`
}

type Telemetry struct {
	// OTLPEndpoint for traces (gRPC). Empty disables trace export.
	OTLPEndpoint string `json:"otlpEndpoint,omitempty"`
	// UserSaltFile holds the salt for pseudonymous user ids in spans (G17).
	UserSaltFile string `json:"userSaltFile,omitempty"`
}

// Agent describes the sidecar the operator injects into session pods.
type Agent struct {
	Image string `json:"image"`
	// LogLevel (debug, info or warn) becomes AGENT_LOG_LEVEL of the sidecar. Default info.
	LogLevel  string                      `json:"logLevel,omitempty"`
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
}

type RateLimit struct {
	// LaunchesPerMinute per user. Default 10.
	LaunchesPerMinute int `json:"launchesPerMinute,omitempty"`
}

// Load reads and validates the config file. Values are never logged.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- path comes from the operator's own flag
	if err != nil {
		return nil, err
	}
	return Parse(b)
}

func Parse(b []byte) (*Config, error) {
	c := &Config{}
	if err := yaml.UnmarshalStrict(b, c); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	c.defaults()
	return c, c.validate()
}

func (c *Config) defaults() {
	if c.Namespace == "" {
		c.Namespace = os.Getenv("POD_NAMESPACE")
	}
	if c.Hosts.Webview == "" && c.Hosts.Instance != "" {
		c.Hosts.Webview = "webview." + c.Hosts.Instance
	}
	if c.SessionsPerUser == 0 {
		c.SessionsPerUser = 3
	}
	if c.Security.HSTSIncludeSubdomains == nil {
		t := true
		c.Security.HSTSIncludeSubdomains = &t
	}
	if c.OIDC.UserClaim == "" {
		c.OIDC.UserClaim = "preferred_username"
	}
	if c.OIDC.NameClaim == "" {
		c.OIDC.NameClaim = "name"
	}
	if c.OIDC.EmailClaim == "" {
		c.OIDC.EmailClaim = "email"
	}
	if c.OIDC.IDTokenCookie == "" {
		c.OIDC.IDTokenCookie = "eduide-id"
	}
	if c.Workspaces.Retention == nil {
		c.Workspaces.Retention = &metav1.Duration{Duration: 14 * 24 * time.Hour}
	}
	if c.Workspaces.Grace == nil {
		c.Workspaces.Grace = &metav1.Duration{Duration: 24 * time.Hour}
	}
	if c.Workspaces.DefaultSize == nil {
		q := resource.MustParse("1Gi")
		c.Workspaces.DefaultSize = &q
	}
	if c.Capacity.ExhaustedAfter == nil {
		c.Capacity.ExhaustedAfter = &metav1.Duration{Duration: 45 * time.Second}
	}
	if c.Admin.RateLimitPerMinute == 0 {
		c.Admin.RateLimitPerMinute = 60
	}
	if c.RateLimit.LaunchesPerMinute == 0 {
		c.RateLimit.LaunchesPerMinute = 10
	}
	if c.Placement.Prepull.PauseImage == "" {
		c.Placement.Prepull.PauseImage = "registry.k8s.io/pause:3.10"
	}
	if c.Gateway.Namespace == "" {
		c.Gateway.Namespace = c.Namespace
	}
}

func (c *Config) validate() error {
	var errs []error
	req := func(v, name string) {
		if strings.TrimSpace(v) == "" {
			errs = append(errs, fmt.Errorf("config: %s is required", name))
		}
	}
	req(c.Deployment, "deployment")
	req(c.Namespace, "namespace (or POD_NAMESPACE)")
	req(c.Hosts.Service, "hosts.service")
	req(c.Hosts.Instance, "hosts.instance")
	req(c.Gateway.Name, "gateway.name")
	req(c.OIDC.IssuerURL, "oidc.issuerURL")
	req(c.OIDC.GatewayClientID, "oidc.gatewayClientID")
	req(c.Agent.Image, "agent.image")
	if c.Hosts.Service != "" && (strings.EqualFold(c.Hosts.Service, c.Hosts.Instance) || strings.EqualFold(c.Hosts.Service, c.Hosts.Webview)) {
		// The session routes catch every path of the instance and webview host (ADR 0005).
		errs = append(errs, errors.New("config: hosts.service must differ from hosts.instance and hosts.webview"))
	}
	if len(c.OIDC.ClientIDs) == 0 {
		errs = append(errs, errors.New("config: oidc.clientIDs needs at least one client"))
	}
	if !slices.Contains([]string{"", "debug", "info", "warn"}, c.Agent.LogLevel) {
		errs = append(errs, errors.New("config: agent.logLevel must be debug, info or warn"))
	}
	if c.SessionsPerUser < 1 {
		errs = append(errs, errors.New("config: sessionsPerUser must be at least 1"))
	}
	if _, err := regexp.Compile(c.Overview.TitleStripPattern); err != nil {
		errs = append(errs, fmt.Errorf("config: overview.titleStripPattern: %w", err))
	}
	errs = append(errs, c.validateAdmin()...)
	return errors.Join(errs...)
}

func (c *Config) validateAdmin() []error {
	var errs []error
	if a := c.OIDC.Admin; a != nil {
		if !rolePathRE.MatchString(a.RolePath) {
			errs = append(errs, errors.New("config: oidc.admin.rolePath must be realm_access.roles or resource_access.<client>.roles"))
		}
		if strings.TrimSpace(a.Role) == "" {
			errs = append(errs, errors.New("config: oidc.admin.role is required"))
		}
		// The admin API shows owners as a keyed hash; without a salt the hash of a user name is easy to reverse.
		if c.Telemetry.UserSaltFile == "" {
			errs = append(errs, errors.New("config: oidc.admin needs telemetry.userSaltFile (the key of the owner hash)"))
		}
	}
	if u := c.Admin.PrometheusURL; u != "" {
		p, err := url.Parse(u)
		if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" || p.User != nil || p.RawQuery != "" || p.Fragment != "" {
			errs = append(errs, errors.New("config: admin.prometheusURL must be an http(s) URL without credentials, query or fragment"))
		}
	}
	for name, u := range map[string]string{"grafana": c.Admin.Links.Grafana, "jaeger": c.Admin.Links.Jaeger} {
		if u != "" && !httpURL(u) {
			errs = append(errs, fmt.Errorf("config: admin.links.%s must be an http(s) URL", name))
		}
	}
	if t := c.Admin.Links.TraceURLTemplate; t != "" && (!strings.Contains(t, "{traceId}") || !httpURL(strings.ReplaceAll(t, "{traceId}", "x"))) {
		errs = append(errs, errors.New("config: admin.links.traceUrlTemplate must be an http(s) URL with {traceId}"))
	}
	if c.Admin.RateLimitPerMinute < 1 {
		errs = append(errs, errors.New("config: admin.rateLimitPerMinute must be at least 1"))
	}
	if d := c.Admin.ScalerInterval; d != nil && d.Duration <= 0 {
		errs = append(errs, errors.New("config: admin.scalerInterval must be positive"))
	}
	return errs
}

func httpURL(s string) bool {
	p, err := url.Parse(s)
	return err == nil && (p.Scheme == "http" || p.Scheme == "https") && p.Host != "" && p.User == nil
}

// InstanceURL is the URL of a session with the given instance id.
func (c *Config) InstanceURL(instance string) string {
	return "https://" + c.Hosts.Instance + "/" + instance + "/"
}

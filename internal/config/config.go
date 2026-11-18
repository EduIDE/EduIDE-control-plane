// Package config is the typed deployment configuration shared by operator,
// service and authz. The chart renders it into a ConfigMap (file config.yaml).
// It is a contract: change it only additively.
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
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
}

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
	Image     string                      `json:"image"`
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
	if c.SessionsPerUser < 1 {
		errs = append(errs, errors.New("config: sessionsPerUser must be at least 1"))
	}
	if _, err := regexp.Compile(c.Overview.TitleStripPattern); err != nil {
		errs = append(errs, fmt.Errorf("config: overview.titleStripPattern: %w", err))
	}
	return errors.Join(errs...)
}

// InstanceURL is the URL of a session with the given instance id.
func (c *Config) InstanceURL(instance string) string {
	return "https://" + c.Hosts.Instance + "/" + instance + "/"
}

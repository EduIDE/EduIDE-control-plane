package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
	"github.com/EduIDE/EduIDE-control-plane/internal/auth"
)

const (
	maxDataBytes    = 8 << 10
	maxBodyBytes    = 64 << 10
	defaultKeyBytes = 2048
)

// decodeLaunchRequest reads a launch body strictly: one JSON object, no unknown fields.
func decodeLaunchRequest(body io.Reader) (*LaunchRequest, *apiError) {
	var req LaunchRequest
	if perr := decodeJSON(body, &req); perr != nil {
		return nil, perr
	}
	if req.Template == "" || len(req.Template) > 63 {
		return nil, problem(CodeInvalidInput, "template is required (at most 63 characters)")
	}
	if req.Workspace != nil && len(*req.Workspace) > 63 {
		return nil, problem(CodeInvalidInput, "workspace is too long")
	}
	if req.Persistence != nil && !req.Persistence.Valid() {
		return nil, problem(CodeInvalidInput, "persistence must be none or volume")
	}
	return &req, nil
}

// decodeJSON reads one JSON object strictly: size limit, no unknown fields, no trailing data.
func decodeJSON(body io.Reader, v any) *apiError {
	raw, err := io.ReadAll(io.LimitReader(body, maxBodyBytes+1))
	if err != nil {
		return problem(CodeInvalidInput, "cannot read request body")
	}
	if len(raw) > maxBodyBytes {
		return problem(CodePayloadTooLarge, "request body too large")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return problem(CodeInvalidInput, "request body is not valid")
	}
	if dec.More() {
		return problem(CodeInvalidInput, "request body has trailing data")
	}
	return nil
}

// validateData checks the request data against the template allowlist (G18) and adds the
// keys that come from the token. The result is what goes into the data Secret.
func validateData(tpl *v1alpha1.IDETemplate, in map[string]string, user auth.User, allowedHosts []string) (map[string]string, *apiError) {
	total := 0
	for k, v := range in {
		total += len(k) + len(v)
	}
	if total > maxDataBytes {
		return nil, problem(CodePayloadTooLarge, "launch data exceeds 8 KiB")
	}
	keys := map[string]v1alpha1.InjectableKey{}
	for _, k := range tpl.Spec.InjectableKeys {
		keys[k.Name] = k
	}
	var variantIDs []string
	if img := tpl.Status.Image; img != nil {
		for _, v := range img.Variants {
			variantIDs = append(variantIDs, v.ID)
		}
	}
	out := map[string]string{}
	for name, val := range in {
		k, ok := keys[name]
		if !ok || k.Source != v1alpha1.SourceRequest && k.Source != "" {
			return nil, problem(CodeUnknownKey, "key "+quote(name)+" is not accepted by this template")
		}
		if name == "TEMPLATE" && len(k.Values) == 0 && len(variantIDs) > 0 {
			// U12: the variants of the image are the allowed values when the template lists none.
			if !slices.Contains(variantIDs, val) {
				return nil, problem(CodeInvalidInput, "invalid value for key TEMPLATE: not a variant of this template")
			}
			out[name] = val
			continue
		}
		if err := validateValue(k, val, allowedHosts); err != nil {
			return nil, problem(CodeInvalidInput, "invalid value for key "+name+": "+err.Error())
		}
		out[name] = val
	}
	for _, k := range tpl.Spec.InjectableKeys {
		switch k.Source {
		case v1alpha1.SourceUserName:
			if user.Name != "" {
				out[k.Name] = user.Name
			}
		case v1alpha1.SourceUserEmail:
			if user.Email != "" {
				out[k.Name] = user.Email
			}
		default:
			if _, ok := in[k.Name]; !ok && k.Required {
				return nil, problem(CodeInvalidInput, "key "+k.Name+" is required")
			}
		}
	}
	return out, nil
}

func quote(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	return strings.ToValidUTF8(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s), "?")
}

func validateValue(k v1alpha1.InjectableKey, val string, allowedHosts []string) error {
	maxLen := int(k.MaxLength)
	if maxLen == 0 {
		maxLen = defaultKeyBytes
	}
	if val == "" || len(val) > maxLen {
		return errors.New("empty or longer than " + strconv.Itoa(maxLen))
	}
	switch k.Format {
	case v1alpha1.FormatHTTPSURL:
		u, err := url.Parse(val)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" {
			return errors.New("must be an https URL")
		}
		if u.User != nil && !k.AllowCredentials {
			return errors.New("must be an https URL without credentials")
		}
		if !slices.ContainsFunc(allowedHosts, func(h string) bool { return strings.EqualFold(h, u.Hostname()) }) {
			return errors.New("host is not allowed")
		}
	case v1alpha1.FormatToken:
		for _, c := range []byte(val) {
			if c <= 0x20 || c >= 0x7f {
				return errors.New("must be printable ASCII without whitespace")
			}
		}
	case v1alpha1.FormatEnum:
		if !slices.Contains(k.Values, val) {
			return errors.New("not one of the allowed values")
		}
	case v1alpha1.FormatPattern:
		re, err := regexp.Compile(`^(?:` + k.Pattern + `)$`)
		if err != nil || !re.MatchString(val) {
			return errors.New("does not match the required pattern")
		}
	default:
		return errors.New("template key has no format")
	}
	return nil
}

// validateDisplay checks the non-secret display fields; the source must carry no credentials.
// withoutCredentials drops user:password from a URL, so a GIT_URI with an Artemis VCS token can still name its
// repository in display.source (which users and the admin API see) without the token.
func withoutCredentials(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	u.User = nil
	return u.String()
}

func validateDisplay(d *Display) *apiError {
	if d == nil {
		return nil
	}
	if d.Title != nil && len(*d.Title) > 200 {
		return problem(CodeInvalidInput, "display.title is longer than 200 characters")
	}
	if d.Source != nil {
		if len(*d.Source) > 2048 {
			return problem(CodeInvalidInput, "display.source is longer than 2048 characters")
		}
		// Clients may render the source as a link, so only plain http(s) URLs (no javascript: or data:).
		u, err := url.Parse(*d.Source)
		if *d.Source != "" && (err != nil || u.Scheme != "https" && u.Scheme != "http" || u.Host == "" ||
			u.User != nil || u.RawQuery != "" || u.Fragment != "") {
			return problem(CodeInvalidInput, "display.source must be an http(s) URL without credentials, query or fragment")
		}
	}
	return nil
}

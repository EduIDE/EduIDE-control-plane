package controller

import (
	"fmt"
	"strings"

	"k8s.io/client-go/discovery"

	v1 "github.com/EduIDE/EduIDE-control-plane/api/v1alpha1"
)

// requiredResources are the API resources this operator version needs, with the subresources it writes.
var requiredResources = []string{
	"idetemplates", "idetemplates/status",
	"idepools", "idepools/status",
	"idesessions", "idesessions/status",
	"ideworkspaces", "ideworkspaces/status",
}

// CheckCRDs fails when the cluster does not serve the EduIDE API this operator needs (G10).
// It uses discovery, which a namespaced Role allows, instead of reading the CRDs.
func CheckCRDs(dc discovery.DiscoveryInterface) error {
	gv := v1.GroupVersion.String()
	hint := "install or upgrade the eduide-control-plane-crds chart before this operator"
	l, err := dc.ServerResourcesForGroupVersion(gv)
	if err != nil {
		return fmt.Errorf("the cluster does not serve %s (%w); %s", gv, err, hint)
	}
	have := map[string]bool{}
	for _, r := range l.APIResources {
		have[r.Name] = true
	}
	var missing []string
	for _, r := range requiredResources {
		if !have[r] {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("the cluster serves %s without %s; %s", gv, strings.Join(missing, ", "), hint)
	}
	return nil
}

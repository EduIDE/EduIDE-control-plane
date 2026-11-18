// crddiff fails when a CRD schema in config/crd removes a field, adds a required
// field or drops a served version compared to a git revision (ADR 0011, G10).
// Usage: go run ./hack/crddiff <base-rev>
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: crddiff <base-rev>")
		os.Exit(2)
	}
	files, _ := filepath.Glob("config/crd/*.yaml")
	var problems []string
	for _, f := range files {
		base, err := exec.CommandContext(context.Background(), "git", "show", os.Args[1]+":"+f).Output() // #nosec G204 G702 -- developer tool, revision from CI
		if err != nil {
			continue // new CRD
		}
		head, err := os.ReadFile(f) // #nosec G304 -- fixed directory
		if err != nil {
			panic(err)
		}
		problems = append(problems, compare(f, base, head)...)
	}
	if len(problems) > 0 {
		fmt.Println(strings.Join(problems, "\n"))
		os.Exit(1)
	}
	fmt.Println("crddiff: no breaking schema changes")
}

func compare(name string, baseYAML, headYAML []byte) []string {
	var base, head apiextv1.CustomResourceDefinition
	if err := yaml.Unmarshal(baseYAML, &base); err != nil {
		return []string{name + ": " + err.Error()}
	}
	if err := yaml.Unmarshal(headYAML, &head); err != nil {
		return []string{name + ": " + err.Error()}
	}
	var out []string
	for _, bv := range base.Spec.Versions {
		var hv *apiextv1.CustomResourceDefinitionVersion
		for i := range head.Spec.Versions {
			if head.Spec.Versions[i].Name == bv.Name {
				hv = &head.Spec.Versions[i]
			}
		}
		if hv == nil || (bv.Served && !hv.Served) {
			out = append(out, fmt.Sprintf("%s: version %s no longer served", name, bv.Name))
			continue
		}
		bp, hp := map[string]bool{}, map[string]bool{}
		breq, hreq := map[string]bool{}, map[string]bool{}
		walk("", bv.Schema.OpenAPIV3Schema, bp, breq)
		walk("", hv.Schema.OpenAPIV3Schema, hp, hreq)
		for _, p := range sortedKeys(bp) {
			if !hp[p] {
				out = append(out, fmt.Sprintf("%s %s: field %s removed", name, bv.Name, p))
			}
		}
		for _, p := range sortedKeys(hreq) {
			if !breq[p] && !strings.HasPrefix(p, ".status") {
				out = append(out, fmt.Sprintf("%s %s: field %s became required", name, bv.Name, p))
			}
		}
	}
	return out
}

func walk(path string, s *apiextv1.JSONSchemaProps, paths, required map[string]bool) {
	if s == nil {
		return
	}
	for _, r := range s.Required {
		required[path+"."+r] = true
	}
	for k, v := range s.Properties {
		p := path + "." + k
		paths[p] = true
		walk(p, &v, paths, required)
	}
	if s.Items != nil && s.Items.Schema != nil {
		walk(path+"[]", s.Items.Schema, paths, required)
	}
}

func sortedKeys(m map[string]bool) []string {
	k := make([]string, 0, len(m))
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

package main

import (
	"strings"
	"testing"
)

const crd = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
spec:
  versions:
  - name: v1alpha1
    served: true
    schema:
      openAPIV3Schema:
        type: object
        properties:
          spec:
            type: object
            required: [%REQ%]
            properties:
              a: {type: string}
              %B%
`

func doc(req, b string) []byte {
	return []byte(strings.NewReplacer("%REQ%", req, "%B%", b).Replace(crd))
}

func TestCompare(t *testing.T) {
	base := doc("a", "b: {type: string}")
	if p := compare("x", base, doc("a", "b: {type: string}\n              c: {type: string}")); len(p) != 0 {
		t.Errorf("additive change flagged: %v", p)
	}
	if p := compare("x", base, doc("a", "")); len(p) != 1 || !strings.Contains(p[0], ".spec.b removed") {
		t.Errorf("removal not flagged: %v", p)
	}
	if p := compare("x", base, doc("a, b", "b: {type: string}")); len(p) != 1 || !strings.Contains(p[0], "became required") {
		t.Errorf("new required field not flagged: %v", p)
	}
}

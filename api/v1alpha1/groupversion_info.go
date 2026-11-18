// Package v1alpha1 contains the EduIDE control plane API types.
// +kubebuilder:object:generate=true
// +groupName=eduide.github.io
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	GroupVersion  = schema.GroupVersion{Group: "eduide.github.io", Version: "v1alpha1"}
	SchemeBuilder = runtime.NewSchemeBuilder(func(s *runtime.Scheme) error {
		s.AddKnownTypes(GroupVersion, objects...)
		metav1.AddToGroupVersion(s, GroupVersion)
		return nil
	})
	AddToScheme = SchemeBuilder.AddToScheme
	objects     []runtime.Object
)

func register(o ...runtime.Object) { objects = append(objects, o...) }

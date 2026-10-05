/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package egressip is the thin, self-contained seam between IPAllocationPolicy and the
// OVN-Kubernetes EgressIP API (k8s.ovn.org/v1): identifying the kind, discovering once
// whether the cluster serves it, and reading the fields the policy needs out of an object
// read as unstructured data. Per DESIGN.md 4.7 the EgressIP is "read as unstructured
// data", so this package never imports the OVN-Kubernetes Go module. The allocation
// logic (the claim view, the planner and the spec.egressIPs merge patches) lives in
// internal/ipalloc; the controller wires the two together.
package egressip

import (
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

// GVK identifies the EgressIP kind this operator reads and patches. EgressIP is
// cluster-scoped.
var GVK = schema.GroupVersionKind{Group: "k8s.ovn.org", Version: "v1", Kind: "EgressIP"}

// ListGVK identifies the list kind used to list EgressIPs as unstructured data.
var ListGVK = schema.GroupVersionKind{Group: "k8s.ovn.org", Version: "v1", Kind: "EgressIPList"}

// ResourceName is the plural resource name Available looks for within the
// k8s.ovn.org/v1 group version.
const ResourceName = "egressips"

// Available reports whether the cluster identified by cfg serves the k8s.ovn.org/v1 API
// group with an "egressips" resource. It performs a single discovery call and does not
// cache the result, mirroring internal/nmstateapi.Available: a cluster that does not run
// OVN-Kubernetes is reported as (false, nil), not an error; any other discovery failure
// is returned as an error so callers can tell "no EgressIP here" apart from "discovery is
// broken".
func Available(cfg *rest.Config) (bool, error) {
	client, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return false, err
	}

	resources, err := client.ServerResourcesForGroupVersion(GVK.GroupVersion().String())
	if err != nil {
		if apierrors.IsNotFound(err) || discovery.IsGroupDiscoveryFailedError(err) {
			return false, nil
		}
		return false, err
	}

	for _, r := range resources.APIResources {
		if r.Name == ResourceName {
			return true, nil
		}
	}
	return false, nil
}

// New returns an empty EgressIP carrying only its GroupVersionKind, ready for a client Get.
func New() *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(GVK)
	return obj
}

// NewList returns an empty EgressIP list carrying its GroupVersionKind, ready for a client
// List.
func NewList() *unstructured.UnstructuredList {
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(ListGVK)
	return list
}

// EgressIPs returns spec.egressIPs, the addresses the EgressIP currently requests, each
// trimmed of surrounding space, with empty entries dropped and the order kept. found is
// false when obj is nil or the field is absent. An error means the field exists but is
// not a list of strings.
func EgressIPs(obj *unstructured.Unstructured) (ips []string, found bool, err error) {
	if obj == nil {
		return nil, false, nil
	}
	raw, found, err := unstructured.NestedFieldNoCopy(obj.Object, "spec", "egressIPs")
	if err != nil {
		return nil, false, fmt.Errorf("spec.egressIPs: %w", err)
	}
	if !found || raw == nil {
		return nil, false, nil
	}
	list, ok := raw.([]interface{})
	if !ok {
		return nil, true, fmt.Errorf("spec.egressIPs is %T, not a list", raw)
	}
	for i, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, true, fmt.Errorf("spec.egressIPs[%d] is %T, not a string", i, item)
		}
		if s = strings.TrimSpace(s); s != "" {
			ips = append(ips, s)
		}
	}
	return ips, true, nil
}

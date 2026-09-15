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

// Package nmstateapi is the thin, self-contained seam between HostRegistrationPolicy and
// the NMState NodeNetworkState API (nmstate.io/v1beta1): identifying the kind, discovering
// once whether the cluster serves it at all, and pulling status.currentState out of an
// object read as unstructured data. Per DESIGN.md 4.7, "NodeNetworkState is read as
// unstructured data; the operator takes no dependency on the NMState Go module", so this
// package (deliberately unlike internal/routeapi, which imports the upstream Go types)
// never imports github.com/nmstate/kubernetes-nmstate. Nothing here talks to
// internal/hostreg (see nmstate.go there for the field-by-field MAC extraction); the
// controller wires the two together.
package nmstateapi

import (
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

// GVK identifies the NodeNetworkState kind this operator reads.
var GVK = schema.GroupVersionKind{Group: "nmstate.io", Version: "v1beta1", Kind: "NodeNetworkState"}

// NodeNetworkStateResourceName is the plural resource name Available looks for within the
// nmstate.io/v1beta1 group version.
const NodeNetworkStateResourceName = "nodenetworkstates"

// Available reports whether the cluster identified by cfg serves the nmstate.io/v1beta1
// API group with a "nodenetworkstates" resource. It performs a single discovery call and
// does not cache the result, mirroring internal/routeapi.Available: a cluster that simply
// does not run the NMState operator is reported as (false, nil), not an error; any other
// discovery failure is returned as an error so callers can tell "no NMState here" apart
// from "discovery is broken".
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
		if r.Name == NodeNetworkStateResourceName {
			return true, nil
		}
	}
	return false, nil
}

// CurrentState extracts status.currentState from a NodeNetworkState read as unstructured
// data. ok is false when obj is nil or the field is absent, not a map, or malformed
// (e.g. the NMState handler has not reported anything for that Node yet).
func CurrentState(obj *unstructured.Unstructured) (map[string]interface{}, bool) {
	if obj == nil {
		return nil, false
	}
	m, found, err := unstructured.NestedMap(obj.Object, "status", "currentState")
	if err != nil || !found {
		return nil, false
	}
	return m, true
}

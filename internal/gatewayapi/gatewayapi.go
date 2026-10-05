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

// Package gatewayapi is the thin, self-contained seam between DNSPublishPolicy's HTTPRoute
// source and the Kubernetes Gateway API (gateway.networking.k8s.io/v1): identifying the
// Gateway and HTTPRoute kinds, discovering once whether the cluster serves them at all, and
// pulling hostnames, parent references, per-parent acceptance and Gateway addresses out of
// objects read as unstructured data. Per DESIGN.md 4.7, "HTTPRoute is read as unstructured
// data so the operator takes no Gateway API module dependency", so this package (like
// internal/nmstateapi, unlike internal/routeapi) never imports sigs.k8s.io/gateway-api.
//
// The field names below follow the Gateway API reference at
// https://gateway-api.sigs.k8s.io/reference/api-spec/ for gateway.networking.k8s.io/v1.
package gatewayapi

import (
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

// Group is the Gateway API group; it is also the default group of a parentRef.
const Group = "gateway.networking.k8s.io"

// Version is the Gateway API version this operator reads.
const Version = "v1"

var (
	// GatewayGVK identifies the Gateway kind.
	GatewayGVK = schema.GroupVersionKind{Group: Group, Version: Version, Kind: "Gateway"}
	// HTTPRouteGVK identifies the HTTPRoute kind.
	HTTPRouteGVK = schema.GroupVersionKind{Group: Group, Version: Version, Kind: "HTTPRoute"}
)

// Plural resource names Available looks for within the gateway.networking.k8s.io/v1 group
// version.
const (
	GatewayResourceName   = "gateways"
	HTTPRouteResourceName = "httproutes"
)

// Available reports whether the cluster identified by cfg serves gateway.networking.k8s.io/v1
// with both a "gateways" and an "httproutes" resource; the HTTPRoute source needs both, since
// a route's addresses come from its parent Gateways. It performs a single discovery call and
// does not cache the result, mirroring internal/routeapi.Available: a cluster that simply
// does not install the Gateway API is reported as (false, nil), not an error; any other
// discovery failure is returned as an error so callers can tell "no Gateway API here" apart
// from "discovery is broken".
func Available(cfg *rest.Config) (bool, error) {
	client, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return false, err
	}

	resources, err := client.ServerResourcesForGroupVersion(GatewayGVK.GroupVersion().String())
	if err != nil {
		if apierrors.IsNotFound(err) || discovery.IsGroupDiscoveryFailedError(err) {
			return false, nil
		}
		return false, err
	}

	var haveGateways, haveHTTPRoutes bool
	for _, r := range resources.APIResources {
		switch r.Name {
		case GatewayResourceName:
			haveGateways = true
		case HTTPRouteResourceName:
			haveHTTPRoutes = true
		}
	}
	return haveGateways && haveHTTPRoutes, nil
}

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

// Package ingressapi is the thin, self-contained seam between DNSPublishPolicy and the
// OpenShift ingress operator API (github.com/openshift/api/operatoringress/v1, group
// ingress.operator.openshift.io): registering the DNSRecord type with a scheme, and
// discovering once whether the cluster serves it at all. Per DESIGN.md 4.7, DNSRecord is
// detected with discovery at startup like Routes, so on a cluster without it (kind) the
// DNSRecord source is inert and logged. Nothing here talks to internal/publish; the
// controller wires the two together.
package ingressapi

import (
	operatoringressv1 "github.com/openshift/api/operatoringress/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

// DNSRecordResourceName is the plural resource name Available looks for within the
// ingress.operator.openshift.io/v1 group version.
const DNSRecordResourceName = "dnsrecords"

// AddToScheme registers ingress.operator.openshift.io/v1 (DNSRecord and DNSRecordList) with
// s, so a manager built from that scheme can watch, get and list DNSRecords. It also
// registers the config.openshift.io types the DNSRecord status refers to, which the
// openshift/api package installs together with its own.
func AddToScheme(s *runtime.Scheme) error {
	return operatoringressv1.Install(s)
}

// Available reports whether the cluster identified by cfg serves the
// ingress.operator.openshift.io/v1 API group with a "dnsrecords" resource. It performs a
// single discovery call and does not cache the result, so callers that only need this once
// (typically at manager startup) should call it once and store the answer themselves. A
// cluster that simply does not have the API (a vanilla Kubernetes/kind cluster rather than
// OpenShift) is reported as (false, nil), not an error; any other discovery failure is
// returned as an error so callers can tell "no DNSRecords here" apart from "discovery is
// broken".
func Available(cfg *rest.Config) (bool, error) {
	client, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return false, err
	}

	resources, err := client.ServerResourcesForGroupVersion(operatoringressv1.GroupVersion.String())
	if err != nil {
		if apierrors.IsNotFound(err) || discovery.IsGroupDiscoveryFailedError(err) {
			return false, nil
		}
		return false, err
	}

	for _, r := range resources.APIResources {
		if r.Name == DNSRecordResourceName {
			return true, nil
		}
	}
	return false, nil
}

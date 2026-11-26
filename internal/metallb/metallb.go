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

// Package metallb is the thin, self-contained seam between IPAllocationPolicy and the
// MetalLB IPAddressPool API (metallb.io/v1beta1). MetalLB only announces an address that
// lies in one of its IPAddressPools, so with spec.metallb.mirrorPools the controller
// mirrors every Subnet, Range and AddressBlock the policy uses into IPAddressPools (see
// DESIGN.md 4.7, "MetalLB pool mirroring"). This package holds everything that does not
// need a client: identifying the kind, discovering once whether the cluster serves it at
// all, and pure builders for the desired pool objects, their names and labels, the label
// selector that lists a policy's mirrored pools and the set of mirrored pools to prune.
//
// Like internal/nmstateapi it works on *unstructured.Unstructured and takes no dependency
// on the MetalLB Go module, and, so that it does not depend on the IPAllocationPolicy API
// type, its builders take plain strings. The controller wires it to the policy: it
// decides which pools the policy uses, calls Desired for each, creates or updates the
// objects (using Equal to skip no-ops), lists the existing ones with Selector and deletes
// what Stale returns.
package metallb

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
)

// GVK identifies the IPAddressPool kind this operator mirrors pools into.
var GVK = schema.GroupVersionKind{Group: "metallb.io", Version: "v1beta1", Kind: "IPAddressPool"}

// ListGVK identifies the list kind, for listing a policy's mirrored pools as unstructured
// data.
var ListGVK = schema.GroupVersionKind{Group: GVK.Group, Version: GVK.Version, Kind: GVK.Kind + "List"}

// ResourceName is the plural resource name Available looks for within the metallb.io/v1beta1
// group version.
const ResourceName = "ipaddresspools"

// Labels stamped on every mirrored pool. A pool in another namespace cannot carry an owner
// reference to the policy, so these labels are how the controller finds a policy's pools
// again. LabelPolicy equals ipalloc.LabelPolicy, the label already stamped on generated
// Addresses.
const (
	LabelPolicy          = "uddi.infoblox.com/policy"
	LabelPolicyNamespace = "uddi.infoblox.com/policy-namespace"
	LabelManagedBy       = "app.kubernetes.io/managed-by"

	// ManagedBy is the LabelManagedBy value of every mirrored pool.
	ManagedBy = "uddi-operator"
)

const (
	// namePrefix starts every mirrored pool name.
	namePrefix = "uddi"
	// nameHashLength is the number of hex digits of the sha256 of the full name appended to a
	// name that has to be truncated.
	nameHashLength = 10
)

// Available reports whether the cluster identified by cfg serves the metallb.io/v1beta1 API
// group with an "ipaddresspools" resource. It performs a single discovery call and does not
// cache the result, mirroring internal/nmstateapi.Available: a cluster that simply does not
// run MetalLB is reported as (false, nil), not an error; any other discovery failure is
// returned as an error so callers can tell "no MetalLB here" apart from "discovery is
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

// Source is one pool the policy uses, as the planner resolved it: the kind and name of the
// pool CR and the addresses it covers. Exactly one of CIDR (a Subnet or AddressBlock) or
// Start and End (a Range) is set.
type Source struct {
	// PoolKind is the kind of the pool CR: Subnet, Range or AddressBlock. Only its lower-case
	// form is used, in the mirrored pool's name.
	PoolKind string
	// PoolName is the name of the pool CR in the policy's namespace.
	PoolName string
	// CIDR is the Subnet or AddressBlock network, for example "10.20.0.0/24".
	CIDR string
	// Start and End are the first and last address of a Range, inclusive.
	Start string
	End   string
}

// PoolName returns the name of the IPAddressPool that mirrors the pool poolKind/poolName of
// the policy: uddi-<policy namespace>-<policy>-<pool kind lower-case>-<pool name>. Every
// part is itself a DNS-1123 name, so the result is a valid DNS-1123 subdomain, and the
// names of pools of different kinds, policies or namespaces differ. A name above 253
// characters is truncated and ends in "-" plus the first 10 hex digits of the sha256 of
// the full name, so two long names that share a prefix stay distinct.
func PoolName(policyNamespace, policy, poolKind, poolName string) string {
	full := strings.Join([]string{namePrefix, policyNamespace, policy, strings.ToLower(poolKind), poolName}, "-")
	if len(full) <= validation.DNS1123SubdomainMaxLength {
		return full
	}
	sum := sha256.Sum256([]byte(full))
	keep := validation.DNS1123SubdomainMaxLength - nameHashLength - 1
	// Cutting mid-name can leave a "-" or "." at the end of the prefix; the suffix has to
	// follow an alphanumeric to keep the name valid.
	return strings.TrimRight(full[:keep], "-.") + "-" + hex.EncodeToString(sum[:])[:nameHashLength]
}

// Desired builds the IPAddressPool that mirrors src for the policy into mirrorNamespace:
// the name from PoolName, the three labels Selector matches, spec.addresses holding the
// CIDR or the "start-end" range, spec.autoAssign false so MetalLB never hands these
// addresses out on its own (they are allocated by Universal DDI; MetalLB only has to
// announce them) and spec.serviceAllocation.namespaces holding only the policy namespace,
// so no Service of another namespace can request an address from the pool. It rejects inputs MetalLB would reject: an unparsable CIDR or address, a
// range whose start is after its end or whose ends are of different families, and a
// source that sets both or neither of CIDR and Start/End. A CIDR is written in its
// canonical network form ("10.20.0.7/24" becomes "10.20.0.0/24"), which is how MetalLB
// reads it.
func Desired(policyNamespace, policy, mirrorNamespace string, src Source) (*unstructured.Unstructured, error) {
	required := []struct{ field, value string }{
		{"policy namespace", policyNamespace},
		{"policy", policy},
		{"mirror namespace", mirrorNamespace},
		{"pool kind", src.PoolKind},
		{"pool name", src.PoolName},
	}
	for _, r := range required {
		if r.value == "" {
			return nil, fmt.Errorf("%s must not be empty", r.field)
		}
	}
	if errs := validation.IsDNS1123Label(mirrorNamespace); len(errs) > 0 {
		return nil, fmt.Errorf("mirror namespace %q is not a valid namespace name: %s", mirrorNamespace, strings.Join(errs, "; "))
	}
	// The labels carry the policy name and namespace verbatim, so a policy whose name is not a
	// valid label value (above 63 characters) cannot be mirrored.
	for _, r := range required[:2] {
		if errs := validation.IsValidLabelValue(r.value); len(errs) > 0 {
			return nil, fmt.Errorf("%s %q is not a valid label value: %s", r.field, r.value, strings.Join(errs, "; "))
		}
	}
	name := PoolName(policyNamespace, policy, src.PoolKind, src.PoolName)
	if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
		return nil, fmt.Errorf("mirrored pool name %q is not a valid name: %s", name, strings.Join(errs, "; "))
	}

	address, err := src.address()
	if err != nil {
		return nil, fmt.Errorf("%s/%s: %w", src.PoolKind, src.PoolName, err)
	}

	pool := &unstructured.Unstructured{Object: map[string]interface{}{}}
	pool.SetGroupVersionKind(GVK)
	pool.SetNamespace(mirrorNamespace)
	pool.SetName(name)
	pool.SetLabels(map[string]string{
		LabelPolicy:          policy,
		LabelPolicyNamespace: policyNamespace,
		LabelManagedBy:       ManagedBy,
	})
	// Unstructured objects have to hold JSON-compatible types, so the list is []interface{}.
	if err := unstructured.SetNestedSlice(pool.Object, []interface{}{address}, "spec", "addresses"); err != nil {
		return nil, err
	}
	if err := unstructured.SetNestedField(pool.Object, false, "spec", "autoAssign"); err != nil {
		return nil, err
	}
	if err := unstructured.SetNestedSlice(pool.Object, []interface{}{policyNamespace}, "spec", "serviceAllocation", "namespaces"); err != nil {
		return nil, err
	}
	return pool, nil
}

// address renders the source as the one spec.addresses entry MetalLB reads: a CIDR or a
// "start-end" range.
func (s Source) address() (string, error) {
	isRange := s.Start != "" || s.End != ""
	switch {
	case s.CIDR != "" && isRange:
		return "", errors.New("set either a CIDR or a start and end address, not both")
	case s.CIDR != "":
		return cidr(s.CIDR)
	case isRange:
		return addressRange(s.Start, s.End)
	default:
		return "", errors.New("no CIDR or start and end address given")
	}
}

func cidr(s string) (string, error) {
	prefix, err := netip.ParsePrefix(s)
	if err != nil {
		return "", fmt.Errorf("invalid CIDR %q: %w", s, err)
	}
	if prefix.Addr().Is4In6() {
		return "", fmt.Errorf("invalid CIDR %q: IPv4-mapped IPv6 prefixes are not supported", s)
	}
	return prefix.Masked().String(), nil
}

func addressRange(start, end string) (string, error) {
	if start == "" || end == "" {
		return "", fmt.Errorf("a range needs both a start and an end address, got %q and %q", start, end)
	}
	first, err := parseAddr(start)
	if err != nil {
		return "", fmt.Errorf("invalid range start: %w", err)
	}
	last, err := parseAddr(end)
	if err != nil {
		return "", fmt.Errorf("invalid range end: %w", err)
	}
	if first.Is4() != last.Is4() {
		return "", fmt.Errorf("range %s-%s mixes IPv4 and IPv6 addresses", start, end)
	}
	if first.Compare(last) > 0 {
		return "", fmt.Errorf("range start %s is after its end %s", start, end)
	}
	return first.String() + "-" + last.String(), nil
}

func parseAddr(s string) (netip.Addr, error) {
	addr, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("invalid IP address %q: %w", s, err)
	}
	if addr.Zone() != "" {
		return netip.Addr{}, fmt.Errorf("invalid IP address %q: zones are not supported", s)
	}
	if addr.Is4In6() {
		return netip.Addr{}, fmt.Errorf("invalid IP address %q: IPv4-mapped IPv6 addresses are not supported", s)
	}
	return addr, nil
}

// Selector selects the IPAddressPools the policy mirrored: those carrying both of its
// policy labels and the managed-by label. Use it to list the existing pools in the mirror
// namespace, for pruning and for the finalizer.
func Selector(policyNamespace, policy string) labels.Selector {
	return labels.SelectorFromSet(labels.Set{
		LabelPolicy:          policy,
		LabelPolicyNamespace: policyNamespace,
		LabelManagedBy:       ManagedBy,
	})
}

// OwnedBy reports whether pool carries the labels of the mirror of the policy. Desired
// names are built from the policy's namespace and name joined with "-", so two different
// policies can in principle map to the same name ("a-b"/"c" and "a"/"b-c"); the controller
// has to check this before it updates an existing pool, and must not take over a pool it
// did not mirror, such as one a user created under the same name.
func OwnedBy(pool *unstructured.Unstructured, policyNamespace, policy string) bool {
	return pool != nil && Selector(policyNamespace, policy).Matches(labels.Set(pool.GetLabels()))
}

// Stale returns, sorted, the names of the pools in existing that the policy mirrored
// earlier and that are no longer desired: they carry the policy's labels (see Selector)
// and their name is not a key of desiredNames with a true value. Pools of other policies
// and pools without the labels, such as ones a user created, are never returned, even when
// existing was listed without Selector.
func Stale(policyNamespace, policy string, existing []unstructured.Unstructured, desiredNames map[string]bool) []string {
	var stale []string
	for i := range existing {
		pool := &existing[i]
		if OwnedBy(pool, policyNamespace, policy) && !desiredNames[pool.GetName()] {
			stale = append(stale, pool.GetName())
		}
	}
	slices.Sort(stale)
	return stale
}

// Equal reports whether a and b agree on every field this package manages: the three
// labels Desired sets, spec.addresses, spec.autoAssign and
// spec.serviceAllocation.namespaces. Labels and spec fields that someone else owns (other
// labels, the rest of serviceAllocation, the avoidBuggyIPs MetalLB defaults in, the
// status) do not count, so the controller can skip the update when the live pool
// already matches Desired. It follows that an update must keep those fields, by patching
// or applying only the managed ones, rather than replace the whole object.
func Equal(a, b *unstructured.Unstructured) bool {
	if a == nil || b == nil {
		return a == b
	}
	aLabels, bLabels := a.GetLabels(), b.GetLabels()
	for _, key := range []string{LabelPolicy, LabelPolicyNamespace, LabelManagedBy} {
		aValue, aFound := aLabels[key]
		bValue, bFound := bLabels[key]
		if aFound != bFound || aValue != bValue {
			return false
		}
	}

	for _, path := range [][]string{{"spec", "addresses"}, {"spec", "serviceAllocation", "namespaces"}} {
		aList, aFound, aErr := unstructured.NestedStringSlice(a.Object, path...)
		bList, bFound, bErr := unstructured.NestedStringSlice(b.Object, path...)
		if aErr != nil || bErr != nil || aFound != bFound || !slices.Equal(aList, bList) {
			return false
		}
	}

	// An absent autoAssign means MetalLB's default (true), which is not the same as false.
	aAuto, aFound, aErr := unstructured.NestedBool(a.Object, "spec", "autoAssign")
	bAuto, bFound, bErr := unstructured.NestedBool(b.Object, "spec", "autoAssign")
	return aErr == nil && bErr == nil && aFound == bFound && aAuto == bAuto
}

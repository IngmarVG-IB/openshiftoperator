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

package ipalloc

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

// maxNameLength is the Kubernetes object name length limit AddressName must respect.
const maxNameLength = 253

// nameHashLength is how many hex characters of the sha256 digest AddressName keeps
// when the plain "<policy>-<service>" name would be too long.
const nameHashLength = 10

// AddressName derives the deterministic name of the Address generated for one
// Service: "<policy>-<service>". When that would exceed the Kubernetes 253
// character name limit, the service part is replaced with the first 10 hex
// characters of sha256(service) so the name stays short and deterministic.
func AddressName(policy, service string) string {
	name := policy + "-" + service
	if len(name) <= maxNameLength {
		return name
	}
	sum := sha256.Sum256([]byte(service))
	return policy + "-" + hex.EncodeToString(sum[:])[:nameHashLength]
}

// DefaultHostname is the hostname registered on an Address when a Service does not
// carry the hostname annotation: "<name>.<namespace>".
func DefaultHostname(ref ServiceRef) string {
	return ref.Name + "." + ref.Namespace
}

// PolicyPool derives the policy's own pool reference from spec.allocateFrom. It
// returns the zero PoolRef if none of subnetRef, rangeRef or addressBlockRef is set,
// which the CRD's CEL validation does not allow in practice.
func PolicyPool(p *uddiv1alpha1.IPAllocationPolicy) PoolRef {
	af := p.Spec.AllocateFrom
	switch {
	case af.SubnetRef != nil:
		return PoolRef{Kind: PoolSubnet, Name: af.SubnetRef.Name}
	case af.RangeRef != nil:
		return PoolRef{Kind: PoolRange, Name: af.RangeRef.Name}
	case af.AddressBlockRef != nil:
		return PoolRef{Kind: PoolAddressBlock, Name: af.AddressBlockRef.Name}
	default:
		return PoolRef{}
	}
}

// WantsAddress reports whether v should have an address allocated, ignoring
// selector matching and Service type: those are the controller's and Compute's job
// respectively, since Services handed to the planner are already selected. An
// explicit opt-out always wins. Otherwise, with requireAnnotation (the spec default)
// a Service must explicitly opt in; without it every Service wants an address
// unless it opted out.
func WantsAddress(p *uddiv1alpha1.IPAllocationPolicy, v ServiceView) bool {
	if v.OptIn != nil && !*v.OptIn {
		return false
	}
	requireAnnotation := p.Spec.RequireAnnotation == nil || *p.Spec.RequireAnnotation
	if requireAnnotation {
		return v.OptIn != nil && *v.OptIn
	}
	return true
}

// PlanInput is everything Compute needs: the policy (for its metadata, connectionRef,
// allocateFrom, assignVia, tags and addressLifecycle), the Service views already
// selected and annotation-parsed, the pool CRs those views (or the policy) refer to,
// and the Address CRs this policy currently owns.
type PlanInput struct {
	Policy   *uddiv1alpha1.IPAllocationPolicy
	Services []ServiceView
	Pools    map[PoolRef]PoolState
	Existing []AddressState
}

// Compute turns Service views, pool state and existing Addresses into the Addresses
// an IPAllocationPolicy should own, the Services that need their assignment field
// written, the allocations to release, and the Services skipped along with why. See
// DESIGN.md section 4.7, pipeline steps 3 to 7.
//
// InvalidAnnotation is treated as a transient, retryable condition: a Service whose
// annotations could not be parsed keeps whatever Address it already has rather than
// having it released out from under it. The three conditions that do release an
// existing allocation are exactly the ones DESIGN.md names: the Service disappeared
// from the input, it opted out (explicitly, or implicitly under requireAnnotation),
// or it is no longer a LoadBalancer.
//
//nolint:gocyclo // the loop is a single linear pipeline; splitting it hides the order.
func Compute(in PlanInput) Plan {
	var plan Plan

	serviceRefs := make(map[string]ServiceRef, len(in.Services))
	for _, v := range in.Services {
		serviceRefs[v.Ref.Name] = v.Ref
	}

	existingByService := make(map[string]AddressState, len(in.Existing))
	for _, a := range in.Existing {
		existingByService[a.Service] = a
	}

	policyPool := PolicyPool(in.Policy)
	assignField := assignViaFieldName(in.Policy)

	// wanted tracks every Service name that must keep whatever Address it has: it
	// either currently wants one, or its annotations could not be parsed this round.
	wanted := make(map[string]bool, len(in.Services))

	var assigned int32

	for _, v := range in.Services {
		if v.Invalid != "" {
			plan.Skips = append(plan.Skips, Skip{Service: v.Ref, Reason: SkipInvalidAnnotation, Message: v.Invalid})
			wanted[v.Ref.Name] = true
			continue
		}

		if !WantsAddress(in.Policy, v) {
			continue
		}

		if !v.IsLoadBalancer {
			plan.Skips = append(plan.Skips, Skip{
				Service: v.Ref,
				Reason:  SkipNotLoadBalancer,
				Message: "Service is not of type LoadBalancer",
			})
			continue
		}

		wanted[v.Ref.Name] = true

		pool := policyPool
		if v.Pool != nil {
			pool = *v.Pool
		}
		poolState, ok := in.Pools[pool]
		if !ok || !poolState.Ready {
			plan.Skips = append(plan.Skips, Skip{
				Service: v.Ref,
				Reason:  SkipPoolNotReady,
				Message: fmt.Sprintf("pool %s has no status.id yet", pool.String()),
			})
			continue
		}

		existing, hasExisting := existingByService[v.Ref.Name]

		if v.CurrentAssignment != "" && (!hasExisting || existing.Address != v.CurrentAssignment) {
			plan.Skips = append(plan.Skips, Skip{
				Service: v.Ref,
				Reason:  SkipManuallyAssigned,
				Message: fmt.Sprintf("%s is already %s", assignField, v.CurrentAssignment),
			})
			continue
		}

		plan.Ensure = append(plan.Ensure, buildDesiredAddress(in.Policy, v, pool, poolState))

		if hasExisting {
			switch {
			case existing.Ready && existing.Address != "":
				if v.CurrentAssignment == existing.Address {
					assigned++
				} else {
					plan.Assign = append(plan.Assign, Assignment{Service: v.Ref, Value: existing.Address})
				}
			default:
				plan.Skips = append(plan.Skips, Skip{
					Service: v.Ref,
					Reason:  SkipAddressPending,
					Message: fmt.Sprintf("%s: %s", existing.Reason, existing.Message),
				})
			}
		}
	}

	for _, a := range in.Existing {
		if wanted[a.Service] {
			continue
		}
		ref, ok := serviceRefs[a.Service]
		if !ok {
			ref = ServiceRef{Namespace: in.Policy.Namespace, Name: a.Service}
		}
		plan.Release = append(plan.Release, Release{Service: ref, AddressName: a.Name, ClearValue: a.Address})
	}

	plan.Allocated = int32(len(plan.Ensure))
	plan.Assigned = assigned

	sort.Slice(plan.Ensure, func(i, j int) bool { return plan.Ensure[i].Service.Name < plan.Ensure[j].Service.Name })
	sort.Slice(plan.Assign, func(i, j int) bool { return plan.Assign[i].Service.Name < plan.Assign[j].Service.Name })
	sort.Slice(plan.Release, func(i, j int) bool { return plan.Release[i].Service.Name < plan.Release[j].Service.Name })
	sort.Slice(plan.Skips, func(i, j int) bool {
		if plan.Skips[i].Service.Name != plan.Skips[j].Service.Name {
			return plan.Skips[i].Service.Name < plan.Skips[j].Service.Name
		}
		return plan.Skips[i].Reason < plan.Skips[j].Reason
	})

	return plan
}

// assignViaFieldName names the Service field an allocated address is written into,
// for ManuallyAssigned skip messages: the annotation key in Annotation mode
// (applying the default), or "spec.loadBalancerIP" in LoadBalancerIP mode.
func assignViaFieldName(p *uddiv1alpha1.IPAllocationPolicy) string {
	if p.Spec.AssignVia.Mode == uddiv1alpha1.AssignModeLoadBalancerIP {
		return "spec.loadBalancerIP"
	}
	if p.Spec.AssignVia.Annotation != "" {
		return p.Spec.AssignVia.Annotation
	}
	return DefaultAssignAnnotation
}

// buildDesiredAddress assembles the DesiredAddress for one Service already known to
// want an address from pool/poolState.
func buildDesiredAddress(p *uddiv1alpha1.IPAllocationPolicy, v ServiceView, pool PoolRef, poolState PoolState) DesiredAddress {
	tags := make(map[string]string, len(p.Spec.Tags)+1)
	for k, val := range p.Spec.Tags {
		tags[k] = val
	}
	tags[TagSource] = fmt.Sprintf("Service/%s/%s", v.Ref.Namespace, v.Ref.Name)

	spec := uddiv1alpha1.AddressSpec{
		CommonSpec: uddiv1alpha1.CommonSpec{
			ConnectionRef: p.Spec.ConnectionRef,
			Tags:          tags,
			Lifecycle:     p.Spec.AddressLifecycle,
		},
		SpaceRef: poolState.SpaceRef,
		Names:    []uddiv1alpha1.AddressName{{Name: v.Hostname, Type: "user"}},
		Comment: fmt.Sprintf("Allocated by IPAllocationPolicy %s/%s for Service %s",
			p.Namespace, p.Name, v.Ref.Name),
	}
	if v.ExplicitAddress != "" {
		spec.Address = v.ExplicitAddress
	} else {
		spec.NextAvailableFrom = nextAvailableFromFor(pool)
	}

	return DesiredAddress{
		Name:    AddressName(p.Name, v.Ref.Name),
		Service: v.Ref,
		Labels: map[string]string{
			LabelPolicy:  p.Name,
			LabelService: v.Ref.Name,
		},
		Spec: spec,
	}
}

// nextAvailableFromFor builds the NextAvailableFrom block naming pool.
func nextAvailableFromFor(pool PoolRef) *uddiv1alpha1.NextAvailableFrom {
	ref := &uddiv1alpha1.ObjectRef{Name: pool.Name}
	switch pool.Kind {
	case PoolSubnet:
		return &uddiv1alpha1.NextAvailableFrom{SubnetRef: ref}
	case PoolRange:
		return &uddiv1alpha1.NextAvailableFrom{RangeRef: ref}
	case PoolAddressBlock:
		return &uddiv1alpha1.NextAvailableFrom{AddressBlockRef: ref}
	default:
		return nil
	}
}

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

package v1alpha1

// The accessors below let the generic reconcile loop in internal/sync work over every
// object-layer kind without reflection. Each kind's spec inlines CommonSpec and each
// kind's status inlines ObjectStatus, so every method is a one-liner.

// ConnectionName returns the Connection name to use, defaulting to "default".
func (s *CommonSpec) ConnectionName() string {
	if s.ConnectionRef.Name == "" {
		return DefaultConnectionName
	}
	return s.ConnectionRef.Name
}

// View

// SpecOf returns the kind's spec.
func (o *View) SpecOf() *ViewSpec { return &o.Spec }

// StatusOf returns the shared object status.
func (o *View) StatusOf() *ObjectStatus { return &o.Status.ObjectStatus }

// ConnectionName returns the Connection name to use.
func (o *View) ConnectionName() string { return o.Spec.ConnectionName() }

// LifecycleOf returns the adoption and deletion policy.
func (o *View) LifecycleOf() Lifecycle { return o.Spec.Lifecycle }

// SpecTags returns the user-supplied tags.
func (o *View) SpecTags() map[string]string { return o.Spec.Tags }

// AuthZone

// SpecOf returns the kind's spec.
func (o *AuthZone) SpecOf() *AuthZoneSpec { return &o.Spec }

// StatusOf returns the shared object status.
func (o *AuthZone) StatusOf() *ObjectStatus { return &o.Status.ObjectStatus }

// ConnectionName returns the Connection name to use.
func (o *AuthZone) ConnectionName() string { return o.Spec.ConnectionName() }

// LifecycleOf returns the adoption and deletion policy.
func (o *AuthZone) LifecycleOf() Lifecycle { return o.Spec.Lifecycle }

// SpecTags returns the user-supplied tags.
func (o *AuthZone) SpecTags() map[string]string { return o.Spec.Tags }

// Record

// SpecOf returns the kind's spec.
func (o *Record) SpecOf() *RecordSpec { return &o.Spec }

// StatusOf returns the shared object status.
func (o *Record) StatusOf() *ObjectStatus { return &o.Status.ObjectStatus }

// ConnectionName returns the Connection name to use.
func (o *Record) ConnectionName() string { return o.Spec.ConnectionName() }

// LifecycleOf returns the adoption and deletion policy.
func (o *Record) LifecycleOf() Lifecycle { return o.Spec.Lifecycle }

// SpecTags returns the user-supplied tags.
func (o *Record) SpecTags() map[string]string { return o.Spec.Tags }

// IPSpace

// SpecOf returns the kind's spec.
func (o *IPSpace) SpecOf() *IPSpaceSpec { return &o.Spec }

// StatusOf returns the shared object status.
func (o *IPSpace) StatusOf() *ObjectStatus { return &o.Status.ObjectStatus }

// ConnectionName returns the Connection name to use.
func (o *IPSpace) ConnectionName() string { return o.Spec.ConnectionName() }

// LifecycleOf returns the adoption and deletion policy.
func (o *IPSpace) LifecycleOf() Lifecycle { return o.Spec.Lifecycle }

// SpecTags returns the user-supplied tags.
func (o *IPSpace) SpecTags() map[string]string { return o.Spec.Tags }

// AddressBlock

// SpecOf returns the kind's spec.
func (o *AddressBlock) SpecOf() *AddressBlockSpec { return &o.Spec }

// StatusOf returns the shared object status.
func (o *AddressBlock) StatusOf() *ObjectStatus { return &o.Status.ObjectStatus }

// ConnectionName returns the Connection name to use.
func (o *AddressBlock) ConnectionName() string { return o.Spec.ConnectionName() }

// LifecycleOf returns the adoption and deletion policy.
func (o *AddressBlock) LifecycleOf() Lifecycle { return o.Spec.Lifecycle }

// SpecTags returns the user-supplied tags.
func (o *AddressBlock) SpecTags() map[string]string { return o.Spec.Tags }

// Subnet

// SpecOf returns the kind's spec.
func (o *Subnet) SpecOf() *SubnetSpec { return &o.Spec }

// StatusOf returns the shared object status.
func (o *Subnet) StatusOf() *ObjectStatus { return &o.Status.ObjectStatus }

// ConnectionName returns the Connection name to use.
func (o *Subnet) ConnectionName() string { return o.Spec.ConnectionName() }

// LifecycleOf returns the adoption and deletion policy.
func (o *Subnet) LifecycleOf() Lifecycle { return o.Spec.Lifecycle }

// SpecTags returns the user-supplied tags.
func (o *Subnet) SpecTags() map[string]string { return o.Spec.Tags }

// Range

// SpecOf returns the kind's spec.
func (o *Range) SpecOf() *RangeSpec { return &o.Spec }

// StatusOf returns the shared object status.
func (o *Range) StatusOf() *ObjectStatus { return &o.Status.ObjectStatus }

// ConnectionName returns the Connection name to use.
func (o *Range) ConnectionName() string { return o.Spec.ConnectionName() }

// LifecycleOf returns the adoption and deletion policy.
func (o *Range) LifecycleOf() Lifecycle { return o.Spec.Lifecycle }

// SpecTags returns the user-supplied tags.
func (o *Range) SpecTags() map[string]string { return o.Spec.Tags }

// Address

// SpecOf returns the kind's spec.
func (o *Address) SpecOf() *AddressSpec { return &o.Spec }

// StatusOf returns the shared object status.
func (o *Address) StatusOf() *ObjectStatus { return &o.Status.ObjectStatus }

// ConnectionName returns the Connection name to use.
func (o *Address) ConnectionName() string { return o.Spec.ConnectionName() }

// LifecycleOf returns the adoption and deletion policy.
func (o *Address) LifecycleOf() Lifecycle { return o.Spec.Lifecycle }

// SpecTags returns the user-supplied tags.
func (o *Address) SpecTags() map[string]string { return o.Spec.Tags }

// FixedAddress

// SpecOf returns the kind's spec.
func (o *FixedAddress) SpecOf() *FixedAddressSpec { return &o.Spec }

// StatusOf returns the shared object status.
func (o *FixedAddress) StatusOf() *ObjectStatus { return &o.Status.ObjectStatus }

// ConnectionName returns the Connection name to use.
func (o *FixedAddress) ConnectionName() string { return o.Spec.ConnectionName() }

// LifecycleOf returns the adoption and deletion policy.
func (o *FixedAddress) LifecycleOf() Lifecycle { return o.Spec.Lifecycle }

// SpecTags returns the user-supplied tags.
func (o *FixedAddress) SpecTags() map[string]string { return o.Spec.Tags }

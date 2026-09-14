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

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Finalizer is set on every object-layer CR so the operator can clean the
// matching Universal DDI object up before the CR disappears.
const Finalizer = "uddi.infoblox.com/finalizer"

// DefaultConnectionName is the Connection used by object-layer CRs that leave
// spec.connectionRef empty.
const DefaultConnectionName = "default"

// Condition types carried by every object-layer CR and by Connection.
const (
	// ConditionReady is the summary condition.
	ConditionReady = "Ready"
	// ConditionSynced is True when the Universal DDI object matched spec at
	// status.observedGeneration.
	ConditionSynced = "Synced"
)

// Condition reasons. See DESIGN.md section 4.1.
const (
	// ReasonConnectionNotReady means the referenced Connection is missing or not verified.
	ReasonConnectionNotReady = "ConnectionNotReady"
	// ReasonDependencyNotReady means a referenced object-layer CR has no status.id yet.
	ReasonDependencyNotReady = "DependencyNotReady"
	// ReasonAlreadyExists means an untagged object exists under the natural key and
	// lifecycle.adoptExisting is false.
	ReasonAlreadyExists = "AlreadyExists"
	// ReasonCreating means the Universal DDI object is being created.
	ReasonCreating = "Creating"
	// ReasonSynced means the Universal DDI object matches spec.
	ReasonSynced = "Synced"
	// ReasonAPIError means a Universal DDI API call failed.
	ReasonAPIError = "APIError"
	// ReasonDeleting means deletion is in progress or blocked.
	ReasonDeleting = "Deleting"
)

// Additional condition reasons used by the Connection controller.
const (
	// ReasonVerified means the API key passed the verification call.
	ReasonVerified = "Verified"
	// ReasonSecretNotFound means the referenced Secret does not exist.
	ReasonSecretNotFound = "SecretNotFound"
	// ReasonSecretKeyMissing means the Secret has no usable data under the configured key.
	ReasonSecretKeyMissing = "SecretKeyMissing"
)

// ConnectionRef names a Connection in the same namespace as the referring CR.
// An empty ref (or an empty name) means the Connection called "default".
type ConnectionRef struct {
	// Name of the Connection in this namespace. Defaults to "default".
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Name string `json:"name,omitempty"`
}

// ObjectRef points at another object-layer CR in the same namespace, or directly
// at a Universal DDI object by its API id (for example "dns/view/2c1a...").
// Exactly one of name and id is set.
// +kubebuilder:validation:XValidation:rule="has(self.name) != has(self.id)",message="exactly one of name or id must be set"
type ObjectRef struct {
	// Name of an object-layer CR in this namespace.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Name string `json:"name,omitempty"`

	// ID is a Universal DDI resource id, for example "dns/view/2c1a...".
	// +kubebuilder:validation:MaxLength=256
	// +optional
	ID string `json:"id,omitempty"`
}

// IsZero reports whether the reference selects nothing.
func (r *ObjectRef) IsZero() bool {
	return r == nil || (r.Name == "" && r.ID == "")
}

// DeletionPolicy decides what happens to the Universal DDI object when the CR is deleted.
// +kubebuilder:validation:Enum=Delete;Orphan
type DeletionPolicy string

const (
	// DeletionPolicyDelete deletes the Universal DDI object, guarded by the ownership uid tag.
	DeletionPolicyDelete DeletionPolicy = "Delete"
	// DeletionPolicyOrphan leaves the Universal DDI object in place.
	DeletionPolicyOrphan DeletionPolicy = "Orphan"
)

// Lifecycle controls adoption and deletion behaviour.
type Lifecycle struct {
	// AdoptExisting allows taking over an existing Universal DDI object that matches the
	// natural key but does not carry this operator's ownership tags. When false such an
	// object is reported as a conflict (Ready=False/AlreadyExists).
	// +kubebuilder:default=false
	// +optional
	AdoptExisting bool `json:"adoptExisting,omitempty"`

	// DeletionPolicy decides whether deleting the CR also deletes the Universal DDI object.
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy DeletionPolicy `json:"deletionPolicy,omitempty"`
}

// Policy returns the effective deletion policy, defaulting to Delete.
func (l Lifecycle) Policy() DeletionPolicy {
	if l.DeletionPolicy == "" {
		return DeletionPolicyDelete
	}
	return l.DeletionPolicy
}

// CommonSpec is inlined into every object-layer kind's spec.
type CommonSpec struct {
	// ConnectionRef names the Connection providing credentials. Empty means the
	// Connection called "default" in this namespace.
	// +optional
	ConnectionRef ConnectionRef `json:"connectionRef,omitempty"`

	// Tags merged onto the Universal DDI object. Ownership tags always win, then the
	// Connection's defaultTags, then these.
	// +optional
	Tags map[string]string `json:"tags,omitempty"`

	// Lifecycle controls adoption and deletion.
	// +optional
	Lifecycle Lifecycle `json:"lifecycle,omitempty"`
}

// ObjectStatus is the status shared by every object-layer kind.
type ObjectStatus struct {
	// ID is the Universal DDI resource id, for example "ipam/subnet/<uuid>".
	// +optional
	ID string `json:"id,omitempty"`

	// ObservedGeneration is the metadata.generation the status was last computed from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastSyncTime is when the Universal DDI object was last read or written.
	// +optional
	LastSyncTime *metav1.Time `json:"lastSyncTime,omitempty"`

	// Conditions hold Ready and Synced.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// DHCPOptionType selects between a single option and an option group.
// +kubebuilder:validation:Enum=option;group
type DHCPOptionType string

const (
	// DHCPOptionTypeOption is a single DHCP option code plus value.
	DHCPOptionTypeOption DHCPOptionType = "option"
	// DHCPOptionTypeGroup references a DHCP option group.
	DHCPOptionTypeGroup DHCPOptionType = "group"
)

// DHCPOption maps to the Universal DDI ipam.OptionItem. Option codes and groups are
// opaque Universal DDI resource ids in v1alpha1; lookup by name is a later convenience.
// +kubebuilder:validation:XValidation:rule="self.type == 'option' ? has(self.optionCode) : has(self.group)",message="type option requires optionCode, type group requires group"
type DHCPOption struct {
	// Type selects a single option or an option group.
	// +kubebuilder:default=option
	Type DHCPOptionType `json:"type"`

	// OptionCode is a Universal DDI option code id, for example "dhcp/option_code/<uuid>".
	// Required when type is "option".
	// +kubebuilder:validation:MaxLength=256
	// +optional
	OptionCode string `json:"optionCode,omitempty"`

	// OptionValue is the value for the option code.
	// +kubebuilder:validation:MaxLength=4096
	// +optional
	OptionValue string `json:"optionValue,omitempty"`

	// Group is a Universal DDI option group id, for example "dhcp/option_group/<uuid>".
	// Required when type is "group".
	// +kubebuilder:validation:MaxLength=256
	// +optional
	Group string `json:"group,omitempty"`
}

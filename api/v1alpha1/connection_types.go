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

// DefaultCSPURL is the Universal DDI Cloud Services Portal endpoint.
const DefaultCSPURL = "https://csp.infoblox.com"

// DefaultClientName is sent to the API as the client identifier.
const DefaultClientName = "uddi-operator"

// DefaultSecretKey is the Secret data key holding the API key.
const DefaultSecretKey = "apiKey"

// SecretKeyRef points at a key inside a Secret in the Connection's namespace.
type SecretKeyRef struct {
	// Name of the Secret.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// Key inside the Secret's data holding the Universal DDI API key.
	// +kubebuilder:default=apiKey
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Key string `json:"key,omitempty"`
}

// RateLimit configures the client-side token bucket applied to Universal DDI calls.
type RateLimit struct {
	// RequestsPerSecond is the sustained rate.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1000
	// +kubebuilder:default=10
	// +optional
	RequestsPerSecond int32 `json:"requestsPerSecond,omitempty"`

	// Burst is the bucket size.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=1000
	// +kubebuilder:default=20
	// +optional
	Burst int32 `json:"burst,omitempty"`
}

// ConnectionSpec defines credentials and client settings for one Universal DDI tenant.
type ConnectionSpec struct {
	// CSPURL is the Cloud Services Portal endpoint: https://, a host name, an optional
	// port and at most a trailing slash. Userinfo, paths, queries and fragments are
	// rejected, and the host must also be on the operator's --allowed-csp-hosts list.
	// +kubebuilder:default="https://csp.infoblox.com"
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:Pattern=`^https://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?/?$`
	// +optional
	CSPURL string `json:"cspURL,omitempty"`

	// SecretRef points at the Secret holding the API key.
	SecretRef SecretKeyRef `json:"secretRef"`

	// ClientName is reported to the API and shows up in the Universal DDI audit log.
	// +kubebuilder:default=uddi-operator
	// +kubebuilder:validation:MaxLength=128
	// +optional
	ClientName string `json:"clientName,omitempty"`

	// DefaultTags are merged into every object created through this Connection,
	// below the operator's ownership tags and above nothing else.
	// +optional
	DefaultTags map[string]string `json:"defaultTags,omitempty"`

	// ResyncInterval is the drift-detection period for objects using this Connection.
	// The minimum is 30s.
	// +kubebuilder:default="10m"
	// +kubebuilder:validation:XValidation:rule="duration(self) >= duration('30s')",message="resyncInterval must be at least 30s"
	// +optional
	ResyncInterval *metav1.Duration `json:"resyncInterval,omitempty"`

	// RateLimit configures the client-side limiter. When unset, the field defaults apply
	// (10 requests per second, burst 20).
	// +optional
	RateLimit *RateLimit `json:"rateLimit,omitempty"`
}

// ConnectionStatus reports whether the credentials work.
type ConnectionStatus struct {
	// ObservedGeneration is the metadata.generation the status was last computed from.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// LastVerifiedTime is when the API key last passed the verification call.
	// +optional
	LastVerifiedTime *metav1.Time `json:"lastVerifiedTime,omitempty"`

	// Conditions hold Ready.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=uddiconn
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Endpoint",type=string,JSONPath=`.spec.cspURL`
// +kubebuilder:printcolumn:name="Verified",type=date,JSONPath=`.status.lastVerifiedTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Connection holds the credentials and client settings for one Universal DDI tenant.
type Connection struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ConnectionSpec   `json:"spec,omitempty"`
	Status ConnectionStatus `json:"status,omitempty"`
}

// SecretKey returns the effective Secret data key.
func (c *Connection) SecretKey() string {
	if c.Spec.SecretRef.Key == "" {
		return DefaultSecretKey
	}
	return c.Spec.SecretRef.Key
}

// CSPURL returns the effective Cloud Services Portal endpoint.
func (c *Connection) CSPURL() string {
	if c.Spec.CSPURL == "" {
		return DefaultCSPURL
	}
	return c.Spec.CSPURL
}

// ClientName returns the effective client name.
func (c *Connection) ClientName() string {
	if c.Spec.ClientName == "" {
		return DefaultClientName
	}
	return c.Spec.ClientName
}

// +kubebuilder:object:root=true

// ConnectionList contains a list of Connection.
type ConnectionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Connection `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Connection{}, &ConnectionList{})
}

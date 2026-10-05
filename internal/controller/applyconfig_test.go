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

package controller

import (
	"encoding/json"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	uddiapply "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1/applyconfiguration/api/v1alpha1"
	"github.com/IngmarVG-IB/openshiftoperator/internal/hostreg"
	"github.com/IngmarVG-IB/openshiftoperator/internal/ipalloc"
	"github.com/IngmarVG-IB/openshiftoperator/internal/publish"
)

// appliedBy reports whether obj carries a managedFields entry written by a server-side
// apply from the given field manager, which is what proves the manager owns its fields.
func appliedBy(obj metav1.Object, manager string) bool {
	for _, mf := range obj.GetManagedFields() {
		if mf.Manager == manager && mf.Operation == metav1.ManagedFieldsOperationApply {
			return true
		}
	}
	return false
}

// unitScheme is a scheme with the core types and the uddi API, for the specs in this
// file that run without the envtest suite.
func unitScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := uddiv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// roundTrip encodes an apply configuration the way the client sends it and decodes it
// into the typed object, so a test can compare it with what the planner asked for.
func roundTrip(t *testing.T, ac any, into any) {
	t.Helper()
	raw, err := json.Marshal(ac)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatal(err)
	}
}

func fullCommonSpec() uddiv1alpha1.CommonSpec {
	return uddiv1alpha1.CommonSpec{
		ConnectionRef: uddiv1alpha1.ConnectionRef{Name: "alt-connection"},
		Tags:          map[string]string{"team": "net", "env": "prod"},
		Lifecycle: uddiv1alpha1.Lifecycle{
			AdoptExisting:  true,
			DeletionPolicy: uddiv1alpha1.DeletionPolicyOrphan,
		},
	}
}

func fullAddressSpec() uddiv1alpha1.AddressSpec {
	return uddiv1alpha1.AddressSpec{
		CommonSpec: fullCommonSpec(),
		SpaceRef:   uddiv1alpha1.ObjectRef{Name: "space"},
		Address:    "10.0.0.5",
		NextAvailableFrom: &uddiv1alpha1.NextAvailableFrom{
			RangeRef: &uddiv1alpha1.ObjectRef{Name: "range"},
		},
		Names:   []uddiv1alpha1.AddressName{{Name: "web.ns", Type: "user"}},
		HWAddr:  "52:54:00:aa:bb:01",
		Comment: "allocated by a test",
	}
}

func TestDesiredRecordApplyConfigurationCarriesTheDesiredObject(t *testing.T) {
	ttl := int32(120)
	yes := true
	policy := &uddiv1alpha1.DNSPublishPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "ns", UID: types.UID("policy-uid")},
	}
	dr := publish.DesiredRecord{
		Name:        "policy-web",
		Labels:      map[string]string{publish.LabelPolicy: "policy"},
		Annotations: map[string]string{publish.AnnotationRecordHost: "web.apps.example.com"},
		Spec: uddiv1alpha1.RecordSpec{
			CommonSpec: fullCommonSpec(),
			ZoneRef:    uddiv1alpha1.ObjectRef{Name: "zone"},
			Name:       "web",
			Type:       "A",
			RData:      uddiv1alpha1.RecordRData{A: &uddiv1alpha1.ARData{Address: "192.0.2.1"}},
			TTL:        &ttl,
			CreatePtr:  &yes,
			Comment:    "published by a test",
			Disabled:   true,
		},
	}

	ac, err := desiredRecordApplyConfiguration(policy, dr, unitScheme(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := *ac.APIVersion; got != uddiv1alpha1.GroupVersion.String() {
		t.Errorf("apiVersion = %q", got)
	}
	if got := *ac.Kind; got != "Record" {
		t.Errorf("kind = %q", got)
	}

	var rec uddiv1alpha1.Record
	roundTrip(t, ac, &rec)
	if rec.Name != dr.Name || rec.Namespace != "ns" {
		t.Errorf("name/namespace = %s/%s", rec.Namespace, rec.Name)
	}
	if !equality.Semantic.DeepEqual(rec.Labels, dr.Labels) || !equality.Semantic.DeepEqual(rec.Annotations, dr.Annotations) {
		t.Errorf("labels/annotations = %v / %v", rec.Labels, rec.Annotations)
	}
	owner := metav1.GetControllerOf(&rec)
	if owner == nil || owner.APIVersion != uddiv1alpha1.GroupVersion.String() || owner.Kind != "DNSPublishPolicy" ||
		owner.Name != "policy" || owner.UID != "policy-uid" || owner.BlockOwnerDeletion == nil || !*owner.BlockOwnerDeletion {
		t.Errorf("owner = %+v", owner)
	}
	if len(rec.OwnerReferences) != 1 {
		t.Errorf("ownerReferences = %+v", rec.OwnerReferences)
	}
	if !equality.Semantic.DeepEqual(rec.Spec, dr.Spec) {
		t.Errorf("spec = %+v, want %+v", rec.Spec, dr.Spec)
	}
}

func TestDesiredAddressApplyConfigurationCarriesTheDesiredObject(t *testing.T) {
	policy := &uddiv1alpha1.IPAllocationPolicy{ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "ns"}}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "ns", UID: types.UID("svc-uid")}}
	svcOwner, err := controllerOwnerReference(svc, unitScheme(t))
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		owner *metav1ac.OwnerReferenceApplyConfiguration
		want  metav1.OwnerReference
	}{
		{
			name:  "Service slot",
			owner: svcOwner,
			want:  metav1.OwnerReference{APIVersion: "v1", Kind: "Service", Name: "web", UID: "svc-uid"},
		},
		{
			name:  "EgressIP slot",
			owner: egressIPOwnerReference("egress-a", types.UID("eip-uid")),
			want:  metav1.OwnerReference{APIVersion: "k8s.ovn.org/v1", Kind: "EgressIP", Name: "egress-a", UID: "eip-uid"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			da := ipalloc.DesiredAddress{
				Name:        "policy-web",
				Labels:      map[string]string{ipalloc.LabelPolicy: "policy", ipalloc.LabelService: "web"},
				Annotations: map[string]string{"example.com/note": "x"},
				Spec:        fullAddressSpec(),
			}

			ac, err := desiredAddressApplyConfiguration(policy, da, tc.owner)
			if err != nil {
				t.Fatal(err)
			}

			var addr uddiv1alpha1.Address
			roundTrip(t, ac, &addr)
			if *ac.Kind != "Address" || addr.Name != da.Name || addr.Namespace != "ns" {
				t.Errorf("kind/name/namespace = %s %s/%s", *ac.Kind, addr.Namespace, addr.Name)
			}
			if !equality.Semantic.DeepEqual(addr.Labels, da.Labels) || !equality.Semantic.DeepEqual(addr.Annotations, da.Annotations) {
				t.Errorf("labels/annotations = %v / %v", addr.Labels, addr.Annotations)
			}
			owner := metav1.GetControllerOf(&addr)
			if owner == nil || owner.APIVersion != tc.want.APIVersion || owner.Kind != tc.want.Kind || owner.Name != tc.want.Name ||
				owner.UID != tc.want.UID || owner.BlockOwnerDeletion == nil || !*owner.BlockOwnerDeletion {
				t.Errorf("owner = %+v", owner)
			}
			if len(addr.OwnerReferences) != 1 {
				t.Errorf("ownerReferences = %+v", addr.OwnerReferences)
			}
			if !equality.Semantic.DeepEqual(addr.Spec, da.Spec) {
				t.Errorf("spec = %+v, want %+v", addr.Spec, da.Spec)
			}
		})
	}
}

func TestDesiredRegistrationApplyConfigurationCarriesTheDesiredObject(t *testing.T) {
	policy := &uddiv1alpha1.HostRegistrationPolicy{ObjectMeta: metav1.ObjectMeta{Name: "policy", Namespace: "ns"}}
	node := hostreg.NodeRef{Name: "node-1", UID: types.UID("node-uid")}
	labels := map[string]string{hostreg.LabelPolicy: "policy", hostreg.LabelNode: "node-1"}
	annotations := map[string]string{"example.com/note": "x"}

	checkOwner := func(t *testing.T, o metav1.Object) {
		t.Helper()
		owner := metav1.GetControllerOf(o)
		if owner == nil || owner.APIVersion != "v1" || owner.Kind != "Node" || owner.Name != "node-1" ||
			owner.UID != "node-uid" || owner.BlockOwnerDeletion == nil || !*owner.BlockOwnerDeletion {
			t.Errorf("owner = %+v", owner)
		}
	}

	t.Run("Address", func(t *testing.T) {
		spec := fullAddressSpec()
		ac, err := desiredRegistrationApplyConfiguration(policy, hostreg.DesiredRegistration{
			Name: "policy-node-1", Kind: hostreg.KindAddress, Node: node,
			Labels: labels, Annotations: annotations, AddressSpec: &spec,
		})
		if err != nil {
			t.Fatal(err)
		}
		var got uddiv1alpha1.Address
		roundTrip(t, ac, &got)
		if got.Name != "policy-node-1" || got.Namespace != "ns" ||
			!equality.Semantic.DeepEqual(got.Labels, labels) || !equality.Semantic.DeepEqual(got.Annotations, annotations) {
			t.Errorf("metadata = %+v", got.ObjectMeta)
		}
		checkOwner(t, &got)
		if !equality.Semantic.DeepEqual(got.Spec, spec) {
			t.Errorf("spec = %+v, want %+v", got.Spec, spec)
		}
	})

	t.Run("FixedAddress", func(t *testing.T) {
		spec := uddiv1alpha1.FixedAddressSpec{
			CommonSpec:  fullCommonSpec(),
			SpaceRef:    uddiv1alpha1.ObjectRef{Name: "space"},
			Address:     "10.0.0.6",
			MatchType:   "mac",
			MatchValue:  "52:54:00:aa:bb:02",
			Name:        "node-1",
			Hostname:    "node-1",
			Comment:     "registered by a test",
			DisableDHCP: true,
			DHCPOptions: []uddiv1alpha1.DHCPOption{
				{Type: uddiv1alpha1.DHCPOptionTypeOption, OptionCode: "dhcp/option_code/aaa", OptionValue: "10.0.10.1"},
				{Type: uddiv1alpha1.DHCPOptionTypeGroup, Group: "dhcp/option_group/bbb"},
			},
		}
		ac, err := desiredRegistrationApplyConfiguration(policy, hostreg.DesiredRegistration{
			Name: "policy-node-1", Kind: hostreg.KindFixedAddress, Node: node,
			Labels: labels, Annotations: annotations, FixedAddressSpec: &spec,
		})
		if err != nil {
			t.Fatal(err)
		}
		var got uddiv1alpha1.FixedAddress
		roundTrip(t, ac, &got)
		if got.Name != "policy-node-1" || got.Namespace != "ns" ||
			!equality.Semantic.DeepEqual(got.Labels, labels) || !equality.Semantic.DeepEqual(got.Annotations, annotations) {
			t.Errorf("metadata = %+v", got.ObjectMeta)
		}
		checkOwner(t, &got)
		if !equality.Semantic.DeepEqual(got.Spec, spec) {
			t.Errorf("spec = %+v, want %+v", got.Spec, spec)
		}
	})

	t.Run("missing spec and unknown kind are errors", func(t *testing.T) {
		for _, d := range []hostreg.DesiredRegistration{
			{Name: "a", Kind: hostreg.KindAddress, Node: node},
			{Name: "f", Kind: hostreg.KindFixedAddress, Node: node},
			{Name: "x", Kind: hostreg.RegistrationKind("Other"), Node: node},
		} {
			if _, err := desiredRegistrationApplyConfiguration(policy, d); err == nil {
				t.Errorf("%s %s: want an error", d.Kind, d.Name)
			}
		}
	})
}

// staleSpecApplyConfiguration stands in for a generated apply configuration that predates
// a field of the API type: it knows name but not comment.
type staleSpecApplyConfiguration struct {
	Name *string `json:"name,omitempty"`
}

func TestSpecApplyConfigurationFailsLoudlyWhenTheGeneratedCodeIsStale(t *testing.T) {
	spec := uddiv1alpha1.RecordSpec{Name: "web", Comment: "a field the stale type does not know"}
	_, err := specApplyConfiguration[staleSpecApplyConfiguration](spec)
	if err == nil || !strings.Contains(err.Error(), "make generate") {
		t.Fatalf("err = %v, want a pointer to make generate", err)
	}

	// The same spec converts cleanly into the real apply configuration.
	if _, err := specApplyConfiguration[uddiapply.RecordSpecApplyConfiguration](spec); err != nil {
		t.Fatal(err)
	}
}

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
	"context"
	"strings"

	"github.com/infobloxopen/universal-ddi-go-client/dnsconfig"
	"sigs.k8s.io/controller-runtime/pkg/client"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	uddisync "github.com/IngmarVG-IB/openshiftoperator/internal/sync"
	"github.com/IngmarVG-IB/openshiftoperator/internal/tags"
	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// depView is the Deps key holding the resolved Universal DDI view id.
const depView = "view"

// authzoneAdapter maps the AuthZone CR onto dnsconfig.AuthZone, following the shape of
// viewAdapter (view_adapter.go).
type authzoneAdapter struct{}

var _ uddisync.Adapter[uddiv1alpha1.AuthZoneSpec, dnsconfig.AuthZone] = authzoneAdapter{}

// Kind returns the CR kind name.
func (authzoneAdapter) Kind() string { return "AuthZone" }

// canonicalFQDN lowercases fqdn and ensures exactly one trailing dot, the canonical form
// Universal DDI converts a zone's fqdn to on create (docs/uddi-api-notes.md). Find and
// Create both use this so the natural-key lookup and the value actually sent agree.
func canonicalFQDN(fqdn string) string {
	f := strings.ToLower(strings.TrimSpace(fqdn))
	f = strings.TrimSuffix(f, ".")
	if f == "" {
		return ""
	}
	return f + "."
}

// Resolve turns spec.viewRef into a Universal DDI view id.
func (authzoneAdapter) Resolve(ctx context.Context, c client.Client, _ *uddi.Client, obj client.Object, spec *uddiv1alpha1.AuthZoneSpec) (uddisync.Deps, error) {
	viewID, err := uddisync.ResolveRef(ctx, c, obj.GetNamespace(), spec.ViewRef, &uddiv1alpha1.View{}, "View")
	if err != nil {
		return nil, err
	}
	return uddisync.Deps{depView: viewID}, nil
}

// Find looks a zone up by its natural key: fqdn plus view.
func (authzoneAdapter) Find(ctx context.Context, uc *uddi.Client, spec *uddiv1alpha1.AuthZoneSpec, deps uddisync.Deps) (*dnsconfig.AuthZone, error) {
	fqdn := canonicalFQDN(spec.FQDN)
	view := deps[depView]
	if fqdn == "" || view == "" {
		return nil, nil
	}
	res, err := uc.DNSConfig.ListAuthZones(ctx, uddi.ListOptions{
		Filter: "fqdn==" + quoteFilterValue(fqdn) + " and view==" + quoteFilterValue(view),
		Limit:  1,
	})
	if err != nil || len(res) == 0 {
		return nil, err
	}
	return &res[0], nil
}

// FindByUID looks a zone up by the ownership uid tag.
func (authzoneAdapter) FindByUID(ctx context.Context, uc *uddi.Client, uid string) (*dnsconfig.AuthZone, error) {
	if uid == "" {
		return nil, nil
	}
	res, err := uc.DNSConfig.ListAuthZones(ctx, uddi.ListOptions{
		TFilter: tags.UIDFilter(uid),
		Limit:   1,
	})
	if err != nil || len(res) == 0 {
		return nil, err
	}
	return &res[0], nil
}

// toSDKInternalSecondaries converts the CRD's internal secondaries into the SDK shape.
func toSDKInternalSecondaries(in []uddiv1alpha1.InternalSecondary) []dnsconfig.InternalSecondary {
	if len(in) == 0 {
		return nil
	}
	out := make([]dnsconfig.InternalSecondary, len(in))
	for i, s := range in {
		out[i] = dnsconfig.InternalSecondary{Host: s.Host}
	}
	return out
}

// toSDKExternalPrimaries converts the CRD's external primaries into the SDK shape.
func toSDKExternalPrimaries(in []uddiv1alpha1.ExternalPrimary) []dnsconfig.ExternalPrimary {
	if len(in) == 0 {
		return nil
	}
	out := make([]dnsconfig.ExternalPrimary, len(in))
	for i, p := range in {
		item := dnsconfig.ExternalPrimary{Type: string(p.Type)}
		if p.Address != "" {
			addr := p.Address
			item.Address = &addr
		}
		if p.FQDN != "" {
			f := p.FQDN
			item.Fqdn = &f
		}
		if p.NSG != "" {
			n := p.NSG
			item.Nsg = &n
		}
		if p.TSIGEnabled {
			tv := true
			item.TsigEnabled = &tv
		}
		out[i] = item
	}
	return out
}

// toSDKExternalSecondaries converts the CRD's external secondaries into the SDK shape.
func toSDKExternalSecondaries(in []uddiv1alpha1.ExternalSecondary) []dnsconfig.ExternalSecondary {
	if len(in) == 0 {
		return nil
	}
	out := make([]dnsconfig.ExternalSecondary, len(in))
	for i, s := range in {
		item := dnsconfig.ExternalSecondary{Address: s.Address, Fqdn: s.FQDN}
		if s.Stealth {
			st := true
			item.Stealth = &st
		}
		if s.TSIGEnabled {
			tv := true
			item.TsigEnabled = &tv
		}
		out[i] = item
	}
	return out
}

// sameInternalSecondaries compares by value, ignoring the SDK struct's AdditionalProperties
// bag so a round trip through the real API (which always sets it, even to an empty map)
// never looks like drift.
func sameInternalSecondaries(a, b []dnsconfig.InternalSecondary) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Host != b[i].Host {
			return false
		}
	}
	return true
}

func sameExternalPrimaries(a, b []dnsconfig.ExternalPrimary) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type ||
			strPtr(a[i].Address) != strPtr(b[i].Address) ||
			strPtr(a[i].Fqdn) != strPtr(b[i].Fqdn) ||
			strPtr(a[i].Nsg) != strPtr(b[i].Nsg) ||
			boolPtr(a[i].TsigEnabled) != boolPtr(b[i].TsigEnabled) {
			return false
		}
	}
	return true
}

func sameExternalSecondaries(a, b []dnsconfig.ExternalSecondary) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Address != b[i].Address ||
			a[i].Fqdn != b[i].Fqdn ||
			boolPtr(a[i].Stealth) != boolPtr(b[i].Stealth) ||
			boolPtr(a[i].TsigEnabled) != boolPtr(b[i].TsigEnabled) {
			return false
		}
	}
	return true
}

// Create makes the Universal DDI zone.
func (authzoneAdapter) Create(ctx context.Context, uc *uddi.Client, spec *uddiv1alpha1.AuthZoneSpec, deps uddisync.Deps, t map[string]string) (*dnsconfig.AuthZone, error) {
	fqdn := canonicalFQDN(spec.FQDN)
	view := deps[depView]
	primaryType := string(spec.PrimaryType)
	in := dnsconfig.AuthZone{
		Fqdn:        &fqdn,
		View:        &view,
		PrimaryType: &primaryType,
	}
	if spec.Comment != "" {
		c := spec.Comment
		in.Comment = &c
	}
	if spec.Disabled {
		d := true
		in.Disabled = &d
	}
	in.Nsgs = spec.NSGs
	in.InternalSecondaries = toSDKInternalSecondaries(spec.InternalSecondaries)
	in.ExternalPrimaries = toSDKExternalPrimaries(spec.ExternalPrimaries)
	in.ExternalSecondaries = toSDKExternalSecondaries(spec.ExternalSecondaries)
	in.Tags = uddi.ToAPITags(t)
	return uc.DNSConfig.CreateAuthZone(ctx, in)
}

// Get reads a zone by id.
func (authzoneAdapter) Get(ctx context.Context, uc *uddi.Client, id string) (*dnsconfig.AuthZone, error) {
	return uc.DNSConfig.GetAuthZone(ctx, id)
}

// Diff compares the mutable managed fields (nsgs, secondaries, primaries, comment,
// disabled, tags) and builds a partial patch. fqdn, primaryType and view are immutable
// (enforced by CEL on the CRD) and are never diffed.
func (authzoneAdapter) Diff(spec *uddiv1alpha1.AuthZoneSpec, deps uddisync.Deps, t tags.Desired, current *dnsconfig.AuthZone) (*dnsconfig.AuthZone, bool) {
	patch := &dnsconfig.AuthZone{}
	changed := false

	if strPtr(current.Comment) != spec.Comment {
		c := spec.Comment
		patch.Comment = &c
		changed = true
	}
	if boolPtr(current.Disabled) != spec.Disabled {
		d := spec.Disabled
		patch.Disabled = &d
		changed = true
	}
	if !sameStrings(current.Nsgs, spec.NSGs) {
		want := spec.NSGs
		if want == nil {
			// An explicit empty list clears the binding; nil would be omitted.
			want = []string{}
		}
		patch.Nsgs = want
		changed = true
	}
	if wantIS := toSDKInternalSecondaries(spec.InternalSecondaries); !sameInternalSecondaries(current.InternalSecondaries, wantIS) {
		patch.InternalSecondaries = wantIS
		if patch.InternalSecondaries == nil {
			patch.InternalSecondaries = []dnsconfig.InternalSecondary{}
		}
		changed = true
	}
	if wantEP := toSDKExternalPrimaries(spec.ExternalPrimaries); !sameExternalPrimaries(current.ExternalPrimaries, wantEP) {
		patch.ExternalPrimaries = wantEP
		if patch.ExternalPrimaries == nil {
			patch.ExternalPrimaries = []dnsconfig.ExternalPrimary{}
		}
		changed = true
	}
	if wantES := toSDKExternalSecondaries(spec.ExternalSecondaries); !sameExternalSecondaries(current.ExternalSecondaries, wantES) {
		patch.ExternalSecondaries = wantES
		if patch.ExternalSecondaries == nil {
			patch.ExternalSecondaries = []dnsconfig.ExternalSecondary{}
		}
		changed = true
	}
	if merged, tagsChanged := tags.ForPatchAPI(current.Tags, t); tagsChanged {
		patch.Tags = merged
		changed = true
	}
	return patch, changed
}

// Update applies a partial patch.
func (authzoneAdapter) Update(ctx context.Context, uc *uddi.Client, id string, patch *dnsconfig.AuthZone) (*dnsconfig.AuthZone, error) {
	return uc.DNSConfig.UpdateAuthZone(ctx, id, *patch)
}

// Delete removes the zone.
func (authzoneAdapter) Delete(ctx context.Context, uc *uddi.Client, id string) error {
	return uc.DNSConfig.DeleteAuthZone(ctx, id)
}

// warningsOf formats the SDK's read-only warnings as "<name>: <message>" lines.
func warningsOf(in []dnsconfig.Warning) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, w := range in {
		out = append(out, strPtr(w.Name)+": "+strPtr(w.Message))
	}
	return out
}

// Observe copies observed fields into the CR status.
func (authzoneAdapter) Observe(obj client.Object, current *dnsconfig.AuthZone) {
	z, ok := obj.(*uddiv1alpha1.AuthZone)
	if !ok || current == nil {
		return
	}
	z.Status.FQDN = strPtr(current.ProtocolFqdn)
	z.Status.View = strPtr(current.View)
	z.Status.Warnings = warningsOf(current.Warnings)
}

// IDOf returns the Universal DDI resource id.
func (authzoneAdapter) IDOf(current *dnsconfig.AuthZone) string { return strPtr(current.Id) }

// TagsOf returns the tags currently set on the zone.
func (authzoneAdapter) TagsOf(current *dnsconfig.AuthZone) map[string]string {
	return uddi.FromAPITags(current.Tags)
}

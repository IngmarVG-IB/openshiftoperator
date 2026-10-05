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
	"fmt"

	"github.com/infobloxopen/universal-ddi-go-client/dnsdata"
	"sigs.k8s.io/controller-runtime/pkg/client"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	uddisync "github.com/IngmarVG-IB/openshiftoperator/internal/sync"
	"github.com/IngmarVG-IB/openshiftoperator/internal/tags"
	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// depZone is the Deps key holding the resolved Universal DDI auth zone id.
const depZone = "zone"

// sourceStatic is the only Record source the operator ever adopts, updates or deletes.
// SYSTEM, DYNAMIC, DELEGATED and DTC records are reported as conflicts and left alone.
const sourceStatic = "STATIC"

// recordAdapter maps the Record CR onto dnsdata.Record, following the shape of
// viewAdapter (view_adapter.go).
type recordAdapter struct{}

var _ uddisync.Adapter[uddiv1alpha1.RecordSpec, dnsdata.Record] = recordAdapter{}

// Kind returns the CR kind name.
func (recordAdapter) Kind() string { return "Record" }

// nameInZone returns the Universal DDI name_in_zone value for a CR's spec.name: "@" or
// the empty string both mean the apex, which Universal DDI represents as an empty
// name_in_zone.
func nameInZone(name string) string {
	if name == "@" {
		return ""
	}
	return name
}

// rdataOf builds the rdata map Universal DDI expects for spec.rdata, matching spec.type.
// Numeric subfields are built as int64 so they compare cleanly against both locally-built
// values and values that have round-tripped through JSON (which decodes numbers as
// float64); see rdataEqual.
func rdataOf(spec *uddiv1alpha1.RecordSpec) map[string]interface{} {
	switch spec.Type {
	case "A":
		if spec.RData.A == nil {
			return nil
		}
		return map[string]interface{}{"address": spec.RData.A.Address}
	case "AAAA":
		if spec.RData.AAAA == nil {
			return nil
		}
		return map[string]interface{}{"address": spec.RData.AAAA.Address}
	case "CNAME":
		if spec.RData.CNAME == nil {
			return nil
		}
		return map[string]interface{}{"cname": spec.RData.CNAME.CNAME}
	case "TXT":
		if spec.RData.TXT == nil {
			return nil
		}
		return map[string]interface{}{"text": spec.RData.TXT.Text}
	case "PTR":
		if spec.RData.PTR == nil {
			return nil
		}
		return map[string]interface{}{"dname": spec.RData.PTR.DName}
	case "SRV":
		if spec.RData.SRV == nil {
			return nil
		}
		return map[string]interface{}{
			"priority": int64(spec.RData.SRV.Priority),
			"weight":   int64(spec.RData.SRV.Weight),
			"port":     int64(spec.RData.SRV.Port),
			"target":   spec.RData.SRV.Target,
		}
	case "MX":
		if spec.RData.MX == nil {
			return nil
		}
		return map[string]interface{}{
			"preference": int64(spec.RData.MX.Preference),
			"exchange":   spec.RData.MX.Exchange,
		}
	case "NS":
		if spec.RData.NS == nil {
			return nil
		}
		return map[string]interface{}{"dname": spec.RData.NS.DName}
	case "CAA":
		if spec.RData.CAA == nil {
			return nil
		}
		return map[string]interface{}{
			"flags": int64(spec.RData.CAA.Flags),
			"tag":   spec.RData.CAA.Tag,
			"value": spec.RData.CAA.Value,
		}
	}
	return nil
}

// rdataEqual reports whether two rdata maps hold the same values. Numbers compare by
// value regardless of their concrete Go type (int64 locally, float64 after a JSON round
// trip); everything else compares as text.
func rdataEqual(a, b map[string]interface{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok || !scalarEqual(av, bv) {
			return false
		}
	}
	return true
}

func scalarEqual(a, b interface{}) bool {
	if af, ok := toFloat(a); ok {
		bf, ok2 := toFloat(b)
		return ok2 && af == bf
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}

func toFloat(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	}
	return 0, false
}

// hasStaticSource reports whether source contains STATIC.
func hasStaticSource(source []string) bool {
	for _, s := range source {
		if s == sourceStatic {
			return true
		}
	}
	return false
}

// Resolve turns spec.zoneRef into a Universal DDI auth zone id.
func (recordAdapter) Resolve(ctx context.Context, c client.Client, obj client.Object, spec *uddiv1alpha1.RecordSpec) (uddisync.Deps, error) {
	zoneID, err := uddisync.ResolveRef(ctx, c, obj.GetNamespace(), spec.ZoneRef, &uddiv1alpha1.AuthZone{}, "AuthZone")
	if err != nil {
		return nil, err
	}
	return uddisync.Deps{depZone: zoneID}, nil
}

// Find looks a record up by its natural key: zone plus name plus type, then matches the
// rdata client-side, since Universal DDI stores one rdata per record object and two A
// records for the same name are two distinct objects.
//
// A match whose source is not STATIC can never be adopted, updated or deleted, no matter
// what lifecycle.adoptExisting says, so it is reported through sync.ErrConflict rather
// than returned as a normal find result; the generic loop turns that into
// Ready=False/AlreadyExists without ever calling Create, Update or Delete.
func (recordAdapter) Find(ctx context.Context, uc *uddi.Client, spec *uddiv1alpha1.RecordSpec, deps uddisync.Deps) (*dnsdata.Record, error) {
	zone := deps[depZone]
	if zone == "" {
		return nil, nil
	}
	name := nameInZone(spec.Name)
	res, err := uc.DNSData.ListRecords(ctx, uddi.ListOptions{
		Filter: "zone==" + quoteFilterValue(zone) +
			" and name_in_zone==" + quoteFilterValue(name) +
			" and type==" + quoteFilterValue(string(spec.Type)),
	})
	if err != nil {
		return nil, err
	}
	want := rdataOf(spec)
	for i := range res {
		if !rdataEqual(res[i].Rdata, want) {
			continue
		}
		found := res[i]
		if !hasStaticSource(found.Source) {
			return nil, fmt.Errorf("%w: Universal DDI record %s already exists with source %v; only STATIC records may be adopted, updated or deleted",
				uddisync.ErrConflict, strPtr(found.Id), found.Source)
		}
		return &found, nil
	}
	return nil, nil
}

// FindByUID looks a record up by the ownership uid tag.
func (recordAdapter) FindByUID(ctx context.Context, uc *uddi.Client, uid string) (*dnsdata.Record, error) {
	if uid == "" {
		return nil, nil
	}
	res, err := uc.DNSData.ListRecords(ctx, uddi.ListOptions{
		TFilter: tags.UIDFilter(uid),
		Limit:   1,
	})
	if err != nil || len(res) == 0 {
		return nil, err
	}
	return &res[0], nil
}

// createOptions builds the request-only options map: create_ptr when spec.createPtr is
// set on an A or AAAA record.
func createOptions(spec *uddiv1alpha1.RecordSpec) map[string]interface{} {
	if spec.CreatePtr == nil || !*spec.CreatePtr {
		return nil
	}
	if spec.Type != "A" && spec.Type != "AAAA" {
		return nil
	}
	return map[string]interface{}{"create_ptr": true}
}

// Create makes the Universal DDI record.
func (recordAdapter) Create(ctx context.Context, uc *uddi.Client, spec *uddiv1alpha1.RecordSpec, deps uddisync.Deps, t map[string]string) (*dnsdata.Record, error) {
	zone := deps[depZone]
	name := nameInZone(spec.Name)
	typ := string(spec.Type)
	in := dnsdata.Record{
		Type:       &typ,
		Zone:       &zone,
		NameInZone: &name,
		Rdata:      rdataOf(spec),
	}
	if spec.TTL != nil {
		ttl := int64(*spec.TTL)
		in.Ttl = &ttl
	}
	if spec.Comment != "" {
		c := spec.Comment
		in.Comment = &c
	}
	if spec.Disabled {
		d := true
		in.Disabled = &d
	}
	if opts := createOptions(spec); opts != nil {
		in.Options = opts
	}
	in.Tags = uddi.ToAPITags(t)
	return uc.DNSData.CreateRecord(ctx, in)
}

// Get reads a record by id.
func (recordAdapter) Get(ctx context.Context, uc *uddi.Client, id string) (*dnsdata.Record, error) {
	return uc.DNSData.GetRecord(ctx, id)
}

// Diff compares the mutable managed fields (rdata, ttl, comment, disabled, tags) and
// builds a partial patch. type, zone and name are immutable (enforced by CEL on the CRD)
// and are never diffed; options is create-only and is never re-sent on update.
func (recordAdapter) Diff(spec *uddiv1alpha1.RecordSpec, deps uddisync.Deps, t tags.Desired, current *dnsdata.Record) (*dnsdata.Record, bool) {
	// The SDK serialises rdata unconditionally (no omitempty), so a patch that left it nil
	// would send "rdata": null and clobber the record data. The desired rdata therefore
	// rides along on every patch, whether or not it is what changed.
	want := rdataOf(spec)
	patch := &dnsdata.Record{Rdata: want}
	changed := !rdataEqual(current.Rdata, want)

	if spec.TTL != nil {
		wantTTL := int64(*spec.TTL)
		if current.Ttl == nil || *current.Ttl != wantTTL {
			v := wantTTL
			patch.Ttl = &v
			changed = true
		}
	}
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
	if merged, tagsChanged := tags.ForPatchAPI(current.Tags, t); tagsChanged {
		patch.Tags = merged
		changed = true
	}
	return patch, changed
}

// Update applies a partial patch.
func (recordAdapter) Update(ctx context.Context, uc *uddi.Client, id string, patch *dnsdata.Record) (*dnsdata.Record, error) {
	return uc.DNSData.UpdateRecord(ctx, id, *patch)
}

// Delete removes the record.
func (recordAdapter) Delete(ctx context.Context, uc *uddi.Client, id string) error {
	return uc.DNSData.DeleteRecord(ctx, id)
}

// Observe copies observed fields into the CR status.
func (recordAdapter) Observe(obj client.Object, current *dnsdata.Record) {
	r, ok := obj.(*uddiv1alpha1.Record)
	if !ok || current == nil {
		return
	}
	r.Status.AbsoluteName = strPtr(current.AbsoluteNameSpec)
	r.Status.Source = append([]string(nil), current.Source...)
	r.Status.Zone = strPtr(current.Zone)
}

// IDOf returns the Universal DDI resource id.
func (recordAdapter) IDOf(current *dnsdata.Record) string { return strPtr(current.Id) }

// TagsOf returns the tags currently set on the record.
func (recordAdapter) TagsOf(current *dnsdata.Record) map[string]string {
	return uddi.FromAPITags(current.Tags)
}

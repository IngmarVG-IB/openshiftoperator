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

package uddi

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"

	"github.com/infobloxopen/universal-ddi-go-client/dnsconfig"
	"github.com/infobloxopen/universal-ddi-go-client/dnsdata"
	"github.com/infobloxopen/universal-ddi-go-client/ipam"
)

// Fake is an in-memory stand-in for the Universal DDI API. It enforces the same
// natural-key uniqueness rules as the real service, evaluates _filter and _tfilter
// expressions, and supports all three next-available address forms
// ("/nextavailableip", "/nextavailablesubnet", "/nextavailableaddressblock"), so the
// adoption, conflict and allocation paths can be tested without a tenant.
//
// One deliberate simplification: Delete removes the object outright, whereas Universal
// DDI moves most objects (views, zones, records, IP spaces and most IPAM objects) to a
// recycle bin for 30 days. Do not build tests on the assumption that a natural key is
// immediately reusable after a delete against the real API.
//
// Every method is safe for concurrent use.
type Fake struct {
	mu  sync.Mutex
	seq int

	views    map[string]dnsconfig.View
	zones    map[string]dnsconfig.AuthZone
	records  map[string]dnsdata.Record
	spaces   map[string]ipam.IPSpace
	blocks   map[string]ipam.AddressBlock
	subnets  map[string]ipam.Subnet
	ranges   map[string]ipam.Range
	adresses map[string]ipam.Address
	fixed    map[string]ipam.FixedAddress

	errs  map[string][]error
	calls map[string]int
}

// NewFake returns an empty Fake wired into a Client.
func NewFake() *Fake {
	return &Fake{
		views:    map[string]dnsconfig.View{},
		zones:    map[string]dnsconfig.AuthZone{},
		records:  map[string]dnsdata.Record{},
		spaces:   map[string]ipam.IPSpace{},
		blocks:   map[string]ipam.AddressBlock{},
		subnets:  map[string]ipam.Subnet{},
		ranges:   map[string]ipam.Range{},
		adresses: map[string]ipam.Address{},
		fixed:    map[string]ipam.FixedAddress{},
		errs:     map[string][]error{},
		calls:    map[string]int{},
	}
}

// Client returns a Client whose four domain interfaces all point at this Fake.
func (f *Fake) Client() *Client {
	return &Client{DNSConfig: f, DNSData: f, IPAM: f, DHCP: f}
}

// PushError queues an error to be returned by the next call to the named method, for
// example "ListViews". Queued errors are consumed in order.
func (f *Fake) PushError(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errs[method] = append(f.errs[method], err)
}

// PushStatus queues an APIError with the given HTTP status for the next call to method.
func (f *Fake) PushStatus(method string, status int, body string) {
	f.PushError(method, NewAPIError("fake", method, status, body))
}

// CallCount reports how many times the named method was called.
func (f *Fake) CallCount(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[method]
}

// Reset clears every stored object, queued error and call counter.
func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq = 0
	f.views = map[string]dnsconfig.View{}
	f.zones = map[string]dnsconfig.AuthZone{}
	f.records = map[string]dnsdata.Record{}
	f.spaces = map[string]ipam.IPSpace{}
	f.blocks = map[string]ipam.AddressBlock{}
	f.subnets = map[string]ipam.Subnet{}
	f.ranges = map[string]ipam.Range{}
	f.adresses = map[string]ipam.Address{}
	f.fixed = map[string]ipam.FixedAddress{}
	f.errs = map[string][]error{}
	f.calls = map[string]int{}
}

// enter records the call and pops a queued error. The caller must not hold the lock.
func (f *Fake) enter(method string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[method]++
	if q := f.errs[method]; len(q) > 0 {
		err := q[0]
		f.errs[method] = q[1:]
		return err
	}
	return nil
}

func (f *Fake) nextID(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s/%08x-0000-4000-8000-%012x", prefix, f.seq, f.seq)
}

func notFound(domain, method, id string) error {
	return NewAPIError(domain, method, http.StatusNotFound, fmt.Sprintf("object %q not found", id))
}

func conflict(domain, method, key string) error {
	return NewAPIError(domain, method, http.StatusConflict, fmt.Sprintf("an object with %s already exists", key))
}

func badRequest(domain, method, msg string) error {
	return NewAPIError(domain, method, http.StatusBadRequest, msg)
}

func copyAnyMap(in map[string]interface{}) map[string]interface{} {
	if in == nil {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copySlice[T any](in []T) []T {
	if in == nil {
		return nil
	}
	out := make([]T, len(in))
	copy(out, in)
	return out
}

func strv(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func i64v(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// filtered walks a map of stored objects in id order, applies the compiled _filter and
// _tfilter expressions and returns at most opts.Limit results starting at opts.Offset.
func filtered[T any](
	domain, method string,
	store map[string]T,
	opts ListOptions,
	fields func(T) map[string]any,
	tags func(T) map[string]interface{},
) ([]T, error) {
	fe, err := parseFilter(opts.Filter)
	if err != nil {
		return nil, badRequest(domain, method, "invalid _filter: "+err.Error())
	}
	te, err := parseFilter(opts.TFilter)
	if err != nil {
		return nil, badRequest(domain, method, "invalid _tfilter: "+err.Error())
	}

	ids := make([]string, 0, len(store))
	for id := range store {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := []T{}
	for _, id := range ids {
		obj := store[id]
		if !matchFilter(fe, fields(obj)) {
			continue
		}
		if te != nil {
			tf := map[string]any{}
			for k, v := range tags(obj) {
				tf[k] = v
			}
			if !matchFilter(te, tf) {
				continue
			}
		}
		out = append(out, obj)
	}
	if opts.Offset > 0 {
		if int(opts.Offset) >= len(out) {
			return []T{}, nil
		}
		out = out[opts.Offset:]
	}
	if opts.Limit > 0 && int(opts.Limit) < len(out) {
		out = out[:opts.Limit]
	}
	return out, nil
}

// --- View ---

func cloneView(in dnsconfig.View) dnsconfig.View {
	out := in
	out.Tags = copyAnyMap(in.Tags)
	out.IpSpaces = copySlice(in.IpSpaces)
	return out
}

func viewFields(v dnsconfig.View) map[string]any {
	return map[string]any{
		"id":      strv(v.Id),
		"name":    v.Name,
		"comment": strv(v.Comment),
	}
}

// CreateView stores a new view. The natural key is the view name.
func (f *Fake) CreateView(ctx context.Context, in dnsconfig.View) (*dnsconfig.View, error) {
	if err := f.enter("CreateView"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if in.Name == "" {
		return nil, badRequest(DomainDNSConfig, "CreateView", "name is required")
	}
	for _, v := range f.views {
		if v.Name == in.Name {
			return nil, conflict(DomainDNSConfig, "CreateView", "name "+in.Name)
		}
	}
	id := f.nextID(PrefixView)
	obj := cloneView(in)
	obj.Id = &id
	f.views[id] = obj
	out := cloneView(obj)
	return &out, nil
}

// GetView reads a view by id.
func (f *Fake) GetView(ctx context.Context, id string) (*dnsconfig.View, error) {
	if err := f.enter("GetView"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.views[QualifyID(PrefixView, id)]
	if !ok {
		return nil, notFound(DomainDNSConfig, "GetView", id)
	}
	out := cloneView(obj)
	return &out, nil
}

// ListViews lists views matching the filters.
func (f *Fake) ListViews(ctx context.Context, opts ListOptions) ([]dnsconfig.View, error) {
	if err := f.enter("ListViews"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	res, err := filtered(DomainDNSConfig, "ListViews", f.views, opts, viewFields,
		func(v dnsconfig.View) map[string]interface{} { return v.Tags })
	if err != nil {
		return nil, err
	}
	out := make([]dnsconfig.View, 0, len(res))
	for _, v := range res {
		out = append(out, cloneView(v))
	}
	return out, nil
}

// UpdateView applies a partial patch to a view.
func (f *Fake) UpdateView(ctx context.Context, id string, patch dnsconfig.View) (*dnsconfig.View, error) {
	if err := f.enter("UpdateView"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := QualifyID(PrefixView, id)
	obj, ok := f.views[key]
	if !ok {
		return nil, notFound(DomainDNSConfig, "UpdateView", id)
	}
	if patch.Name != "" && patch.Name != obj.Name {
		for k, v := range f.views {
			if k != key && v.Name == patch.Name {
				return nil, conflict(DomainDNSConfig, "UpdateView", "name "+patch.Name)
			}
		}
		obj.Name = patch.Name
	}
	if patch.Comment != nil {
		obj.Comment = patch.Comment
	}
	if patch.Disabled != nil {
		obj.Disabled = patch.Disabled
	}
	if patch.IpSpaces != nil {
		obj.IpSpaces = copySlice(patch.IpSpaces)
	}
	if patch.Tags != nil {
		obj.Tags = copyAnyMap(patch.Tags)
	}
	f.views[key] = obj
	out := cloneView(obj)
	return &out, nil
}

// DeleteView removes a view.
func (f *Fake) DeleteView(ctx context.Context, id string) error {
	if err := f.enter("DeleteView"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := QualifyID(PrefixView, id)
	if _, ok := f.views[key]; !ok {
		return notFound(DomainDNSConfig, "DeleteView", id)
	}
	delete(f.views, key)
	return nil
}

// --- AuthZone ---

func cloneAuthZone(in dnsconfig.AuthZone) dnsconfig.AuthZone {
	out := in
	out.Tags = copyAnyMap(in.Tags)
	out.Nsgs = copySlice(in.Nsgs)
	out.InternalSecondaries = copySlice(in.InternalSecondaries)
	out.ExternalPrimaries = copySlice(in.ExternalPrimaries)
	out.ExternalSecondaries = copySlice(in.ExternalSecondaries)
	out.Warnings = copySlice(in.Warnings)
	return out
}

func authZoneFields(z dnsconfig.AuthZone) map[string]any {
	return map[string]any{
		"id":            strv(z.Id),
		"fqdn":          strv(z.Fqdn),
		"protocol_fqdn": strv(z.ProtocolFqdn),
		"view":          strv(z.View),
		"primary_type":  strv(z.PrimaryType),
		"comment":       strv(z.Comment),
	}
}

// CreateAuthZone stores a new zone. The natural key is fqdn plus view.
func (f *Fake) CreateAuthZone(ctx context.Context, in dnsconfig.AuthZone) (*dnsconfig.AuthZone, error) {
	if err := f.enter("CreateAuthZone"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if strv(in.Fqdn) == "" {
		return nil, badRequest(DomainDNSConfig, "CreateAuthZone", "fqdn is required")
	}
	for _, z := range f.zones {
		if strv(z.Fqdn) == strv(in.Fqdn) && strv(z.View) == strv(in.View) {
			return nil, conflict(DomainDNSConfig, "CreateAuthZone", "fqdn "+strv(in.Fqdn)+" in view "+strv(in.View))
		}
	}
	id := f.nextID(PrefixAuthZone)
	obj := cloneAuthZone(in)
	obj.Id = &id
	if obj.ProtocolFqdn == nil {
		pf := strv(in.Fqdn)
		obj.ProtocolFqdn = &pf
	}
	f.zones[id] = obj
	out := cloneAuthZone(obj)
	return &out, nil
}

// GetAuthZone reads a zone by id.
func (f *Fake) GetAuthZone(ctx context.Context, id string) (*dnsconfig.AuthZone, error) {
	if err := f.enter("GetAuthZone"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.zones[QualifyID(PrefixAuthZone, id)]
	if !ok {
		return nil, notFound(DomainDNSConfig, "GetAuthZone", id)
	}
	out := cloneAuthZone(obj)
	return &out, nil
}

// ListAuthZones lists zones matching the filters.
func (f *Fake) ListAuthZones(ctx context.Context, opts ListOptions) ([]dnsconfig.AuthZone, error) {
	if err := f.enter("ListAuthZones"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	res, err := filtered(DomainDNSConfig, "ListAuthZones", f.zones, opts, authZoneFields,
		func(z dnsconfig.AuthZone) map[string]interface{} { return z.Tags })
	if err != nil {
		return nil, err
	}
	out := make([]dnsconfig.AuthZone, 0, len(res))
	for _, z := range res {
		out = append(out, cloneAuthZone(z))
	}
	return out, nil
}

// UpdateAuthZone applies a partial patch to a zone.
func (f *Fake) UpdateAuthZone(ctx context.Context, id string, patch dnsconfig.AuthZone) (*dnsconfig.AuthZone, error) {
	if err := f.enter("UpdateAuthZone"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := QualifyID(PrefixAuthZone, id)
	obj, ok := f.zones[key]
	if !ok {
		return nil, notFound(DomainDNSConfig, "UpdateAuthZone", id)
	}
	if patch.Comment != nil {
		obj.Comment = patch.Comment
	}
	if patch.Disabled != nil {
		obj.Disabled = patch.Disabled
	}
	if patch.Nsgs != nil {
		obj.Nsgs = copySlice(patch.Nsgs)
	}
	if patch.InternalSecondaries != nil {
		obj.InternalSecondaries = copySlice(patch.InternalSecondaries)
	}
	if patch.ExternalPrimaries != nil {
		obj.ExternalPrimaries = copySlice(patch.ExternalPrimaries)
	}
	if patch.Tags != nil {
		obj.Tags = copyAnyMap(patch.Tags)
	}
	f.zones[key] = obj
	out := cloneAuthZone(obj)
	return &out, nil
}

// DeleteAuthZone removes a zone. Zones with records cannot be deleted, mirroring the API.
func (f *Fake) DeleteAuthZone(ctx context.Context, id string) error {
	if err := f.enter("DeleteAuthZone"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := QualifyID(PrefixAuthZone, id)
	if _, ok := f.zones[key]; !ok {
		return notFound(DomainDNSConfig, "DeleteAuthZone", id)
	}
	for _, r := range f.records {
		if strv(r.Zone) == key {
			return badRequest(DomainDNSConfig, "DeleteAuthZone", "zone still contains records")
		}
	}
	delete(f.zones, key)
	return nil
}

// --- Record ---

func cloneRecord(in dnsdata.Record) dnsdata.Record {
	out := in
	out.Tags = copyAnyMap(in.Tags)
	out.Rdata = copyAnyMap(in.Rdata)
	out.Options = copyAnyMap(in.Options)
	out.Source = copySlice(in.Source)
	return out
}

// rdataKey canonicalises an rdata map so two records with the same content collide.
func rdataKey(rdata map[string]interface{}) string {
	keys := make([]string, 0, len(rdata))
	for k := range rdata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%v;", k, rdata[k])
	}
	return b.String()
}

func recordFields(r dnsdata.Record) map[string]any {
	return map[string]any{
		"id":                 strv(r.Id),
		"zone":               strv(r.Zone),
		"name_in_zone":       strv(r.NameInZone),
		"type":               strv(r.Type),
		"absolute_name_spec": strv(r.AbsoluteNameSpec),
		"view":               strv(r.View),
		"comment":            strv(r.Comment),
	}
}

// CreateRecord stores a new record. The natural key is zone plus name plus type plus rdata.
func (f *Fake) CreateRecord(ctx context.Context, in dnsdata.Record) (*dnsdata.Record, error) {
	if err := f.enter("CreateRecord"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(in.Rdata) == 0 {
		return nil, badRequest(DomainDNSData, "CreateRecord", "rdata is required")
	}
	key := rdataKey(in.Rdata)
	for _, r := range f.records {
		if strv(r.Zone) == strv(in.Zone) &&
			strv(r.NameInZone) == strv(in.NameInZone) &&
			strv(r.Type) == strv(in.Type) &&
			rdataKey(r.Rdata) == key {
			return nil, conflict(DomainDNSData, "CreateRecord",
				fmt.Sprintf("record %s/%s in zone %s", strv(in.NameInZone), strv(in.Type), strv(in.Zone)))
		}
	}
	id := f.nextID(PrefixRecord)
	obj := cloneRecord(in)
	obj.Id = &id
	if obj.Source == nil {
		obj.Source = []string{"STATIC"}
	}
	if obj.AbsoluteNameSpec == nil {
		abs := strv(in.NameInZone)
		if z, ok := f.zones[strv(in.Zone)]; ok {
			fqdn := strv(z.Fqdn)
			if abs == "" || abs == "@" {
				abs = fqdn
			} else {
				abs = abs + "." + fqdn
			}
		}
		obj.AbsoluteNameSpec = &abs
	}
	f.records[id] = obj
	out := cloneRecord(obj)
	return &out, nil
}

// GetRecord reads a record by id.
func (f *Fake) GetRecord(ctx context.Context, id string) (*dnsdata.Record, error) {
	if err := f.enter("GetRecord"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.records[QualifyID(PrefixRecord, id)]
	if !ok {
		return nil, notFound(DomainDNSData, "GetRecord", id)
	}
	out := cloneRecord(obj)
	return &out, nil
}

// ListRecords lists records matching the filters.
func (f *Fake) ListRecords(ctx context.Context, opts ListOptions) ([]dnsdata.Record, error) {
	if err := f.enter("ListRecords"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	res, err := filtered(DomainDNSData, "ListRecords", f.records, opts, recordFields,
		func(r dnsdata.Record) map[string]interface{} { return r.Tags })
	if err != nil {
		return nil, err
	}
	out := make([]dnsdata.Record, 0, len(res))
	for _, r := range res {
		out = append(out, cloneRecord(r))
	}
	return out, nil
}

// UpdateRecord applies a partial patch to a record.
func (f *Fake) UpdateRecord(ctx context.Context, id string, patch dnsdata.Record) (*dnsdata.Record, error) {
	if err := f.enter("UpdateRecord"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := QualifyID(PrefixRecord, id)
	obj, ok := f.records[key]
	if !ok {
		return nil, notFound(DomainDNSData, "UpdateRecord", id)
	}
	if len(patch.Rdata) > 0 {
		obj.Rdata = copyAnyMap(patch.Rdata)
	}
	if patch.Ttl != nil {
		obj.Ttl = patch.Ttl
	}
	if patch.Comment != nil {
		obj.Comment = patch.Comment
	}
	if patch.Disabled != nil {
		obj.Disabled = patch.Disabled
	}
	if patch.Options != nil {
		obj.Options = copyAnyMap(patch.Options)
	}
	if patch.Tags != nil {
		obj.Tags = copyAnyMap(patch.Tags)
	}
	f.records[key] = obj
	out := cloneRecord(obj)
	return &out, nil
}

// DeleteRecord removes a record.
func (f *Fake) DeleteRecord(ctx context.Context, id string) error {
	if err := f.enter("DeleteRecord"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := QualifyID(PrefixRecord, id)
	if _, ok := f.records[key]; !ok {
		return notFound(DomainDNSData, "DeleteRecord", id)
	}
	delete(f.records, key)
	return nil
}

// --- IPSpace ---

func cloneIPSpace(in ipam.IPSpace) ipam.IPSpace {
	out := in
	out.Tags = copyAnyMap(in.Tags)
	out.DhcpOptions = copySlice(in.DhcpOptions)
	return out
}

func ipSpaceFields(s ipam.IPSpace) map[string]any {
	return map[string]any{
		"id":      strv(s.Id),
		"name":    s.Name,
		"comment": strv(s.Comment),
	}
}

// CreateIPSpace stores a new IP space. The natural key is the name.
func (f *Fake) CreateIPSpace(ctx context.Context, in ipam.IPSpace) (*ipam.IPSpace, error) {
	if err := f.enter("CreateIPSpace"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if in.Name == "" {
		return nil, badRequest(DomainIPAM, "CreateIPSpace", "name is required")
	}
	for _, s := range f.spaces {
		if s.Name == in.Name {
			return nil, conflict(DomainIPAM, "CreateIPSpace", "name "+in.Name)
		}
	}
	id := f.nextID(PrefixIPSpace)
	obj := cloneIPSpace(in)
	obj.Id = &id
	f.spaces[id] = obj
	out := cloneIPSpace(obj)
	return &out, nil
}

// GetIPSpace reads an IP space by id.
func (f *Fake) GetIPSpace(ctx context.Context, id string) (*ipam.IPSpace, error) {
	if err := f.enter("GetIPSpace"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.spaces[QualifyID(PrefixIPSpace, id)]
	if !ok {
		return nil, notFound(DomainIPAM, "GetIPSpace", id)
	}
	out := cloneIPSpace(obj)
	return &out, nil
}

// ListIPSpaces lists IP spaces matching the filters.
func (f *Fake) ListIPSpaces(ctx context.Context, opts ListOptions) ([]ipam.IPSpace, error) {
	if err := f.enter("ListIPSpaces"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	res, err := filtered(DomainIPAM, "ListIPSpaces", f.spaces, opts, ipSpaceFields,
		func(s ipam.IPSpace) map[string]interface{} { return s.Tags })
	if err != nil {
		return nil, err
	}
	out := make([]ipam.IPSpace, 0, len(res))
	for _, s := range res {
		out = append(out, cloneIPSpace(s))
	}
	return out, nil
}

// UpdateIPSpace applies a partial patch to an IP space.
func (f *Fake) UpdateIPSpace(ctx context.Context, id string, patch ipam.IPSpace) (*ipam.IPSpace, error) {
	if err := f.enter("UpdateIPSpace"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := QualifyID(PrefixIPSpace, id)
	obj, ok := f.spaces[key]
	if !ok {
		return nil, notFound(DomainIPAM, "UpdateIPSpace", id)
	}
	if patch.Name != "" {
		obj.Name = patch.Name
	}
	if patch.Comment != nil {
		obj.Comment = patch.Comment
	}
	if patch.DhcpOptions != nil {
		obj.DhcpOptions = copySlice(patch.DhcpOptions)
	}
	if patch.Tags != nil {
		obj.Tags = copyAnyMap(patch.Tags)
	}
	f.spaces[key] = obj
	out := cloneIPSpace(obj)
	return &out, nil
}

// DeleteIPSpace removes an IP space.
func (f *Fake) DeleteIPSpace(ctx context.Context, id string) error {
	if err := f.enter("DeleteIPSpace"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := QualifyID(PrefixIPSpace, id)
	if _, ok := f.spaces[key]; !ok {
		return notFound(DomainIPAM, "DeleteIPSpace", id)
	}
	delete(f.spaces, key)
	return nil
}

// --- AddressBlock ---

func cloneAddressBlock(in ipam.AddressBlock) ipam.AddressBlock {
	out := in
	out.Tags = copyAnyMap(in.Tags)
	out.DhcpOptions = copySlice(in.DhcpOptions)
	return out
}

func addressBlockFields(b ipam.AddressBlock) map[string]any {
	return map[string]any{
		"id":      strv(b.Id),
		"address": strv(b.Address),
		"cidr":    i64v(b.Cidr),
		"space":   strv(b.Space),
		"name":    strv(b.Name),
		"comment": strv(b.Comment),
	}
}

// CreateAddressBlock stores a new address block. The natural key is address plus cidr
// plus space.
func (f *Fake) CreateAddressBlock(ctx context.Context, in ipam.AddressBlock) (*ipam.AddressBlock, error) {
	if err := f.enter("CreateAddressBlock"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	address := strv(in.Address)
	if address == "" {
		return nil, badRequest(DomainIPAM, "CreateAddressBlock", "address is required")
	}
	parent := ""
	if parentID, suffix, ok := ParentOfNextAvailable(address); ok {
		if suffix != NextAvailableBlockSuffix {
			return nil, badRequest(DomainIPAM, "CreateAddressBlock", "unsupported address form "+suffix)
		}
		alloc, err := f.allocateNextPrefixLocked(DomainIPAM, "CreateAddressBlock", parentID, int(i64v(in.Cidr)), strv(in.Space))
		if err != nil {
			return nil, err
		}
		address, parent = alloc, parentID
	}
	for _, b := range f.blocks {
		if strv(b.Address) == address && i64v(b.Cidr) == i64v(in.Cidr) && strv(b.Space) == strv(in.Space) {
			return nil, conflict(DomainIPAM, "CreateAddressBlock",
				fmt.Sprintf("address %s/%d in space %s", address, i64v(in.Cidr), strv(in.Space)))
		}
	}
	id := f.nextID(PrefixAddressBlock)
	obj := cloneAddressBlock(in)
	obj.Id = &id
	obj.Address = &address
	if parent != "" && obj.Parent == nil {
		obj.Parent = &parent
	}
	f.blocks[id] = obj
	out := cloneAddressBlock(obj)
	return &out, nil
}

// GetAddressBlock reads an address block by id.
func (f *Fake) GetAddressBlock(ctx context.Context, id string) (*ipam.AddressBlock, error) {
	if err := f.enter("GetAddressBlock"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.blocks[QualifyID(PrefixAddressBlock, id)]
	if !ok {
		return nil, notFound(DomainIPAM, "GetAddressBlock", id)
	}
	out := cloneAddressBlock(obj)
	return &out, nil
}

// ListAddressBlocks lists address blocks matching the filters.
func (f *Fake) ListAddressBlocks(ctx context.Context, opts ListOptions) ([]ipam.AddressBlock, error) {
	if err := f.enter("ListAddressBlocks"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	res, err := filtered(DomainIPAM, "ListAddressBlocks", f.blocks, opts, addressBlockFields,
		func(b ipam.AddressBlock) map[string]interface{} { return b.Tags })
	if err != nil {
		return nil, err
	}
	out := make([]ipam.AddressBlock, 0, len(res))
	for _, b := range res {
		out = append(out, cloneAddressBlock(b))
	}
	return out, nil
}

// UpdateAddressBlock applies a partial patch to an address block.
func (f *Fake) UpdateAddressBlock(ctx context.Context, id string, patch ipam.AddressBlock) (*ipam.AddressBlock, error) {
	if err := f.enter("UpdateAddressBlock"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := QualifyID(PrefixAddressBlock, id)
	obj, ok := f.blocks[key]
	if !ok {
		return nil, notFound(DomainIPAM, "UpdateAddressBlock", id)
	}
	if patch.Name != nil {
		obj.Name = patch.Name
	}
	if patch.Comment != nil {
		obj.Comment = patch.Comment
	}
	if patch.DhcpOptions != nil {
		obj.DhcpOptions = copySlice(patch.DhcpOptions)
	}
	if patch.Tags != nil {
		obj.Tags = copyAnyMap(patch.Tags)
	}
	f.blocks[key] = obj
	out := cloneAddressBlock(obj)
	return &out, nil
}

// DeleteAddressBlock removes an address block.
func (f *Fake) DeleteAddressBlock(ctx context.Context, id string) error {
	if err := f.enter("DeleteAddressBlock"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := QualifyID(PrefixAddressBlock, id)
	if _, ok := f.blocks[key]; !ok {
		return notFound(DomainIPAM, "DeleteAddressBlock", id)
	}
	delete(f.blocks, key)
	return nil
}

// --- Subnet ---

func cloneSubnet(in ipam.Subnet) ipam.Subnet {
	out := in
	out.Tags = copyAnyMap(in.Tags)
	out.DhcpOptions = copySlice(in.DhcpOptions)
	return out
}

func subnetFields(s ipam.Subnet) map[string]any {
	return map[string]any{
		"id":      strv(s.Id),
		"address": strv(s.Address),
		"cidr":    i64v(s.Cidr),
		"space":   strv(s.Space),
		"name":    strv(s.Name),
		"comment": strv(s.Comment),
	}
}

// CreateSubnet stores a new subnet. The natural key is address plus cidr plus space.
func (f *Fake) CreateSubnet(ctx context.Context, in ipam.Subnet) (*ipam.Subnet, error) {
	if err := f.enter("CreateSubnet"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	address := strv(in.Address)
	if address == "" {
		return nil, badRequest(DomainIPAM, "CreateSubnet", "address is required")
	}
	parent := ""
	if parentID, suffix, ok := ParentOfNextAvailable(address); ok {
		if suffix != NextAvailableSubnetSuffix {
			return nil, badRequest(DomainIPAM, "CreateSubnet", "unsupported address form "+suffix)
		}
		alloc, err := f.allocateNextPrefixLocked(DomainIPAM, "CreateSubnet", parentID, int(i64v(in.Cidr)), strv(in.Space))
		if err != nil {
			return nil, err
		}
		address, parent = alloc, parentID
	}
	for _, s := range f.subnets {
		if strv(s.Address) == address && i64v(s.Cidr) == i64v(in.Cidr) && strv(s.Space) == strv(in.Space) {
			return nil, conflict(DomainIPAM, "CreateSubnet",
				fmt.Sprintf("address %s/%d in space %s", address, i64v(in.Cidr), strv(in.Space)))
		}
	}
	id := f.nextID(PrefixSubnet)
	obj := cloneSubnet(in)
	obj.Id = &id
	obj.Address = &address
	if parent != "" && obj.Parent == nil {
		obj.Parent = &parent
	}
	f.subnets[id] = obj
	out := cloneSubnet(obj)
	return &out, nil
}

// GetSubnet reads a subnet by id.
func (f *Fake) GetSubnet(ctx context.Context, id string) (*ipam.Subnet, error) {
	if err := f.enter("GetSubnet"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.subnets[QualifyID(PrefixSubnet, id)]
	if !ok {
		return nil, notFound(DomainIPAM, "GetSubnet", id)
	}
	out := cloneSubnet(obj)
	return &out, nil
}

// ListSubnets lists subnets matching the filters.
func (f *Fake) ListSubnets(ctx context.Context, opts ListOptions) ([]ipam.Subnet, error) {
	if err := f.enter("ListSubnets"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	res, err := filtered(DomainIPAM, "ListSubnets", f.subnets, opts, subnetFields,
		func(s ipam.Subnet) map[string]interface{} { return s.Tags })
	if err != nil {
		return nil, err
	}
	out := make([]ipam.Subnet, 0, len(res))
	for _, s := range res {
		out = append(out, cloneSubnet(s))
	}
	return out, nil
}

// UpdateSubnet applies a partial patch to a subnet.
func (f *Fake) UpdateSubnet(ctx context.Context, id string, patch ipam.Subnet) (*ipam.Subnet, error) {
	if err := f.enter("UpdateSubnet"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := QualifyID(PrefixSubnet, id)
	obj, ok := f.subnets[key]
	if !ok {
		return nil, notFound(DomainIPAM, "UpdateSubnet", id)
	}
	if patch.Name != nil {
		obj.Name = patch.Name
	}
	if patch.Comment != nil {
		obj.Comment = patch.Comment
	}
	if patch.DhcpHost != nil {
		obj.DhcpHost = patch.DhcpHost
	}
	if patch.DhcpOptions != nil {
		obj.DhcpOptions = copySlice(patch.DhcpOptions)
	}
	if patch.DisableDhcp != nil {
		obj.DisableDhcp = patch.DisableDhcp
	}
	if patch.Tags != nil {
		obj.Tags = copyAnyMap(patch.Tags)
	}
	f.subnets[key] = obj
	out := cloneSubnet(obj)
	return &out, nil
}

// DeleteSubnet removes a subnet. Subnets that still contain addresses cannot be
// deleted, mirroring the API.
func (f *Fake) DeleteSubnet(ctx context.Context, id string) error {
	if err := f.enter("DeleteSubnet"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := QualifyID(PrefixSubnet, id)
	if _, ok := f.subnets[key]; !ok {
		return notFound(DomainIPAM, "DeleteSubnet", id)
	}
	for _, a := range f.adresses {
		if strv(a.Parent) == key {
			return badRequest(DomainIPAM, "DeleteSubnet", "subnet still contains addresses")
		}
	}
	delete(f.subnets, key)
	return nil
}

// --- Range ---

func cloneRange(in ipam.Range) ipam.Range {
	out := in
	out.Tags = copyAnyMap(in.Tags)
	out.DhcpOptions = copySlice(in.DhcpOptions)
	out.ExclusionRanges = copySlice(in.ExclusionRanges)
	return out
}

func rangeFields(r ipam.Range) map[string]any {
	return map[string]any{
		"id":      strv(r.Id),
		"start":   r.Start,
		"end":     r.End,
		"space":   strv(r.Space),
		"name":    strv(r.Name),
		"comment": strv(r.Comment),
	}
}

// CreateRange stores a new range. The natural key is start plus end plus space.
func (f *Fake) CreateRange(ctx context.Context, in ipam.Range) (*ipam.Range, error) {
	if err := f.enter("CreateRange"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if in.Start == "" || in.End == "" {
		return nil, badRequest(DomainIPAM, "CreateRange", "start and end are required")
	}
	for _, r := range f.ranges {
		if r.Start == in.Start && r.End == in.End && strv(r.Space) == strv(in.Space) {
			return nil, conflict(DomainIPAM, "CreateRange",
				fmt.Sprintf("range %s-%s in space %s", in.Start, in.End, strv(in.Space)))
		}
	}
	id := f.nextID(PrefixRange)
	obj := cloneRange(in)
	obj.Id = &id
	if obj.Parent == nil {
		if parent := f.subnetContainingLocked(in.Start, strv(in.Space)); parent != "" {
			obj.Parent = &parent
		}
	}
	f.ranges[id] = obj
	out := cloneRange(obj)
	return &out, nil
}

// GetRange reads a range by id.
func (f *Fake) GetRange(ctx context.Context, id string) (*ipam.Range, error) {
	if err := f.enter("GetRange"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.ranges[QualifyID(PrefixRange, id)]
	if !ok {
		return nil, notFound(DomainIPAM, "GetRange", id)
	}
	out := cloneRange(obj)
	return &out, nil
}

// ListRanges lists ranges matching the filters.
func (f *Fake) ListRanges(ctx context.Context, opts ListOptions) ([]ipam.Range, error) {
	if err := f.enter("ListRanges"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	res, err := filtered(DomainIPAM, "ListRanges", f.ranges, opts, rangeFields,
		func(r ipam.Range) map[string]interface{} { return r.Tags })
	if err != nil {
		return nil, err
	}
	out := make([]ipam.Range, 0, len(res))
	for _, r := range res {
		out = append(out, cloneRange(r))
	}
	return out, nil
}

// UpdateRange applies a partial patch to a range.
func (f *Fake) UpdateRange(ctx context.Context, id string, patch ipam.Range) (*ipam.Range, error) {
	if err := f.enter("UpdateRange"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := QualifyID(PrefixRange, id)
	obj, ok := f.ranges[key]
	if !ok {
		return nil, notFound(DomainIPAM, "UpdateRange", id)
	}
	if patch.Name != nil {
		obj.Name = patch.Name
	}
	if patch.Comment != nil {
		obj.Comment = patch.Comment
	}
	if patch.DhcpHost != nil {
		obj.DhcpHost = patch.DhcpHost
	}
	if patch.DhcpOptions != nil {
		obj.DhcpOptions = copySlice(patch.DhcpOptions)
	}
	if patch.ExclusionRanges != nil {
		obj.ExclusionRanges = copySlice(patch.ExclusionRanges)
	}
	if patch.DisableDhcp != nil {
		obj.DisableDhcp = patch.DisableDhcp
	}
	if patch.Tags != nil {
		obj.Tags = copyAnyMap(patch.Tags)
	}
	f.ranges[key] = obj
	out := cloneRange(obj)
	return &out, nil
}

// DeleteRange removes a range.
func (f *Fake) DeleteRange(ctx context.Context, id string) error {
	if err := f.enter("DeleteRange"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := QualifyID(PrefixRange, id)
	if _, ok := f.ranges[key]; !ok {
		return notFound(DomainIPAM, "DeleteRange", id)
	}
	delete(f.ranges, key)
	return nil
}

// --- Address ---

func cloneAddress(in ipam.Address) ipam.Address {
	out := in
	out.Tags = copyAnyMap(in.Tags)
	out.Names = copySlice(in.Names)
	out.Usage = copySlice(in.Usage)
	return out
}

func addressFields(a ipam.Address) map[string]any {
	return map[string]any{
		"id":      strv(a.Id),
		"address": a.Address,
		"space":   strv(a.Space),
		"state":   strv(a.State),
		"parent":  strv(a.Parent),
		"comment": strv(a.Comment),
		"hwaddr":  strv(a.Hwaddr),
	}
}

// CreateAddress reserves an address. When in.Address is "<parent id>/nextavailableip"
// the lowest free address inside that parent is allocated, atomically with the tags,
// which is how the operator does next-available allocation. Otherwise the natural key
// is address plus space.
func (f *Fake) CreateAddress(ctx context.Context, in ipam.Address) (*ipam.Address, error) {
	if err := f.enter("CreateAddress"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	space := strv(in.Space)
	address := in.Address
	parent := ""

	if parentID, suffix, ok := ParentOfNextAvailable(address); ok {
		if suffix != NextAvailableIPSuffix {
			return nil, badRequest(DomainIPAM, "CreateAddress", "unsupported address form "+suffix)
		}
		alloc, p, err := f.allocateNextIPLocked(parentID, space)
		if err != nil {
			return nil, err
		}
		address, parent = alloc, p
	} else {
		if address == "" {
			return nil, badRequest(DomainIPAM, "CreateAddress", "address is required")
		}
		for _, a := range f.adresses {
			if a.Address == address && strv(a.Space) == space {
				return nil, conflict(DomainIPAM, "CreateAddress",
					fmt.Sprintf("address %s in space %s", address, space))
			}
		}
		parent = f.subnetContainingLocked(address, space)
	}

	id := f.nextID(PrefixAddress)
	obj := cloneAddress(in)
	obj.Id = &id
	obj.Address = address
	if parent != "" {
		obj.Parent = &parent
	}
	if obj.State == nil {
		state := "used"
		obj.State = &state
	}
	f.adresses[id] = obj
	out := cloneAddress(obj)
	return &out, nil
}

// GetAddress reads an address by id.
func (f *Fake) GetAddress(ctx context.Context, id string) (*ipam.Address, error) {
	if err := f.enter("GetAddress"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.adresses[QualifyID(PrefixAddress, id)]
	if !ok {
		return nil, notFound(DomainIPAM, "GetAddress", id)
	}
	out := cloneAddress(obj)
	return &out, nil
}

// ListAddresses lists addresses matching the filters.
func (f *Fake) ListAddresses(ctx context.Context, opts ListOptions) ([]ipam.Address, error) {
	if err := f.enter("ListAddresses"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	res, err := filtered(DomainIPAM, "ListAddresses", f.adresses, opts, addressFields,
		func(a ipam.Address) map[string]interface{} { return a.Tags })
	if err != nil {
		return nil, err
	}
	out := make([]ipam.Address, 0, len(res))
	for _, a := range res {
		out = append(out, cloneAddress(a))
	}
	return out, nil
}

// UpdateAddress applies a partial patch to an address.
func (f *Fake) UpdateAddress(ctx context.Context, id string, patch ipam.Address) (*ipam.Address, error) {
	if err := f.enter("UpdateAddress"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := QualifyID(PrefixAddress, id)
	obj, ok := f.adresses[key]
	if !ok {
		return nil, notFound(DomainIPAM, "UpdateAddress", id)
	}
	if patch.Names != nil {
		obj.Names = copySlice(patch.Names)
	}
	if patch.Hwaddr != nil {
		obj.Hwaddr = patch.Hwaddr
	}
	if patch.Interface != nil {
		obj.Interface = patch.Interface
	}
	if patch.Comment != nil {
		obj.Comment = patch.Comment
	}
	if patch.Tags != nil {
		obj.Tags = copyAnyMap(patch.Tags)
	}
	f.adresses[key] = obj
	out := cloneAddress(obj)
	return &out, nil
}

// DeleteAddress releases an address.
func (f *Fake) DeleteAddress(ctx context.Context, id string) error {
	if err := f.enter("DeleteAddress"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := QualifyID(PrefixAddress, id)
	if _, ok := f.adresses[key]; !ok {
		return notFound(DomainIPAM, "DeleteAddress", id)
	}
	delete(f.adresses, key)
	return nil
}

// --- FixedAddress ---

func cloneFixedAddress(in ipam.FixedAddress) ipam.FixedAddress {
	out := in
	out.Tags = copyAnyMap(in.Tags)
	out.DhcpOptions = copySlice(in.DhcpOptions)
	return out
}

func fixedAddressFields(a ipam.FixedAddress) map[string]any {
	return map[string]any{
		"id":          strv(a.Id),
		"address":     a.Address,
		"ip_space":    strv(a.IpSpace),
		"match_type":  a.MatchType,
		"match_value": a.MatchValue,
		"name":        strv(a.Name),
		"comment":     strv(a.Comment),
	}
}

// CreateFixedAddress stores a new DHCP reservation. The natural key is address plus
// ip space.
func (f *Fake) CreateFixedAddress(ctx context.Context, in ipam.FixedAddress) (*ipam.FixedAddress, error) {
	if err := f.enter("CreateFixedAddress"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if in.Address == "" {
		return nil, badRequest(DomainDHCP, "CreateFixedAddress", "address is required")
	}
	for _, a := range f.fixed {
		if a.Address == in.Address && strv(a.IpSpace) == strv(in.IpSpace) {
			return nil, conflict(DomainDHCP, "CreateFixedAddress",
				fmt.Sprintf("address %s in space %s", in.Address, strv(in.IpSpace)))
		}
	}
	id := f.nextID(PrefixFixedAddress)
	obj := cloneFixedAddress(in)
	obj.Id = &id
	if obj.Parent == nil {
		if parent := f.subnetContainingLocked(in.Address, strv(in.IpSpace)); parent != "" {
			obj.Parent = &parent
		}
	}
	f.fixed[id] = obj
	out := cloneFixedAddress(obj)
	return &out, nil
}

// GetFixedAddress reads a DHCP reservation by id.
func (f *Fake) GetFixedAddress(ctx context.Context, id string) (*ipam.FixedAddress, error) {
	if err := f.enter("GetFixedAddress"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	obj, ok := f.fixed[QualifyID(PrefixFixedAddress, id)]
	if !ok {
		return nil, notFound(DomainDHCP, "GetFixedAddress", id)
	}
	out := cloneFixedAddress(obj)
	return &out, nil
}

// ListFixedAddresses lists DHCP reservations matching the filters.
func (f *Fake) ListFixedAddresses(ctx context.Context, opts ListOptions) ([]ipam.FixedAddress, error) {
	if err := f.enter("ListFixedAddresses"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	res, err := filtered(DomainDHCP, "ListFixedAddresses", f.fixed, opts, fixedAddressFields,
		func(a ipam.FixedAddress) map[string]interface{} { return a.Tags })
	if err != nil {
		return nil, err
	}
	out := make([]ipam.FixedAddress, 0, len(res))
	for _, a := range res {
		out = append(out, cloneFixedAddress(a))
	}
	return out, nil
}

// UpdateFixedAddress applies a partial patch to a DHCP reservation.
func (f *Fake) UpdateFixedAddress(ctx context.Context, id string, patch ipam.FixedAddress) (*ipam.FixedAddress, error) {
	if err := f.enter("UpdateFixedAddress"); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := QualifyID(PrefixFixedAddress, id)
	obj, ok := f.fixed[key]
	if !ok {
		return nil, notFound(DomainDHCP, "UpdateFixedAddress", id)
	}
	if patch.MatchValue != "" {
		obj.MatchValue = patch.MatchValue
	}
	if patch.Name != nil {
		obj.Name = patch.Name
	}
	if patch.Hostname != nil {
		obj.Hostname = patch.Hostname
	}
	if patch.Comment != nil {
		obj.Comment = patch.Comment
	}
	if patch.DisableDhcp != nil {
		obj.DisableDhcp = patch.DisableDhcp
	}
	if patch.DhcpOptions != nil {
		obj.DhcpOptions = copySlice(patch.DhcpOptions)
	}
	if patch.Tags != nil {
		obj.Tags = copyAnyMap(patch.Tags)
	}
	f.fixed[key] = obj
	out := cloneFixedAddress(obj)
	return &out, nil
}

// DeleteFixedAddress removes a DHCP reservation.
func (f *Fake) DeleteFixedAddress(ctx context.Context, id string) error {
	if err := f.enter("DeleteFixedAddress"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	key := QualifyID(PrefixFixedAddress, id)
	if _, ok := f.fixed[key]; !ok {
		return notFound(DomainDHCP, "DeleteFixedAddress", id)
	}
	delete(f.fixed, key)
	return nil
}

// --- next-available allocation ---

// allocateNextIPLocked returns the lowest free address inside the given parent, which
// may be a subnet, a range or an address block, plus the parent id to record on the
// object. The caller must hold the lock.
func (f *Fake) allocateNextIPLocked(parentID, space string) (string, string, error) {
	const method = "CreateAddress"

	var first, last netip.Addr
	var parent string

	switch {
	case strings.HasPrefix(parentID, PrefixSubnet+"/"):
		s, ok := f.subnets[parentID]
		if !ok {
			return "", "", notFound(DomainIPAM, method, parentID)
		}
		lo, hi, err := hostRange(strv(s.Address), int(i64v(s.Cidr)))
		if err != nil {
			return "", "", badRequest(DomainIPAM, method, err.Error())
		}
		first, last, parent = lo, hi, parentID
		if space == "" {
			space = strv(s.Space)
		}
	case strings.HasPrefix(parentID, PrefixAddressBlock+"/"):
		b, ok := f.blocks[parentID]
		if !ok {
			return "", "", notFound(DomainIPAM, method, parentID)
		}
		lo, hi, err := hostRange(strv(b.Address), int(i64v(b.Cidr)))
		if err != nil {
			return "", "", badRequest(DomainIPAM, method, err.Error())
		}
		first, last = lo, hi
		if space == "" {
			space = strv(b.Space)
		}
	case strings.HasPrefix(parentID, PrefixRange+"/"):
		r, ok := f.ranges[parentID]
		if !ok {
			return "", "", notFound(DomainIPAM, method, parentID)
		}
		lo, err := netip.ParseAddr(r.Start)
		if err != nil {
			return "", "", badRequest(DomainIPAM, method, "invalid range start: "+r.Start)
		}
		hi, err := netip.ParseAddr(r.End)
		if err != nil {
			return "", "", badRequest(DomainIPAM, method, "invalid range end: "+r.End)
		}
		first, last = lo, hi
		parent = strv(r.Parent)
		if space == "" {
			space = strv(r.Space)
		}
	default:
		return "", "", badRequest(DomainIPAM, method, "unsupported next-available parent "+parentID)
	}

	taken := map[string]bool{}
	for _, a := range f.adresses {
		if strv(a.Space) == space {
			taken[a.Address] = true
		}
	}
	for _, a := range f.fixed {
		if strv(a.IpSpace) == space {
			taken[a.Address] = true
		}
	}

	for ip := first; ip.IsValid() && ip.Compare(last) <= 0; ip = ip.Next() {
		if !taken[ip.String()] {
			if parent == "" {
				parent = f.subnetContainingLocked(ip.String(), space)
			}
			return ip.String(), parent, nil
		}
	}
	return "", "", badRequest(DomainIPAM, method, "no free addresses in "+parentID)
}

// allocateNextPrefixLocked returns the network address of the lowest free prefix of the
// requested cidr inside the given parent address block. Existing subnets and address
// blocks in the same space are treated as occupied. The caller must hold the lock.
func (f *Fake) allocateNextPrefixLocked(domain, method, parentID string, cidr int, space string) (string, error) {
	parentBlock, ok := f.blocks[parentID]
	if !ok {
		return "", notFound(domain, method, parentID)
	}
	pa, err := netip.ParseAddr(strv(parentBlock.Address))
	if err != nil {
		return "", badRequest(domain, method, "invalid parent address "+strv(parentBlock.Address))
	}
	parent, err := pa.Prefix(int(i64v(parentBlock.Cidr)))
	if err != nil {
		return "", badRequest(domain, method, "invalid parent prefix")
	}
	if cidr < parent.Bits() {
		return "", badRequest(domain, method, fmt.Sprintf("cidr /%d is larger than the parent /%d", cidr, parent.Bits()))
	}
	if space == "" {
		space = strv(parentBlock.Space)
	}

	occupied := make([]netip.Prefix, 0, len(f.subnets)+len(f.blocks))
	for id, s := range f.subnets {
		if strv(s.Space) != space {
			continue
		}
		if p, ok := parsePrefix(strv(s.Address), int(i64v(s.Cidr))); ok && id != parentID {
			occupied = append(occupied, p)
		}
	}
	for id, b := range f.blocks {
		if strv(b.Space) != space || id == parentID {
			continue
		}
		if p, ok := parsePrefix(strv(b.Address), int(i64v(b.Cidr))); ok {
			occupied = append(occupied, p)
		}
	}

	for addr := parent.Masked().Addr(); addr.IsValid() && parent.Contains(addr); {
		cand := netip.PrefixFrom(addr, cidr)
		free := true
		for _, o := range occupied {
			if prefixesOverlap(cand, o) {
				free = false
				break
			}
		}
		if free {
			return addr.String(), nil
		}
		addr = lastInPrefix(cand).Next()
	}
	return "", badRequest(domain, method, fmt.Sprintf("no free /%d in %s", cidr, parentID))
}

func parsePrefix(address string, cidr int) (netip.Prefix, bool) {
	a, err := netip.ParseAddr(address)
	if err != nil {
		return netip.Prefix{}, false
	}
	p, err := a.Prefix(cidr)
	if err != nil {
		return netip.Prefix{}, false
	}
	return p, true
}

func prefixesOverlap(a, b netip.Prefix) bool {
	return a.Contains(b.Masked().Addr()) || b.Contains(a.Masked().Addr())
}

// hostRange returns the first and last usable address of a prefix. For IPv4 prefixes
// shorter than /31 the network and broadcast addresses are skipped, matching what an
// IPAM hands out.
func hostRange(address string, cidr int) (netip.Addr, netip.Addr, error) {
	addr, err := netip.ParseAddr(address)
	if err != nil {
		return netip.Addr{}, netip.Addr{}, fmt.Errorf("invalid address %q", address)
	}
	prefix, err := addr.Prefix(cidr)
	if err != nil {
		return netip.Addr{}, netip.Addr{}, fmt.Errorf("invalid prefix %s/%d", address, cidr)
	}
	first := prefix.Masked().Addr()
	last := lastInPrefix(prefix)
	if addr.Is4() && cidr < 31 {
		first = first.Next()
		last = prevAddr(last)
	}
	return first, last, nil
}

func lastInPrefix(p netip.Prefix) netip.Addr {
	b := p.Masked().Addr().AsSlice()
	bits := p.Bits()
	for i := range b {
		lo := i * 8
		for bit := 0; bit < 8; bit++ {
			if lo+bit >= bits {
				b[i] |= 1 << (7 - bit)
			}
		}
	}
	out, _ := netip.AddrFromSlice(b)
	return out
}

func prevAddr(a netip.Addr) netip.Addr {
	b := a.AsSlice()
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] > 0 {
			b[i]--
			for j := i + 1; j < len(b); j++ {
				b[j] = 0xff
			}
			break
		}
	}
	out, _ := netip.AddrFromSlice(b)
	return out
}

// subnetContainingLocked returns the id of the subnet in the given space that contains
// address, or "". The caller must hold the lock.
func (f *Fake) subnetContainingLocked(address, space string) string {
	ip, err := netip.ParseAddr(address)
	if err != nil {
		return ""
	}
	best := ""
	bestBits := -1
	for id, s := range f.subnets {
		if strv(s.Space) != space {
			continue
		}
		sa, err := netip.ParseAddr(strv(s.Address))
		if err != nil {
			continue
		}
		p, err := sa.Prefix(int(i64v(s.Cidr)))
		if err != nil {
			continue
		}
		if p.Contains(ip) && p.Bits() > bestBits {
			best, bestBits = id, p.Bits()
		}
	}
	return best
}

// Compile-time assertions that the fake satisfies every domain interface.
var (
	_ DNSConfig = (*Fake)(nil)
	_ DNSData   = (*Fake)(nil)
	_ IPAM      = (*Fake)(nil)
	_ DHCP      = (*Fake)(nil)
)

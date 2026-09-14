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

// Package uddi wraps the Infoblox universal-ddi-go-client behind per-domain interfaces
// carrying exactly the methods the object-layer adapters need, plus an in-memory fake
// that enforces the same natural-key uniqueness rules as the real API.
package uddi

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/infobloxopen/universal-ddi-go-client/dnsconfig"
	"github.com/infobloxopen/universal-ddi-go-client/dnsdata"
	"github.com/infobloxopen/universal-ddi-go-client/ipam"
)

// SDK domain names used in APIError.Domain.
const (
	DomainDNSConfig = "dnsconfig"
	DomainDNSData   = "dnsdata"
	DomainIPAM      = "ipam"
	DomainDHCP      = "dhcp"
)

// ListOptions carries the query parameters the adapters use. Filter maps to _filter,
// TFilter to _tfilter, the rest to their obvious _-prefixed query parameters.
type ListOptions struct {
	// Filter is a _filter expression over JSON field names, for example
	// `name=="default"` or `address=="10.0.10.0" and cidr==24`.
	Filter string
	// TFilter is a _tfilter expression over tag keys, for example
	// `"uddi_operator_uid"=="<uid>"`.
	TFilter string
	// Limit caps the page size. Zero means the server default.
	Limit int32
	// Offset is the zero-origin index into the collection.
	Offset int32
	// Fields is a comma-separated _fields projection.
	Fields string
	// OrderBy is a comma-separated _order_by expression.
	OrderBy string
}

// DNSConfig covers the dnsconfig package: DNS views and authoritative zones.
type DNSConfig interface {
	CreateView(ctx context.Context, in dnsconfig.View) (*dnsconfig.View, error)
	GetView(ctx context.Context, id string) (*dnsconfig.View, error)
	ListViews(ctx context.Context, opts ListOptions) ([]dnsconfig.View, error)
	UpdateView(ctx context.Context, id string, patch dnsconfig.View) (*dnsconfig.View, error)
	DeleteView(ctx context.Context, id string) error

	CreateAuthZone(ctx context.Context, in dnsconfig.AuthZone) (*dnsconfig.AuthZone, error)
	GetAuthZone(ctx context.Context, id string) (*dnsconfig.AuthZone, error)
	ListAuthZones(ctx context.Context, opts ListOptions) ([]dnsconfig.AuthZone, error)
	UpdateAuthZone(ctx context.Context, id string, patch dnsconfig.AuthZone) (*dnsconfig.AuthZone, error)
	DeleteAuthZone(ctx context.Context, id string) error
}

// DNSData covers the dnsdata package: DNS resource records.
type DNSData interface {
	CreateRecord(ctx context.Context, in dnsdata.Record) (*dnsdata.Record, error)
	GetRecord(ctx context.Context, id string) (*dnsdata.Record, error)
	ListRecords(ctx context.Context, opts ListOptions) ([]dnsdata.Record, error)
	UpdateRecord(ctx context.Context, id string, patch dnsdata.Record) (*dnsdata.Record, error)
	DeleteRecord(ctx context.Context, id string) error
}

// IPAM covers the address-management half of the ipam package: IP spaces, address
// blocks, subnets, ranges and addresses.
type IPAM interface {
	CreateIPSpace(ctx context.Context, in ipam.IPSpace) (*ipam.IPSpace, error)
	GetIPSpace(ctx context.Context, id string) (*ipam.IPSpace, error)
	ListIPSpaces(ctx context.Context, opts ListOptions) ([]ipam.IPSpace, error)
	UpdateIPSpace(ctx context.Context, id string, patch ipam.IPSpace) (*ipam.IPSpace, error)
	DeleteIPSpace(ctx context.Context, id string) error

	CreateAddressBlock(ctx context.Context, in ipam.AddressBlock) (*ipam.AddressBlock, error)
	GetAddressBlock(ctx context.Context, id string) (*ipam.AddressBlock, error)
	ListAddressBlocks(ctx context.Context, opts ListOptions) ([]ipam.AddressBlock, error)
	UpdateAddressBlock(ctx context.Context, id string, patch ipam.AddressBlock) (*ipam.AddressBlock, error)
	DeleteAddressBlock(ctx context.Context, id string) error

	CreateSubnet(ctx context.Context, in ipam.Subnet) (*ipam.Subnet, error)
	GetSubnet(ctx context.Context, id string) (*ipam.Subnet, error)
	ListSubnets(ctx context.Context, opts ListOptions) ([]ipam.Subnet, error)
	UpdateSubnet(ctx context.Context, id string, patch ipam.Subnet) (*ipam.Subnet, error)
	DeleteSubnet(ctx context.Context, id string) error

	CreateRange(ctx context.Context, in ipam.Range) (*ipam.Range, error)
	GetRange(ctx context.Context, id string) (*ipam.Range, error)
	ListRanges(ctx context.Context, opts ListOptions) ([]ipam.Range, error)
	UpdateRange(ctx context.Context, id string, patch ipam.Range) (*ipam.Range, error)
	DeleteRange(ctx context.Context, id string) error

	// CreateAddress reserves an address. Setting in.Address to "<parent id>/nextavailableip"
	// allocates the lowest free address in that parent atomically with the tags, which is
	// how the operator does next-available allocation.
	CreateAddress(ctx context.Context, in ipam.Address) (*ipam.Address, error)
	GetAddress(ctx context.Context, id string) (*ipam.Address, error)
	ListAddresses(ctx context.Context, opts ListOptions) ([]ipam.Address, error)
	UpdateAddress(ctx context.Context, id string, patch ipam.Address) (*ipam.Address, error)
	DeleteAddress(ctx context.Context, id string) error
}

// DHCP covers the DHCP objects, which live in the ipam Go package even though their
// REST paths are under /dhcp/.
type DHCP interface {
	CreateFixedAddress(ctx context.Context, in ipam.FixedAddress) (*ipam.FixedAddress, error)
	GetFixedAddress(ctx context.Context, id string) (*ipam.FixedAddress, error)
	ListFixedAddresses(ctx context.Context, opts ListOptions) ([]ipam.FixedAddress, error)
	UpdateFixedAddress(ctx context.Context, id string, patch ipam.FixedAddress) (*ipam.FixedAddress, error)
	DeleteFixedAddress(ctx context.Context, id string) error
}

// Client groups the four domain interfaces. Both the SDK-backed client and the fake
// populate all four fields with the same value.
type Client struct {
	DNSConfig DNSConfig
	DNSData   DNSData
	IPAM      IPAM
	DHCP      DHCP
}

// Resource id prefixes. Universal DDI ids look like "<domain>/<type>/<uuid>" while the
// REST paths already carry "<domain>/<type>", so path parameters take the bare uuid.
const (
	PrefixView         = "dns/view"
	PrefixAuthZone     = "dns/auth_zone"
	PrefixRecord       = "dns/record"
	PrefixIPSpace      = "ipam/ip_space"
	PrefixAddressBlock = "ipam/address_block"
	PrefixSubnet       = "ipam/subnet"
	PrefixRange        = "ipam/range"
	PrefixAddress      = "ipam/address"
	PrefixFixedAddress = "dhcp/fixed_address"
)

// Next-available address forms. Setting the address field of a create body to a parent
// resource id plus one of these suffixes makes Universal DDI allocate inside that
// parent, atomically with the tags in the same request. These are the forms the
// Infoblox Terraform provider uses.
const (
	// NextAvailableIPSuffix allocates the next free address in a subnet, range or
	// address block (AddressAPI.Create).
	NextAvailableIPSuffix = "/nextavailableip"
	// NextAvailableSubnetSuffix allocates the next free subnet of the requested cidr in
	// an address block (SubnetAPI.Create).
	NextAvailableSubnetSuffix = "/nextavailablesubnet"
	// NextAvailableBlockSuffix allocates the next free address block of the requested
	// cidr in a parent address block (AddressBlockAPI.Create).
	NextAvailableBlockSuffix = "/nextavailableaddressblock"
)

// nextAvailableSuffixes lists every recognised next-available form.
var nextAvailableSuffixes = []string{
	NextAvailableIPSuffix,
	NextAvailableSubnetSuffix,
	NextAvailableBlockSuffix,
}

// BareID strips the "<domain>/<type>/" prefix from a Universal DDI resource id, which
// is what the generated path parameters expect. An id that is already bare is returned
// unchanged.
func BareID(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

// QualifyID prefixes a bare uuid with the given resource prefix. An id that already
// contains a slash is returned unchanged.
func QualifyID(prefix, id string) string {
	if id == "" || strings.Contains(id, "/") {
		return id
	}
	return prefix + "/" + id
}

// ParentOfNextAvailable splits a "<parent id>/nextavailable..." address into the parent
// resource id and the suffix. It returns ("", "", false) for a literal address.
func ParentOfNextAvailable(address string) (parent, suffix string, ok bool) {
	for _, s := range nextAvailableSuffixes {
		if strings.HasSuffix(address, s) {
			return strings.TrimSuffix(address, s), s, true
		}
	}
	return "", "", false
}

// IsNextAvailable reports whether address is a next-available expression of any form.
func IsNextAvailable(address string) bool {
	_, _, ok := ParentOfNextAvailable(address)
	return ok
}

// TagFilter builds a _tfilter expression in the form Universal DDI expects: both the
// key and the value single-quoted, clauses joined with " and ", for example
// `'uddi_operator_uid'=='1234' and 'uddi_operator_managed'=='true'`. Keys are sorted so
// the expression is stable. An empty map yields an empty string.
func TagFilter(tags map[string]string) string {
	if len(tags) == 0 {
		return ""
	}
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	clauses := make([]string, 0, len(keys))
	for _, k := range keys {
		clauses = append(clauses, fmt.Sprintf("'%s'=='%s'", quoteTag(k), quoteTag(tags[k])))
	}
	return strings.Join(clauses, " and ")
}

// quoteTag escapes the only character that can terminate a single-quoted literal.
func quoteTag(s string) string {
	return strings.ReplaceAll(s, "'", "\\'")
}

// ToAPITags converts a string map into the map[string]interface{} the SDK models use.
// It returns nil for an empty input so no "tags" key is emitted on a patch.
func ToAPITags(in map[string]string) map[string]interface{} {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// FromAPITags converts SDK tags back into a string map, dropping non-string values.
func FromAPITags(in map[string]interface{}) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

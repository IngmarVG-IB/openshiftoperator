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
	"errors"
	"fmt"
	"net/http"
	"time"

	sdk "github.com/infobloxopen/universal-ddi-go-client/client"
	"github.com/infobloxopen/universal-ddi-go-client/dnsconfig"
	"github.com/infobloxopen/universal-ddi-go-client/dnsdata"
	"github.com/infobloxopen/universal-ddi-go-client/ipam"
	"github.com/infobloxopen/universal-ddi-go-client/option"
)

// ErrNoAPIKey is returned when Config carries no API key.
var ErrNoAPIKey = errors.New("uddi: API key is empty")

// Config is everything NewSDKClient needs. APIKey is never logged or copied into an
// error.
type Config struct {
	// CSPURL is the Cloud Services Portal endpoint. Empty means the SDK default.
	CSPURL string
	// APIKey authenticates every request.
	APIKey string
	// ClientName is reported to the API and shows up in the audit log.
	ClientName string
	// ConnectionLabel is the "connection" label value stamped on every
	// uddi_operator_api_requests_total / uddi_operator_api_request_duration_seconds
	// sample this client produces (metrics.go). Empty by default, which keeps this a
	// purely additive field: callers such as internal/connection that never set it get
	// an empty "connection" label and nothing else changes.
	ConnectionLabel string
	// DefaultTags are merged by the SDK into every taggable request body.
	DefaultTags map[string]string
	// RateLimitRPS is the client-side sustained request rate. Zero disables limiting.
	RateLimitRPS float64
	// RateLimitBurst is the client-side token bucket size.
	RateLimitBurst int
	// Timeout bounds one API call end to end: the wait for a rate-limit token, every
	// attempt and the backoff between retries (see newHTTPClient). Zero means 30s.
	Timeout time.Duration
	// Debug turns on SDK request and response logging. Never enable it against a real
	// tenant: it writes the Authorization header.
	Debug bool
}

// sdkClient implements DNSConfig, DNSData, IPAM and DHCP over the aggregated SDK client.
type sdkClient struct {
	api *sdk.APIClient
	// alloc serialises next-available creates per parent resource id. The API can hand
	// the same address, subnet or block to two concurrent callers.
	alloc KeyedMutex
}

// lockAllocation serialises a create whose address field is a next-available
// expression. It returns a release function, which is a no-op for literal addresses.
func (c *sdkClient) lockAllocation(address string) func() {
	parent, _, ok := ParentOfNextAvailable(address)
	if !ok {
		return func() {}
	}
	c.alloc.Lock(parent)
	return func() { c.alloc.Unlock(parent) }
}

// NewSDKClient builds a Client backed by the real Universal DDI API.
//
// Note on options: v0.4.0 of universal-ddi-go-client exposes only WithCSPUrl,
// WithAPIKey, WithClientName, WithDefaultTags, WithHTTPClient and WithDebug. It has no
// WithRateLimit and no WithRetryDisabled because it carries neither a limiter nor a
// retry loop (both were bloxone-go-client features). Rate limiting and retrying are
// therefore applied through the custom transport chain built by newHTTPClient, and there
// is no SDK retry to disable.
func NewSDKClient(cfg Config) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, ErrNoAPIKey
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	httpClient := newHTTPClient(&http.Client{Timeout: timeout}, cfg.RateLimitRPS, cfg.RateLimitBurst, cfg.ConnectionLabel)

	opts := []option.ClientOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithHTTPClient(httpClient),
	}
	if cfg.CSPURL != "" {
		opts = append(opts, option.WithCSPUrl(cfg.CSPURL))
	}
	if cfg.ClientName != "" {
		opts = append(opts, option.WithClientName(cfg.ClientName))
	}
	if len(cfg.DefaultTags) > 0 {
		opts = append(opts, option.WithDefaultTags(cfg.DefaultTags))
	}
	if cfg.Debug {
		opts = append(opts, option.WithDebug(true))
	}

	c := &sdkClient{api: sdk.NewAPIClient(opts...)}
	return &Client{DNSConfig: c, DNSData: c, IPAM: c, DHCP: c, OptionCodes: NewOptionCodeResolver(c)}, nil
}

// --- listing and pagination ---

const (
	// listPageSize is the _limit of every page fetched when ListOptions.Limit is zero.
	listPageSize = 1000
	// listMaxPages caps how many pages one List call fetches, 100,000 objects in all. A
	// cursor that never ends, or a tenant far larger than the operator expects, ends in
	// an error instead of an endless loop.
	listMaxPages = 100
)

// ErrTooManyPages is returned, wrapped, by a List call that still had a full page left
// after listMaxPages pages. Narrow the filter or ask for one page with a Limit.
var ErrTooManyPages = errors.New("uddi: list result set too large")

// Pagination in this SDK is by offset, not by page token. Every generated list request
// has a PageToken parameter (sent as _page_token), but none of the nine list responses
// the operator uses (dnsconfig.ListViewResponse, ListAuthZoneResponse,
// dnsdata.ListRecordResponse, ipam.ListIPSpaceResponse, ListAddressBlockResponse,
// ListSubnetResponse, ListRangeResponse, ListAddressResponse and ListFixedAddressResponse)
// has a field to carry a token back: each holds only "results". The ApiPageInfo type with
// page_token exists only in the inframgmt, infraprovision and clouddiscovery packages,
// which the operator does not use. So listAll walks _offset and _limit pages of
// listPageSize and stops at the first short page.

// listRequest is what the nine generated list request builders have in common. Each
// method returns the builder's own type, so R is that type.
type listRequest[R any] interface {
	Filter(string) R
	Tfilter(string) R
	Fields(string) R
	OrderBy(string) R
	Limit(int32) R
	Offset(int32) R
}

// applyListOptions copies the set fields of o onto a generated list request.
func applyListOptions[R listRequest[R]](req R, o ListOptions) R {
	if o.Filter != "" {
		req = req.Filter(o.Filter)
	}
	if o.TFilter != "" {
		req = req.Tfilter(o.TFilter)
	}
	if o.Fields != "" {
		req = req.Fields(o.Fields)
	}
	if o.OrderBy != "" {
		req = req.OrderBy(o.OrderBy)
	}
	if o.Limit > 0 {
		req = req.Limit(o.Limit)
	}
	if o.Offset > 0 {
		req = req.Offset(o.Offset)
	}
	return req
}

// listAll runs fetch, which issues one list request with the options it is given, and
// returns every result.
//
// With a non-zero opts.Limit the caller asked for one page, so fetch runs once with the
// options as given. With a zero Limit it runs once per page of listPageSize results,
// starting at opts.Offset, until a page comes back short; method names the call in the
// error returned when listMaxPages full pages in a row leave the end still unseen. An
// error on any page fails the whole call and no partial result is returned.
//
// A server that capped _limit below listPageSize would make every page look short and
// truncate the list at its first page. Universal DDI documents only that the service
// "may impose a maximum", so this has to be confirmed against a tenant.
func listAll[T any](method string, opts ListOptions, fetch func(ListOptions) ([]T, error)) ([]T, error) {
	if opts.Limit > 0 {
		return fetch(opts)
	}
	var all []T
	page := opts
	page.Limit = listPageSize
	longest := 0
	for range listMaxPages {
		res, err := fetch(page)
		if err != nil {
			return nil, err
		}
		all = append(all, res...)
		// The last page is the first one shorter than the longest page seen so far, or an
		// empty one. Comparing with the requested size instead would silently truncate
		// the walk after one page if a tenant caps _limit below listPageSize, which the
		// API documents as possible. A short first page costs one extra, empty request.
		if len(res) == 0 || len(res) < longest {
			return all, nil
		}
		longest = len(res)
		page.Offset += int32(len(res))
	}
	return nil, fmt.Errorf("%w: %s returned more than %d pages of %d results; narrow the filter or set a limit",
		ErrTooManyPages, method, listMaxPages, listPageSize)
}

// --- dnsconfig: View ---

func (c *sdkClient) CreateView(ctx context.Context, in dnsconfig.View) (*dnsconfig.View, error) {
	res, resp, err := c.api.DNSConfigurationAPI.ViewAPI.Create(ctx).Body(in).Execute()
	if err != nil {
		return nil, wrap(DomainDNSConfig, "CreateView", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) GetView(ctx context.Context, id string) (*dnsconfig.View, error) {
	res, resp, err := c.api.DNSConfigurationAPI.ViewAPI.Read(ctx, BareID(id)).Execute()
	if err != nil {
		return nil, wrap(DomainDNSConfig, "GetView", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) ListViews(ctx context.Context, opts ListOptions) ([]dnsconfig.View, error) {
	return listAll("ListViews", opts, func(page ListOptions) ([]dnsconfig.View, error) {
		res, resp, err := applyListOptions(c.api.DNSConfigurationAPI.ViewAPI.List(ctx), page).Execute()
		if err != nil {
			return nil, wrap(DomainDNSConfig, "ListViews", resp, err)
		}
		return res.GetResults(), nil
	})
}

func (c *sdkClient) UpdateView(ctx context.Context, id string, patch dnsconfig.View) (*dnsconfig.View, error) {
	res, resp, err := c.api.DNSConfigurationAPI.ViewAPI.Update(ctx, BareID(id)).Body(patch).Execute()
	if err != nil {
		return nil, wrap(DomainDNSConfig, "UpdateView", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) DeleteView(ctx context.Context, id string) error {
	resp, err := c.api.DNSConfigurationAPI.ViewAPI.Delete(ctx, BareID(id)).Execute()
	return wrap(DomainDNSConfig, "DeleteView", resp, err)
}

// --- dnsconfig: AuthZone ---

func (c *sdkClient) CreateAuthZone(ctx context.Context, in dnsconfig.AuthZone) (*dnsconfig.AuthZone, error) {
	res, resp, err := c.api.DNSConfigurationAPI.AuthZoneAPI.Create(ctx).Body(in).Execute()
	if err != nil {
		return nil, wrap(DomainDNSConfig, "CreateAuthZone", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) GetAuthZone(ctx context.Context, id string) (*dnsconfig.AuthZone, error) {
	res, resp, err := c.api.DNSConfigurationAPI.AuthZoneAPI.Read(ctx, BareID(id)).Execute()
	if err != nil {
		return nil, wrap(DomainDNSConfig, "GetAuthZone", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) ListAuthZones(ctx context.Context, opts ListOptions) ([]dnsconfig.AuthZone, error) {
	return listAll("ListAuthZones", opts, func(page ListOptions) ([]dnsconfig.AuthZone, error) {
		res, resp, err := applyListOptions(c.api.DNSConfigurationAPI.AuthZoneAPI.List(ctx), page).Execute()
		if err != nil {
			return nil, wrap(DomainDNSConfig, "ListAuthZones", resp, err)
		}
		return res.GetResults(), nil
	})
}

func (c *sdkClient) UpdateAuthZone(ctx context.Context, id string, patch dnsconfig.AuthZone) (*dnsconfig.AuthZone, error) {
	res, resp, err := c.api.DNSConfigurationAPI.AuthZoneAPI.Update(ctx, BareID(id)).Body(patch).Execute()
	if err != nil {
		return nil, wrap(DomainDNSConfig, "UpdateAuthZone", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) DeleteAuthZone(ctx context.Context, id string) error {
	resp, err := c.api.DNSConfigurationAPI.AuthZoneAPI.Delete(ctx, BareID(id)).Execute()
	return wrap(DomainDNSConfig, "DeleteAuthZone", resp, err)
}

// --- dnsdata: Record ---

func (c *sdkClient) CreateRecord(ctx context.Context, in dnsdata.Record) (*dnsdata.Record, error) {
	res, resp, err := c.api.DNSDataAPI.RecordAPI.Create(ctx).Body(in).Execute()
	if err != nil {
		return nil, wrap(DomainDNSData, "CreateRecord", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) GetRecord(ctx context.Context, id string) (*dnsdata.Record, error) {
	res, resp, err := c.api.DNSDataAPI.RecordAPI.Read(ctx, BareID(id)).Execute()
	if err != nil {
		return nil, wrap(DomainDNSData, "GetRecord", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) ListRecords(ctx context.Context, opts ListOptions) ([]dnsdata.Record, error) {
	return listAll("ListRecords", opts, func(page ListOptions) ([]dnsdata.Record, error) {
		res, resp, err := applyListOptions(c.api.DNSDataAPI.RecordAPI.List(ctx), page).Execute()
		if err != nil {
			return nil, wrap(DomainDNSData, "ListRecords", resp, err)
		}
		return res.GetResults(), nil
	})
}

func (c *sdkClient) UpdateRecord(ctx context.Context, id string, patch dnsdata.Record) (*dnsdata.Record, error) {
	res, resp, err := c.api.DNSDataAPI.RecordAPI.Update(ctx, BareID(id)).Body(patch).Execute()
	if err != nil {
		return nil, wrap(DomainDNSData, "UpdateRecord", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) DeleteRecord(ctx context.Context, id string) error {
	resp, err := c.api.DNSDataAPI.RecordAPI.Delete(ctx, BareID(id)).Execute()
	return wrap(DomainDNSData, "DeleteRecord", resp, err)
}

// --- ipam: IPSpace ---

func (c *sdkClient) CreateIPSpace(ctx context.Context, in ipam.IPSpace) (*ipam.IPSpace, error) {
	res, resp, err := c.api.IPAddressManagementAPI.IpSpaceAPI.Create(ctx).Body(in).Execute()
	if err != nil {
		return nil, wrap(DomainIPAM, "CreateIPSpace", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) GetIPSpace(ctx context.Context, id string) (*ipam.IPSpace, error) {
	res, resp, err := c.api.IPAddressManagementAPI.IpSpaceAPI.Read(ctx, BareID(id)).Execute()
	if err != nil {
		return nil, wrap(DomainIPAM, "GetIPSpace", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) ListIPSpaces(ctx context.Context, opts ListOptions) ([]ipam.IPSpace, error) {
	return listAll("ListIPSpaces", opts, func(page ListOptions) ([]ipam.IPSpace, error) {
		res, resp, err := applyListOptions(c.api.IPAddressManagementAPI.IpSpaceAPI.List(ctx), page).Execute()
		if err != nil {
			return nil, wrap(DomainIPAM, "ListIPSpaces", resp, err)
		}
		return res.GetResults(), nil
	})
}

func (c *sdkClient) UpdateIPSpace(ctx context.Context, id string, patch ipam.IPSpace) (*ipam.IPSpace, error) {
	res, resp, err := c.api.IPAddressManagementAPI.IpSpaceAPI.Update(ctx, BareID(id)).Body(patch).Execute()
	if err != nil {
		return nil, wrap(DomainIPAM, "UpdateIPSpace", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) DeleteIPSpace(ctx context.Context, id string) error {
	resp, err := c.api.IPAddressManagementAPI.IpSpaceAPI.Delete(ctx, BareID(id)).Execute()
	return wrap(DomainIPAM, "DeleteIPSpace", resp, err)
}

// --- ipam: AddressBlock ---

func (c *sdkClient) CreateAddressBlock(ctx context.Context, in ipam.AddressBlock) (*ipam.AddressBlock, error) {
	defer c.lockAllocation(strPtrValue(in.Address))()
	res, resp, err := c.api.IPAddressManagementAPI.AddressBlockAPI.Create(ctx).Body(in).Execute()
	if err != nil {
		return nil, wrap(DomainIPAM, "CreateAddressBlock", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) GetAddressBlock(ctx context.Context, id string) (*ipam.AddressBlock, error) {
	res, resp, err := c.api.IPAddressManagementAPI.AddressBlockAPI.Read(ctx, BareID(id)).Execute()
	if err != nil {
		return nil, wrap(DomainIPAM, "GetAddressBlock", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) ListAddressBlocks(ctx context.Context, opts ListOptions) ([]ipam.AddressBlock, error) {
	return listAll("ListAddressBlocks", opts, func(page ListOptions) ([]ipam.AddressBlock, error) {
		res, resp, err := applyListOptions(c.api.IPAddressManagementAPI.AddressBlockAPI.List(ctx), page).Execute()
		if err != nil {
			return nil, wrap(DomainIPAM, "ListAddressBlocks", resp, err)
		}
		return res.GetResults(), nil
	})
}

func (c *sdkClient) UpdateAddressBlock(ctx context.Context, id string, patch ipam.AddressBlock) (*ipam.AddressBlock, error) {
	res, resp, err := c.api.IPAddressManagementAPI.AddressBlockAPI.Update(ctx, BareID(id)).Body(patch).Execute()
	if err != nil {
		return nil, wrap(DomainIPAM, "UpdateAddressBlock", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) DeleteAddressBlock(ctx context.Context, id string) error {
	resp, err := c.api.IPAddressManagementAPI.AddressBlockAPI.Delete(ctx, BareID(id)).Execute()
	return wrap(DomainIPAM, "DeleteAddressBlock", resp, err)
}

// --- ipam: Subnet ---

func (c *sdkClient) CreateSubnet(ctx context.Context, in ipam.Subnet) (*ipam.Subnet, error) {
	defer c.lockAllocation(strPtrValue(in.Address))()
	res, resp, err := c.api.IPAddressManagementAPI.SubnetAPI.Create(ctx).Body(in).Execute()
	if err != nil {
		return nil, wrap(DomainIPAM, "CreateSubnet", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) GetSubnet(ctx context.Context, id string) (*ipam.Subnet, error) {
	res, resp, err := c.api.IPAddressManagementAPI.SubnetAPI.Read(ctx, BareID(id)).Execute()
	if err != nil {
		return nil, wrap(DomainIPAM, "GetSubnet", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) ListSubnets(ctx context.Context, opts ListOptions) ([]ipam.Subnet, error) {
	return listAll("ListSubnets", opts, func(page ListOptions) ([]ipam.Subnet, error) {
		res, resp, err := applyListOptions(c.api.IPAddressManagementAPI.SubnetAPI.List(ctx), page).Execute()
		if err != nil {
			return nil, wrap(DomainIPAM, "ListSubnets", resp, err)
		}
		return res.GetResults(), nil
	})
}

func (c *sdkClient) UpdateSubnet(ctx context.Context, id string, patch ipam.Subnet) (*ipam.Subnet, error) {
	res, resp, err := c.api.IPAddressManagementAPI.SubnetAPI.Update(ctx, BareID(id)).Body(patch).Execute()
	if err != nil {
		return nil, wrap(DomainIPAM, "UpdateSubnet", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) DeleteSubnet(ctx context.Context, id string) error {
	resp, err := c.api.IPAddressManagementAPI.SubnetAPI.Delete(ctx, BareID(id)).Execute()
	return wrap(DomainIPAM, "DeleteSubnet", resp, err)
}

// --- ipam: Range ---

func (c *sdkClient) CreateRange(ctx context.Context, in ipam.Range) (*ipam.Range, error) {
	res, resp, err := c.api.IPAddressManagementAPI.RangeAPI.Create(ctx).Body(in).Execute()
	if err != nil {
		return nil, wrap(DomainIPAM, "CreateRange", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) GetRange(ctx context.Context, id string) (*ipam.Range, error) {
	res, resp, err := c.api.IPAddressManagementAPI.RangeAPI.Read(ctx, BareID(id)).Execute()
	if err != nil {
		return nil, wrap(DomainIPAM, "GetRange", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) ListRanges(ctx context.Context, opts ListOptions) ([]ipam.Range, error) {
	return listAll("ListRanges", opts, func(page ListOptions) ([]ipam.Range, error) {
		res, resp, err := applyListOptions(c.api.IPAddressManagementAPI.RangeAPI.List(ctx), page).Execute()
		if err != nil {
			return nil, wrap(DomainIPAM, "ListRanges", resp, err)
		}
		return res.GetResults(), nil
	})
}

func (c *sdkClient) UpdateRange(ctx context.Context, id string, patch ipam.Range) (*ipam.Range, error) {
	res, resp, err := c.api.IPAddressManagementAPI.RangeAPI.Update(ctx, BareID(id)).Body(patch).Execute()
	if err != nil {
		return nil, wrap(DomainIPAM, "UpdateRange", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) DeleteRange(ctx context.Context, id string) error {
	resp, err := c.api.IPAddressManagementAPI.RangeAPI.Delete(ctx, BareID(id)).Execute()
	return wrap(DomainIPAM, "DeleteRange", resp, err)
}

// --- ipam: Address ---

func (c *sdkClient) CreateAddress(ctx context.Context, in ipam.Address) (*ipam.Address, error) {
	defer c.lockAllocation(in.Address)()
	res, resp, err := c.api.IPAddressManagementAPI.AddressAPI.Create(ctx).Body(in).Execute()
	if err != nil {
		return nil, wrap(DomainIPAM, "CreateAddress", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) GetAddress(ctx context.Context, id string) (*ipam.Address, error) {
	res, resp, err := c.api.IPAddressManagementAPI.AddressAPI.Read(ctx, BareID(id)).Execute()
	if err != nil {
		return nil, wrap(DomainIPAM, "GetAddress", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) ListAddresses(ctx context.Context, opts ListOptions) ([]ipam.Address, error) {
	return listAll("ListAddresses", opts, func(page ListOptions) ([]ipam.Address, error) {
		res, resp, err := applyListOptions(c.api.IPAddressManagementAPI.AddressAPI.List(ctx), page).Execute()
		if err != nil {
			return nil, wrap(DomainIPAM, "ListAddresses", resp, err)
		}
		return res.GetResults(), nil
	})
}

func (c *sdkClient) UpdateAddress(ctx context.Context, id string, patch ipam.Address) (*ipam.Address, error) {
	res, resp, err := c.api.IPAddressManagementAPI.AddressAPI.Update(ctx, BareID(id)).Body(patch).Execute()
	if err != nil {
		return nil, wrap(DomainIPAM, "UpdateAddress", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) DeleteAddress(ctx context.Context, id string) error {
	resp, err := c.api.IPAddressManagementAPI.AddressAPI.Delete(ctx, BareID(id)).Execute()
	return wrap(DomainIPAM, "DeleteAddress", resp, err)
}

// --- dhcp: FixedAddress ---

func (c *sdkClient) CreateFixedAddress(ctx context.Context, in ipam.FixedAddress) (*ipam.FixedAddress, error) {
	res, resp, err := c.api.IPAddressManagementAPI.FixedAddressAPI.Create(ctx).Body(in).Execute()
	if err != nil {
		return nil, wrap(DomainDHCP, "CreateFixedAddress", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) GetFixedAddress(ctx context.Context, id string) (*ipam.FixedAddress, error) {
	res, resp, err := c.api.IPAddressManagementAPI.FixedAddressAPI.Read(ctx, BareID(id)).Execute()
	if err != nil {
		return nil, wrap(DomainDHCP, "GetFixedAddress", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) ListFixedAddresses(ctx context.Context, opts ListOptions) ([]ipam.FixedAddress, error) {
	return listAll("ListFixedAddresses", opts, func(page ListOptions) ([]ipam.FixedAddress, error) {
		res, resp, err := applyListOptions(c.api.IPAddressManagementAPI.FixedAddressAPI.List(ctx), page).Execute()
		if err != nil {
			return nil, wrap(DomainDHCP, "ListFixedAddresses", resp, err)
		}
		return res.GetResults(), nil
	})
}

func (c *sdkClient) UpdateFixedAddress(ctx context.Context, id string, patch ipam.FixedAddress) (*ipam.FixedAddress, error) {
	res, resp, err := c.api.IPAddressManagementAPI.FixedAddressAPI.Update(ctx, BareID(id)).Body(patch).Execute()
	if err != nil {
		return nil, wrap(DomainDHCP, "UpdateFixedAddress", resp, err)
	}
	out := res.GetResult()
	return &out, nil
}

func (c *sdkClient) DeleteFixedAddress(ctx context.Context, id string) error {
	resp, err := c.api.IPAddressManagementAPI.FixedAddressAPI.Delete(ctx, BareID(id)).Execute()
	return wrap(DomainDHCP, "DeleteFixedAddress", resp, err)
}

// --- dhcp: OptionCode ---

// ListOptionCodes lists DHCP option codes. The generated list request has no _tfilter,
// because option codes carry no tags, so opts.TFilter is ignored.
func (c *sdkClient) ListOptionCodes(ctx context.Context, opts ListOptions) ([]ipam.OptionCode, error) {
	return listAll("ListOptionCodes", opts, func(page ListOptions) ([]ipam.OptionCode, error) {
		req := c.api.IPAddressManagementAPI.OptionCodeAPI.List(ctx)
		if page.Filter != "" {
			req = req.Filter(page.Filter)
		}
		if page.Fields != "" {
			req = req.Fields(page.Fields)
		}
		if page.OrderBy != "" {
			req = req.OrderBy(page.OrderBy)
		}
		if page.Limit > 0 {
			req = req.Limit(page.Limit)
		}
		if page.Offset > 0 {
			req = req.Offset(page.Offset)
		}
		res, resp, err := req.Execute()
		if err != nil {
			return nil, wrap(DomainDHCP, "ListOptionCodes", resp, err)
		}
		return res.GetResults(), nil
	})
}

func strPtrValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Compile-time assertions that the SDK client satisfies every domain interface.
var (
	_ DNSConfig = (*sdkClient)(nil)
	_ DNSData   = (*sdkClient)(nil)
	_ IPAM      = (*sdkClient)(nil)
	_ DHCP      = (*sdkClient)(nil)
)

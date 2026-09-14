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
	// DefaultTags are merged by the SDK into every taggable request body.
	DefaultTags map[string]string
	// RateLimitRPS is the client-side sustained request rate. Zero disables limiting.
	RateLimitRPS float64
	// RateLimitBurst is the client-side token bucket size.
	RateLimitBurst int
	// Timeout bounds a single HTTP request. Zero means 30s.
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
// retry loop (both were bloxone-go-client features). Rate limiting is therefore applied
// through a custom transport, and there is no SDK retry to disable.
func NewSDKClient(cfg Config) (*Client, error) {
	if cfg.APIKey == "" {
		return nil, ErrNoAPIKey
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	httpClient := newRateLimitedClient(&http.Client{Timeout: timeout}, cfg.RateLimitRPS, cfg.RateLimitBurst)

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
	return &Client{DNSConfig: c, DNSData: c, IPAM: c, DHCP: c}, nil
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
	req := c.api.DNSConfigurationAPI.ViewAPI.List(ctx)
	if opts.Filter != "" {
		req = req.Filter(opts.Filter)
	}
	if opts.TFilter != "" {
		req = req.Tfilter(opts.TFilter)
	}
	if opts.Fields != "" {
		req = req.Fields(opts.Fields)
	}
	if opts.OrderBy != "" {
		req = req.OrderBy(opts.OrderBy)
	}
	if opts.Limit > 0 {
		req = req.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		req = req.Offset(opts.Offset)
	}
	res, resp, err := req.Execute()
	if err != nil {
		return nil, wrap(DomainDNSConfig, "ListViews", resp, err)
	}
	return res.GetResults(), nil
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
	req := c.api.DNSConfigurationAPI.AuthZoneAPI.List(ctx)
	if opts.Filter != "" {
		req = req.Filter(opts.Filter)
	}
	if opts.TFilter != "" {
		req = req.Tfilter(opts.TFilter)
	}
	if opts.Fields != "" {
		req = req.Fields(opts.Fields)
	}
	if opts.OrderBy != "" {
		req = req.OrderBy(opts.OrderBy)
	}
	if opts.Limit > 0 {
		req = req.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		req = req.Offset(opts.Offset)
	}
	res, resp, err := req.Execute()
	if err != nil {
		return nil, wrap(DomainDNSConfig, "ListAuthZones", resp, err)
	}
	return res.GetResults(), nil
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
	req := c.api.DNSDataAPI.RecordAPI.List(ctx)
	if opts.Filter != "" {
		req = req.Filter(opts.Filter)
	}
	if opts.TFilter != "" {
		req = req.Tfilter(opts.TFilter)
	}
	if opts.Fields != "" {
		req = req.Fields(opts.Fields)
	}
	if opts.OrderBy != "" {
		req = req.OrderBy(opts.OrderBy)
	}
	if opts.Limit > 0 {
		req = req.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		req = req.Offset(opts.Offset)
	}
	res, resp, err := req.Execute()
	if err != nil {
		return nil, wrap(DomainDNSData, "ListRecords", resp, err)
	}
	return res.GetResults(), nil
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
	req := c.api.IPAddressManagementAPI.IpSpaceAPI.List(ctx)
	if opts.Filter != "" {
		req = req.Filter(opts.Filter)
	}
	if opts.TFilter != "" {
		req = req.Tfilter(opts.TFilter)
	}
	if opts.Fields != "" {
		req = req.Fields(opts.Fields)
	}
	if opts.OrderBy != "" {
		req = req.OrderBy(opts.OrderBy)
	}
	if opts.Limit > 0 {
		req = req.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		req = req.Offset(opts.Offset)
	}
	res, resp, err := req.Execute()
	if err != nil {
		return nil, wrap(DomainIPAM, "ListIPSpaces", resp, err)
	}
	return res.GetResults(), nil
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
	req := c.api.IPAddressManagementAPI.AddressBlockAPI.List(ctx)
	if opts.Filter != "" {
		req = req.Filter(opts.Filter)
	}
	if opts.TFilter != "" {
		req = req.Tfilter(opts.TFilter)
	}
	if opts.Fields != "" {
		req = req.Fields(opts.Fields)
	}
	if opts.OrderBy != "" {
		req = req.OrderBy(opts.OrderBy)
	}
	if opts.Limit > 0 {
		req = req.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		req = req.Offset(opts.Offset)
	}
	res, resp, err := req.Execute()
	if err != nil {
		return nil, wrap(DomainIPAM, "ListAddressBlocks", resp, err)
	}
	return res.GetResults(), nil
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
	req := c.api.IPAddressManagementAPI.SubnetAPI.List(ctx)
	if opts.Filter != "" {
		req = req.Filter(opts.Filter)
	}
	if opts.TFilter != "" {
		req = req.Tfilter(opts.TFilter)
	}
	if opts.Fields != "" {
		req = req.Fields(opts.Fields)
	}
	if opts.OrderBy != "" {
		req = req.OrderBy(opts.OrderBy)
	}
	if opts.Limit > 0 {
		req = req.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		req = req.Offset(opts.Offset)
	}
	res, resp, err := req.Execute()
	if err != nil {
		return nil, wrap(DomainIPAM, "ListSubnets", resp, err)
	}
	return res.GetResults(), nil
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
	req := c.api.IPAddressManagementAPI.RangeAPI.List(ctx)
	if opts.Filter != "" {
		req = req.Filter(opts.Filter)
	}
	if opts.TFilter != "" {
		req = req.Tfilter(opts.TFilter)
	}
	if opts.Fields != "" {
		req = req.Fields(opts.Fields)
	}
	if opts.OrderBy != "" {
		req = req.OrderBy(opts.OrderBy)
	}
	if opts.Limit > 0 {
		req = req.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		req = req.Offset(opts.Offset)
	}
	res, resp, err := req.Execute()
	if err != nil {
		return nil, wrap(DomainIPAM, "ListRanges", resp, err)
	}
	return res.GetResults(), nil
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
	req := c.api.IPAddressManagementAPI.AddressAPI.List(ctx)
	if opts.Filter != "" {
		req = req.Filter(opts.Filter)
	}
	if opts.TFilter != "" {
		req = req.Tfilter(opts.TFilter)
	}
	if opts.Fields != "" {
		req = req.Fields(opts.Fields)
	}
	if opts.OrderBy != "" {
		req = req.OrderBy(opts.OrderBy)
	}
	if opts.Limit > 0 {
		req = req.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		req = req.Offset(opts.Offset)
	}
	res, resp, err := req.Execute()
	if err != nil {
		return nil, wrap(DomainIPAM, "ListAddresses", resp, err)
	}
	return res.GetResults(), nil
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
	req := c.api.IPAddressManagementAPI.FixedAddressAPI.List(ctx)
	if opts.Filter != "" {
		req = req.Filter(opts.Filter)
	}
	if opts.TFilter != "" {
		req = req.Tfilter(opts.TFilter)
	}
	if opts.Fields != "" {
		req = req.Fields(opts.Fields)
	}
	if opts.OrderBy != "" {
		req = req.OrderBy(opts.OrderBy)
	}
	if opts.Limit > 0 {
		req = req.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		req = req.Offset(opts.Offset)
	}
	res, resp, err := req.Execute()
	if err != nil {
		return nil, wrap(DomainDHCP, "ListFixedAddresses", resp, err)
	}
	return res.GetResults(), nil
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

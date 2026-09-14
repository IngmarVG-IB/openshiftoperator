# Universal DDI API and Go SDK notes

Working notes for implementers, condensed from research on 2026-09-14 against the SDK
sources. Anything marked **verify** must be confirmed against a real tenant in the
integration tests before the behaviour is relied on.

## Module

- Use `github.com/infobloxopen/universal-ddi-go-client`, latest tag v0.4.0 (2026-07-15),
  minimum Go 1.23, Apache 2.0. Generated with OpenAPI Generator; hand-written code lives in
  `internal/` (client, rate limiter, retry) and `option/`.
- `github.com/infobloxopen/bloxone-go-client` is deprecated and must not be used. The API
  surface is identical, only the import path changed.
- Docs: the per-package `docs/*.md` files in the repo mirror the login-gated
  https://csp.infoblox.com/apidoc and are the practical reference.

## Client construction

```go
import (
    uddi "github.com/infobloxopen/universal-ddi-go-client/client"
    "github.com/infobloxopen/universal-ddi-go-client/option"
)

c := uddi.NewAPIClient(
    option.WithCSPUrl("https://csp.infoblox.com"), // default
    option.WithAPIKey(key),
    option.WithClientName("uddi-operator"),
    option.WithDefaultTags(map[string]string{...}), // merged into every taggable body
    option.WithRateLimit(10, 20),                   // default 25 rps, burst 25, mutating verbs only
    option.WithRetryDisabled(),                     // see "Retries" below
    option.WithHTTPClient(httpClient),
)
c.DNSConfigurationAPI    // *dnsconfig.APIClient
c.DNSDataAPI             // *dnsdata.APIClient
c.IPAddressManagementAPI // *ipam.APIClient (IPAM and DHCP)
```

Headers on every request: `Authorization: Token <key>`, `x-infoblox-client: <clientName>`,
`x-infoblox-sdk: golang-sdk`, plus a `User-Agent` that always reports SDK version 0.1
regardless of the tag.

Environment variables honoured by the SDK (useful for integration tests):
`INFOBLOX_PORTAL_URL`, `INFOBLOX_PORTAL_KEY`, `INFOBLOX_RATE_LIMIT`,
`INFOBLOX_RATE_LIMIT_BURST`, `INFOBLOX_MAX_RETRIES`, `INFOBLOX_RETRY_MIN_WAIT`,
`INFOBLOX_RETRY_MAX_WAIT`.

## Packages

| Package | Covers |
| --- | --- |
| `dnsconfig` | View, AuthZone, ForwardZone, ACL, AuthNSG, Delegation, DNS Server, DNS Host, LBDN |
| `dnsdata` | Record (all RR types), SOA serial increment |
| `ipam` | IPSpace, AddressBlock, Subnet, Range, Address, IpamHost, **and all DHCP objects**: FixedAddress, DHCP Host, HA Group, option code / space / group / filter |
| `keys`, `inframgmt`, `infraprovision`, `dtc`, `anycast`, `dfp`, ... | not needed for the operator |

There is no `dhcp` Go package. DHCP objects are in `ipam` even though their REST paths are
under `/dhcp/...`.

All REST paths are relative to `https://csp.infoblox.com/api/ddi/v1`.

## Object reference

Every object: `Id *string` (read-only, `<domain>/<type>/<uuid>`), `Tags map[string]interface{}`,
`Comment *string`, `CreatedAt` / `UpdatedAt` read-only. Update is always **PATCH**; the
generated client serialises only fields that were explicitly set, so a partial struct is a
partial patch. Delete returns no body.

| Kind | Go type | API service | REST path | Required on create | Read-only after create |
| --- | --- | --- | --- | --- | --- |
| View | `dnsconfig.View` | `ViewAPI` | `/dns/view` | `name` | |
| AuthZone | `dnsconfig.AuthZone` | `AuthZoneAPI` | `/dns/auth_zone` | `fqdn`, `primary_type`, `view` | `fqdn`, `primary_type` |
| Record | `dnsdata.Record` | `RecordAPI` | `/dns/record` | `type`, `rdata`, and (`zone` + `name_in_zone`) or `absolute_name_spec` | `type`, `zone`, `name_in_zone` |
| IPSpace | `ipam.IPSpace` | `IpSpaceAPI` | `/ipam/ip_space` | `name` | |
| AddressBlock | `ipam.AddressBlock` | `AddressBlockAPI` | `/ipam/address_block` | `address`, `cidr`, `space` | `address`, `cidr` |
| Subnet | `ipam.Subnet` | `SubnetAPI` | `/ipam/subnet` | `address`, `cidr`, `space` | `address`, `cidr` |
| Range | `ipam.Range` | `RangeAPI` | `/ipam/range` | `start`, `end`, `space` | |
| Address | `ipam.Address` | `AddressAPI` | `/ipam/address` | `address`, `space` | `address` |
| FixedAddress | `ipam.FixedAddress` | `FixedAddressAPI` | `/dhcp/fixed_address` | `address`, `match_type`, `match_value`, `ip_space` | |
| DHCP Host | `ipam.Host` | `DhcpHostAPI` | `/dhcp/host` | read, list, update only; hosts are registered out of band | |

Constructors with required args: `dnsconfig.NewView(name)`, `dnsdata.NewRecord(rdata)`,
`ipam.NewIPSpace(name)`, `ipam.NewRange(end, start)` (note the order),
`ipam.NewAddress(address)`, `ipam.NewFixedAddress(address, matchType, matchValue)`.

### View

- `ip_spaces []string`: IP spaces bound to the view (for reverse lookups). Worth exposing.
- Delete moves the view to the Recycle Bin rather than hard-deleting (**verify** whether
  this also applies to zones and records, and whether recycled objects show up in List
  results; the natural-key lookup must ignore them).

### AuthZone

- `primary_type`: `cloud` (served by Universal DDI DNS hosts) or `external`.
- `view`: `dns/view/<uuid>`.
- Nameserver assignment is mid-migration in the API. Legacy fields `nsg`, `nsgs` and
  `internal_secondaries` / `external_primaries` / `external_secondaries` still exist, but
  combining an NSG with internal secondaries and multiple NSGs are now rejected. The new
  "unified nameservers" model uses `nameservers []Nameserver` with `version: v2`. The CRD
  should expose the unified model as primary and keep the legacy fields for adoption of old
  zones (**verify** the `Nameserver` struct fields in `dnsconfig/docs/Nameserver.md`).
- `warnings []Warning` (read-only) after create / update: surface in status.
- `mapped_subnet` / `mapping` are read-only and only set on reverse zones.

### Record

- Zone reference: set `zone` (`dns/auth_zone/<uuid>`) + `name_in_zone` (label relative to
  the zone, empty string for the apex), or set the synthetic `absolute_name_spec` and let
  the server derive both. `absolute_zone_name`, `dns_*` fields are read-only mirrors.
- `rdata` per type (`map[string]interface{}`):

  | type | rdata | required |
  | --- | --- | --- |
  | A / AAAA | `{"address": ip}` | address |
  | CNAME | `{"cname": fqdn}` | cname |
  | TXT | `{"text": s}` | |
  | PTR | `{"dname": fqdn}` | dname |
  | SRV | `{"priority", "weight", "port", "target"}` | priority, port, target |
  | MX | `{"preference", "exchange"}` | both |
  | NS | `{"dname": fqdn}` | dname |
  | CAA | `{"flags", "tag", "value"}` | tag, value |

- `options` (request-only): for A / AAAA `create_ptr` and `check_rmz` to auto-create the
  matching PTR; for PTR, `address` plus `view` to create by IP.
- `source []string` (read-only): `STATIC`, `SYSTEM`, `DYNAMIC`, `DELEGATED`, `DTC`. Never
  adopt or delete non-STATIC records.
- `ttl`, `disabled`, `comment`, `tags`.

### IPAM objects

- `space`: `ipam/ip_space/<uuid>` on AddressBlock, Subnet, Range, Address; `ip_space` on
  FixedAddress.
- Subnet and AddressBlock take `address` plus `cidr` (int64) separately.
- Range: `start`, `end`, `exclusion_ranges [{start, end, comment}]`, `dhcp_host`; parent
  subnet is derived by the server.
- Address: `names [{name, type}]`, `hwaddr`, `interface`, `comment`, `tags`; read-only
  `state`, `usage`, `parent`.
- `dhcp_host` (`dhcp/host/<uuid>`) on Subnet and Range assigns the object to a DHCP-serving
  host.
- `dhcp_options []OptionItem`: `{type: "option", option_code: <id>, option_value: s}` or
  `{type: "group", group: <id>}`. Option code ids are opaque resource identifiers (the API
  path is `/dhcp/option_code`; **verify** the id prefix).
- Read-only utilisation: `utilization` / `utilization_v6` on IPSpace and AddressBlock,
  `dhcp_utilization` on Subnet, `utilization` on Range.

### Next-available allocation

Two mechanisms:

1. Dedicated endpoints on the parent service, which allocate and reserve in one call but
   accept only `count` and `contiguous`, so names, comment and tags need a follow-up PATCH:
   - `SubnetAPI.CreateNextAvailableIP(ctx, id).Count(1).Execute()` (`POST /ipam/subnet/{id}/nextavailableip`), same on `RangeAPI` and `AddressBlockAPI`. `ListNextAvailableIP` previews without reserving.
   - `AddressBlockAPI.CreateNextAvailableSubnet(ctx, id).Cidr(n).Count(1)` and `CreateNextAvailableAB` for child subnets and blocks.
2. `AddressAPI.Create` with the `address` field set to `<parent resource id>/nextavailableip`
   (`ipam/subnet/<uuid>/nextavailableip`, `ipam/range/<uuid>/nextavailableip` or
   `ipam/address_block/<uuid>/nextavailableip`) instead of a literal IP. This is exactly
   what the Infoblox Terraform provider does for `next_available_id`
   (`internal/service/ipam/model_ipamsvc_address.go`: `to.Address = id + "/nextavailableip"`),
   sending `space`, `names`, `comment`, `hwaddr`, `interface` and `tags` in the same body.
   The provider wraps each such create in a per-parent mutex.

The operator uses mechanism 2 because it is atomic with the ownership tags, and
serialises creates per parent like the provider does. For next-available subnets and
address blocks the provider presumably uses the analogous `/nextavailablesubnet` and
`/nextavailableaddressblock` suffixes on `SubnetAPI.Create` / `AddressBlockAPI.Create`
with `cidr` set (**verify** in `model_ipamsvc_subnet.go` and the integration tests).

### FixedAddress

- `match_type`: `mac`, `client_text`, `client_hex`, `relay_text`, `relay_hex` (IPv4 only),
  `duid` (IPv6 only).
- `disable_dhcp` turns the reservation into an exclusion without deleting it.
- `hostname`, `name`, `dhcp_options`, `tags`.

## List, filter, pagination

Go builder methods map to wire query parameters: `Fields` -> `_fields`, `Filter` ->
`_filter`, `Offset` -> `_offset`, `Limit` -> `_limit`, `PageToken` -> `_page_token`,
`OrderBy` -> `_order_by`, `Tfilter` -> `_tfilter`, `TorderBy` -> `_torder_by`,
`Inherit` -> `_inherit` (`none` default, `partial`, `full`).

`_filter` grammar: JSON field names, literals (numbers, quoted strings, `null`), operators
`==`, `!=`, `>`, `>=`, `<`, `<=`, `and`, `or`, `not`, `~` (regex), `!~`, parentheses.
Examples: `fqdn=="apps.example.com."`, `name_in_zone=="www" and type=="A"`,
`address=="10.0.10.0" and cidr==24`.

`_tfilter` filters on tags with the same shape, e.g. `("uddi_operator_uid"=="<uid>")`
(**verify** the exact grammar and which operators are supported; also **verify** the
allowed characters in tag keys before settling on the ownership tag names).

Pagination: use `_page_token` from the response when present, otherwise `_offset` /
`_limit`. Default page sizes are small; always paginate in List helpers.

## Errors, rate limiting, retries

- Every call returns `(result, *http.Response, error)`. On status >= 300 the error is an
  unexported `GenericOpenAPIError`; the response is still non-nil, so branch on
  `resp.StatusCode`. To get the body use a structural assertion:

  ```go
  if be, ok := err.(interface{ Body() []byte }); ok { body := be.Body() }
  ```
- Whether duplicates return 409 or 400 is undocumented (**verify**). The reconcile loop
  does not depend on it: it looks up by natural key before every create.
- Built-in rate limiter: token bucket, default 25 rps / burst 25, mutating verbs only.
- Built-in retry: 3 attempts, 1s to 30s exponential backoff with jitter, on 429, 500, 502,
  503, 504, honouring `Retry-After`. There is no idempotency key, so a retried POST can
  double-create. The operator disables SDK retries and relies on controller requeue with
  backoff plus the natural-key lookup, which makes creates idempotent at reconcile level.

## Examples

```go
// A record in an existing zone
rec := dnsdata.NewRecord(map[string]interface{}{"address": "10.0.0.5"})
rec.SetType("A")
rec.SetZone("dns/auth_zone/<uuid>")
rec.SetNameInZone("www")
rec.SetTags(map[string]interface{}{"uddi_operator_managed": "true"})
res, httpResp, err := c.DNSDataAPI.RecordAPI.Create(ctx).Body(*rec).Execute()
id := res.GetResult().GetId()

// Next-available IP, atomic with tags (the operator's path)
a := ipam.NewAddress("ipam/subnet/<uuid>/nextavailableip")
a.SetSpace("ipam/ip_space/<uuid>")
a.SetNames([]ipam.Name{{Name: "svc-web.team-a", Type: "user"}})
a.SetTags(map[string]interface{}{"uddi_operator_uid": uid})
res, httpResp, err := c.IPAddressManagementAPI.AddressAPI.Create(ctx).Body(*a).Execute()
allocated := res.GetResult().GetAddress()

// Next-available IP via the dedicated endpoint (preview / bulk only)
res2, _, err := c.IPAddressManagementAPI.SubnetAPI.CreateNextAvailableIP(ctx, subnetUUID).Count(1).Execute()
addr := res2.GetResults()[0]   // addr.GetAddress(), addr.GetId()
```

Note that the path parameter for the dedicated next-available endpoints is the bare UUID,
not the `ipam/subnet/` prefixed id (**verify**).

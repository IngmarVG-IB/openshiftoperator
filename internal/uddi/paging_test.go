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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	sdk "github.com/infobloxopen/universal-ddi-go-client/client"
	"github.com/infobloxopen/universal-ddi-go-client/dnsconfig"
	"github.com/infobloxopen/universal-ddi-go-client/dnsdata"
	"github.com/infobloxopen/universal-ddi-go-client/ipam"
	"github.com/infobloxopen/universal-ddi-go-client/option"
)

// apiPrefix is the path every generated Universal DDI request starts with.
const apiPrefix = "/api/ddi/v1"

// ipFor returns a distinct dotted address for each i below 2^24.
func ipFor(i int) string { return fmt.Sprintf("10.%d.%d.%d", i>>16&255, i>>8&255, i&255) }

// idsOf returns the id of every object, read from its JSON form so one helper serves all
// nine SDK types.
func idsOf[T any](res []T) []string {
	ids := make([]string, 0, len(res))
	for _, obj := range res {
		raw, err := json.Marshal(obj)
		if err != nil {
			panic(err)
		}
		var withID struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &withID); err != nil {
			panic(err)
		}
		ids = append(ids, withID.ID)
	}
	return ids
}

// listResult adapts a typed List call to the uniform shape of listKind.list.
func listResult[T any](res []T, err error) (any, []string, error) { return res, idsOf(res), err }

// listKind describes one of the nine kinds with a List call: how to create the i-th
// object in a Fake, how to list it through a Client and where the REST path is.
type listKind struct {
	method string // the List method, for example ListViews
	path   string // the REST path below apiPrefix
	create func(f *Fake, i int) error
	list   func(ctx context.Context, c *Client, o ListOptions) (items any, ids []string, err error)
}

func listKinds() []listKind {
	ctx := context.Background()
	space := ptr("ipam/ip_space/space")
	return []listKind{
		{
			method: "ListViews", path: "/dns/view",
			create: func(f *Fake, i int) error {
				_, err := f.CreateView(ctx, dnsconfig.View{Name: fmt.Sprintf("view-%05d", i)})
				return err
			},
			list: func(ctx context.Context, c *Client, o ListOptions) (any, []string, error) {
				return listResult(c.DNSConfig.ListViews(ctx, o))
			},
		},
		{
			method: "ListAuthZones", path: "/dns/auth_zone",
			create: func(f *Fake, i int) error {
				_, err := f.CreateAuthZone(ctx, dnsconfig.AuthZone{Fqdn: ptr(fmt.Sprintf("zone-%05d.example.com.", i))})
				return err
			},
			list: func(ctx context.Context, c *Client, o ListOptions) (any, []string, error) {
				return listResult(c.DNSConfig.ListAuthZones(ctx, o))
			},
		},
		{
			method: "ListRecords", path: "/dns/record",
			create: func(f *Fake, i int) error {
				_, err := f.CreateRecord(ctx, dnsdata.Record{
					Zone:       ptr("dns/auth_zone/zone"),
					NameInZone: ptr(fmt.Sprintf("host-%05d", i)),
					Type:       ptr("A"),
					Rdata:      map[string]interface{}{"address": "10.0.0.1"},
				})
				return err
			},
			list: func(ctx context.Context, c *Client, o ListOptions) (any, []string, error) {
				return listResult(c.DNSData.ListRecords(ctx, o))
			},
		},
		{
			method: "ListIPSpaces", path: "/ipam/ip_space",
			create: func(f *Fake, i int) error {
				_, err := f.CreateIPSpace(ctx, ipam.IPSpace{Name: fmt.Sprintf("space-%05d", i)})
				return err
			},
			list: func(ctx context.Context, c *Client, o ListOptions) (any, []string, error) {
				return listResult(c.IPAM.ListIPSpaces(ctx, o))
			},
		},
		{
			method: "ListAddressBlocks", path: "/ipam/address_block",
			create: func(f *Fake, i int) error {
				_, err := f.CreateAddressBlock(ctx, ipam.AddressBlock{Address: ptr(ipFor(i)), Cidr: ptr(int64(24)), Space: space})
				return err
			},
			list: func(ctx context.Context, c *Client, o ListOptions) (any, []string, error) {
				return listResult(c.IPAM.ListAddressBlocks(ctx, o))
			},
		},
		{
			method: "ListSubnets", path: "/ipam/subnet",
			create: func(f *Fake, i int) error {
				_, err := f.CreateSubnet(ctx, ipam.Subnet{Address: ptr(ipFor(i)), Cidr: ptr(int64(24)), Space: space})
				return err
			},
			list: func(ctx context.Context, c *Client, o ListOptions) (any, []string, error) {
				return listResult(c.IPAM.ListSubnets(ctx, o))
			},
		},
		{
			method: "ListRanges", path: "/ipam/range",
			create: func(f *Fake, i int) error {
				_, err := f.CreateRange(ctx, ipam.Range{Start: ipFor(i), End: ipFor(i), Space: space})
				return err
			},
			list: func(ctx context.Context, c *Client, o ListOptions) (any, []string, error) {
				return listResult(c.IPAM.ListRanges(ctx, o))
			},
		},
		{
			method: "ListAddresses", path: "/ipam/address",
			create: func(f *Fake, i int) error {
				_, err := f.CreateAddress(ctx, ipam.Address{Address: ipFor(i), Space: space})
				return err
			},
			list: func(ctx context.Context, c *Client, o ListOptions) (any, []string, error) {
				return listResult(c.IPAM.ListAddresses(ctx, o))
			},
		},
		{
			method: "ListFixedAddresses", path: "/dhcp/fixed_address",
			create: func(f *Fake, i int) error {
				_, err := f.CreateFixedAddress(ctx, ipam.FixedAddress{
					Address: ipFor(i), IpSpace: space, MatchType: "mac", MatchValue: "aa:bb:cc:dd:ee:ff",
				})
				return err
			},
			list: func(ctx context.Context, c *Client, o ListOptions) (any, []string, error) {
				return listResult(c.DHCP.ListFixedAddresses(ctx, o))
			},
		},
	}
}

func viewKind() listKind { return listKinds()[0] }

// seedKind creates n objects of the kind in f.
func seedKind(t *testing.T, f *Fake, k listKind, n int) {
	t.Helper()
	for i := range n {
		if err := k.create(f, i); err != nil {
			t.Fatalf("%s: creating object %d: %v", k.method, i, err)
		}
	}
}

// listServer serves the nine List endpoints from a Fake, honouring _offset, _limit,
// _filter and _tfilter, and records the query of every request.
type listServer struct {
	*httptest.Server
	mu      sync.Mutex
	queries []url.Values
	// failRequest, when set, is asked about each request by its 1-based number; a
	// non-zero status is answered instead of the list.
	failRequest func(n int) int
}

func newListServer(t *testing.T, f *Fake) *listServer {
	t.Helper()
	kinds := listKinds()
	s := &listServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.queries = append(s.queries, r.URL.Query())
		n := len(s.queries)
		s.mu.Unlock()
		if s.failRequest != nil {
			if status := s.failRequest(n); status != 0 {
				http.Error(w, `{"error":{"message":"scripted failure"}}`, status)
				return
			}
		}

		for _, k := range kinds {
			if r.Method != http.MethodGet || r.URL.Path != apiPrefix+k.path {
				continue
			}
			q := r.URL.Query()
			opts := ListOptions{Filter: q.Get("_filter"), TFilter: q.Get("_tfilter")}
			if v, err := strconv.ParseInt(q.Get("_offset"), 10, 32); err == nil {
				opts.Offset = int32(v)
			}
			if v, err := strconv.ParseInt(q.Get("_limit"), 10, 32); err == nil {
				opts.Limit = int32(v)
			}
			items, _, err := k.list(r.Context(), f.Client(), opts)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"results": items})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *listServer) requests() []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.queries)
}

// param returns the named query parameter of every request, "" where it was not sent.
func param(reqs []url.Values, name string) []string {
	out := make([]string, 0, len(reqs))
	for _, q := range reqs {
		out = append(out, q.Get(name))
	}
	return out
}

// newTestSDK returns a Client backed by the real SDK against baseURL, with the production
// transport chain except that waits between retries are skipped.
func newTestSDK(t *testing.T, baseURL string) *Client {
	t.Helper()
	httpClient := newHTTPClient(&http.Client{}, 0, 0, "test-"+t.Name())
	rt, ok := httpClient.Transport.(*retryTransport)
	if !ok {
		t.Fatalf("outermost transport is %T, want *retryTransport", httpClient.Transport)
	}
	rt.sleep = noSleep
	c := &sdkClient{api: sdk.NewAPIClient(
		option.WithAPIKey("test-key"),
		option.WithCSPUrl(baseURL),
		option.WithHTTPClient(httpClient),
	)}
	return &Client{DNSConfig: c, DNSData: c, IPAM: c, DHCP: c}
}

func TestListFetchesEveryPageWhenLimitIsZero(t *testing.T) {
	f := NewFake()
	seedKind(t, f, viewKind(), 2500)
	srv := newListServer(t, f)
	c := newTestSDK(t, srv.URL)

	got, err := c.DNSConfig.ListViews(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("ListViews: %v", err)
	}
	if len(got) != 2500 {
		t.Fatalf("ListViews returned %d views, want 2500", len(got))
	}
	seen := map[string]bool{}
	for _, v := range got {
		seen[v.Name] = true
	}
	if len(seen) != 2500 {
		t.Fatalf("the 2500 views include %d distinct names, want 2500: pages overlapped or skipped", len(seen))
	}

	reqs := srv.requests()
	if len(reqs) != 3 {
		t.Fatalf("server saw %d requests, want 3 pages", len(reqs))
	}
	if got, want := param(reqs, "_limit"), []string{"1000", "1000", "1000"}; !slices.Equal(got, want) {
		t.Fatalf("_limit per request = %v, want %v", got, want)
	}
	if got, want := param(reqs, "_offset"), []string{"", "1000", "2000"}; !slices.Equal(got, want) {
		t.Fatalf("_offset per request = %v, want %v", got, want)
	}
}

func TestListPagesEveryKind(t *testing.T) {
	for _, k := range listKinds() {
		t.Run(k.method, func(t *testing.T) {
			f := NewFake()
			seedKind(t, f, k, listPageSize+1)
			srv := newListServer(t, f)
			c := newTestSDK(t, srv.URL)

			_, ids, err := k.list(context.Background(), c, ListOptions{})
			if err != nil {
				t.Fatalf("%s: %v", k.method, err)
			}
			if len(ids) != listPageSize+1 {
				t.Fatalf("%s returned %d objects, want %d", k.method, len(ids), listPageSize+1)
			}
			distinct := map[string]bool{}
			for _, id := range ids {
				distinct[id] = true
			}
			if len(distinct) != len(ids) {
				t.Fatalf("%s returned %d distinct ids for %d objects", k.method, len(distinct), len(ids))
			}
			if got, want := param(srv.requests(), "_offset"), []string{"", "1000"}; !slices.Equal(got, want) {
				t.Fatalf("%s _offset per request = %v, want %v", k.method, got, want)
			}
		})
	}
}

func TestListEndsWithAnEmptyPageWhenTheCountIsAMultipleOfThePageSize(t *testing.T) {
	f := NewFake()
	seedKind(t, f, viewKind(), 2000)
	srv := newListServer(t, f)
	c := newTestSDK(t, srv.URL)

	got, err := c.DNSConfig.ListViews(context.Background(), ListOptions{})
	if err != nil || len(got) != 2000 {
		t.Fatalf("ListViews = (%d views, %v), want 2000", len(got), err)
	}
	if got, want := param(srv.requests(), "_offset"), []string{"", "1000", "2000"}; !slices.Equal(got, want) {
		t.Fatalf("_offset per request = %v, want %v (the last page is empty)", got, want)
	}
}

func TestListOfASmallCollectionIsOneRequest(t *testing.T) {
	f := NewFake()
	seedKind(t, f, viewKind(), 3)
	srv := newListServer(t, f)
	c := newTestSDK(t, srv.URL)

	got, err := c.DNSConfig.ListViews(context.Background(), ListOptions{})
	if err != nil || len(got) != 3 {
		t.Fatalf("ListViews = (%d views, %v), want 3", len(got), err)
	}
	if n := len(srv.requests()); n != 1 {
		t.Fatalf("server saw %d requests, want 1", n)
	}
}

func TestListWithALimitIsASingleRequest(t *testing.T) {
	tests := []struct {
		name       string
		opts       ListOptions
		wantCount  int
		wantLimit  string
		wantOffset string
	}{
		{"limit 1 for a lookup", ListOptions{Limit: 1}, 1, "1", ""},
		{"limit and offset are passed through", ListOptions{Limit: 10, Offset: 5}, 10, "10", "5"},
		{"a limit of one full page does not page on", ListOptions{Limit: 1000}, 1000, "1000", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewFake()
			seedKind(t, f, viewKind(), 2500)
			srv := newListServer(t, f)
			c := newTestSDK(t, srv.URL)

			got, err := c.DNSConfig.ListViews(context.Background(), tt.opts)
			if err != nil {
				t.Fatalf("ListViews: %v", err)
			}
			if len(got) != tt.wantCount {
				t.Fatalf("ListViews returned %d views, want %d", len(got), tt.wantCount)
			}
			reqs := srv.requests()
			if len(reqs) != 1 {
				t.Fatalf("server saw %d requests, want exactly 1", len(reqs))
			}
			if got := reqs[0].Get("_limit"); got != tt.wantLimit {
				t.Fatalf("_limit = %q, want %q", got, tt.wantLimit)
			}
			if got := reqs[0].Get("_offset"); got != tt.wantOffset {
				t.Fatalf("_offset = %q, want %q", got, tt.wantOffset)
			}
		})
	}
}

func TestListWithAnOffsetStartsPagingThere(t *testing.T) {
	f := NewFake()
	seedKind(t, f, viewKind(), 2500)
	srv := newListServer(t, f)
	c := newTestSDK(t, srv.URL)

	got, err := c.DNSConfig.ListViews(context.Background(), ListOptions{Offset: 1500})
	if err != nil {
		t.Fatalf("ListViews: %v", err)
	}
	if len(got) != 1000 {
		t.Fatalf("ListViews returned %d views, want the 1000 after offset 1500", len(got))
	}
	if got, want := param(srv.requests(), "_offset"), []string{"1500", "2500"}; !slices.Equal(got, want) {
		t.Fatalf("_offset per request = %v, want %v", got, want)
	}
}

func TestListForwardsItsOptionsOnEveryPage(t *testing.T) {
	f := NewFake()
	seedKind(t, f, viewKind(), 1500)
	srv := newListServer(t, f)
	c := newTestSDK(t, srv.URL)

	got, err := c.DNSConfig.ListViews(context.Background(), ListOptions{
		Filter: `name!="never"`, Fields: "name,id", OrderBy: "name",
	})
	if err != nil || len(got) != 1500 {
		t.Fatalf("ListViews = (%d views, %v), want 1500", len(got), err)
	}
	reqs := srv.requests()
	if len(reqs) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(reqs))
	}
	for i, q := range reqs {
		for name, want := range map[string]string{"_filter": `name!="never"`, "_fields": "name,id", "_order_by": "name"} {
			if got := q.Get(name); got != want {
				t.Fatalf("request %d: %s = %q, want %q", i+1, name, got, want)
			}
		}
	}
}

func TestListFailsInsteadOfLoopingPastTheCap(t *testing.T) {
	// A server that always has another full page.
	var page strings.Builder
	page.WriteString(`{"results":[`)
	for i := range listPageSize {
		if i > 0 {
			page.WriteByte(',')
		}
		fmt.Fprintf(&page, `{"name":"view-%d"}`, i)
	}
	page.WriteString(`]}`)

	var mu sync.Mutex
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(page.String()))
	}))
	defer srv.Close()
	c := newTestSDK(t, srv.URL)

	got, err := c.DNSConfig.ListViews(context.Background(), ListOptions{})
	if !errors.Is(err, ErrTooManyPages) {
		t.Fatalf("err = %v, want ErrTooManyPages", err)
	}
	if !strings.Contains(err.Error(), "ListViews") {
		t.Fatalf("err = %q, want it to name the kind (ListViews)", err)
	}
	if got != nil {
		t.Fatalf("got %d results with the error, want none", len(got))
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != listMaxPages {
		t.Fatalf("server saw %d requests, want %d", requests, listMaxPages)
	}
}

func TestListAFailedPageFailsTheWholeCall(t *testing.T) {
	f := NewFake()
	seedKind(t, f, viewKind(), 2500)
	srv := newListServer(t, f)
	srv.failRequest = func(n int) int {
		if n == 2 {
			return http.StatusInternalServerError
		}
		return 0
	}
	c := newTestSDK(t, srv.URL)

	got, err := c.DNSConfig.ListViews(context.Background(), ListOptions{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError || apiErr.Method != "ListViews" {
		t.Fatalf("err = %v, want an APIError with status 500 from ListViews", err)
	}
	if got != nil {
		t.Fatalf("got %d results with the error, want none", len(got))
	}
	if n := len(srv.requests()); n != 2 {
		t.Fatalf("server saw %d requests, want 2: a 500 is not retried and ends the walk", n)
	}
}

// TestListAllWalksOffsetsInPagesOfOneThousand drives the paging helper alone, without HTTP.
func TestListAllWalksOffsetsInPagesOfOneThousand(t *testing.T) {
	const total = 2500
	var calls []ListOptions
	fetch := func(page ListOptions) ([]int, error) {
		calls = append(calls, page)
		start := int(page.Offset)
		end := min(start+int(page.Limit), total)
		if start >= total {
			return nil, nil
		}
		out := make([]int, 0, end-start)
		for i := start; i < end; i++ {
			out = append(out, i)
		}
		return out, nil
	}

	got, err := listAll("ListThings", ListOptions{Filter: "f"}, fetch)
	if err != nil {
		t.Fatalf("listAll: %v", err)
	}
	if len(got) != total {
		t.Fatalf("listAll returned %d items, want %d", len(got), total)
	}
	for i, v := range got {
		if v != i {
			t.Fatalf("item %d = %d, want %d", i, v, i)
		}
	}
	wantOffsets := []int32{0, 1000, 2000}
	if len(calls) != len(wantOffsets) {
		t.Fatalf("fetch ran %d times, want %d", len(calls), len(wantOffsets))
	}
	for i, c := range calls {
		if c.Offset != wantOffsets[i] || c.Limit != listPageSize || c.Filter != "f" {
			t.Fatalf("call %d = %+v, want offset %d, limit %d, filter f", i+1, c, wantOffsets[i], listPageSize)
		}
	}
}

func TestListAllRunsFetchOnceForANonZeroLimit(t *testing.T) {
	calls := 0
	got, err := listAll("ListThings", ListOptions{Limit: 1, Offset: 7}, func(page ListOptions) ([]int, error) {
		calls++
		if page.Limit != 1 || page.Offset != 7 {
			t.Fatalf("fetch got %+v, want the options unchanged", page)
		}
		return make([]int, listPageSize), nil // a full page, which must not trigger another fetch
	})
	if err != nil || calls != 1 || len(got) != listPageSize {
		t.Fatalf("listAll = (%d items, %v) after %d calls, want one call", len(got), err, calls)
	}
}

func TestListAllStopsAtTheCapWithAnErrorNamingTheKind(t *testing.T) {
	calls := 0
	got, err := listAll("ListWidgets", ListOptions{}, func(ListOptions) ([]int, error) {
		calls++
		return make([]int, listPageSize), nil
	})
	if !errors.Is(err, ErrTooManyPages) {
		t.Fatalf("err = %v, want ErrTooManyPages", err)
	}
	if !strings.Contains(err.Error(), "ListWidgets") {
		t.Fatalf("err = %q, want it to name ListWidgets", err)
	}
	if got != nil {
		t.Fatalf("got %d items with the error, want none", len(got))
	}
	if calls != listMaxPages {
		t.Fatalf("fetch ran %d times, want %d", calls, listMaxPages)
	}
}

func TestListAllAcceptsAShortPageAsTheLastAllowedPage(t *testing.T) {
	// 99 full pages and a short hundredth is within the cap.
	calls := 0
	got, err := listAll("ListThings", ListOptions{}, func(ListOptions) ([]int, error) {
		calls++
		if calls == listMaxPages {
			return make([]int, 5), nil
		}
		return make([]int, listPageSize), nil
	})
	if err != nil || len(got) != (listMaxPages-1)*listPageSize+5 {
		t.Fatalf("listAll = (%d items, %v), want %d items", len(got), err, (listMaxPages-1)*listPageSize+5)
	}
}

func TestFakeListHonoursOffsetAndLimit(t *testing.T) {
	const n = 25
	for _, k := range listKinds() {
		t.Run(k.method, func(t *testing.T) {
			f := NewFake()
			seedKind(t, f, k, n)
			c := f.Client()
			ctx := context.Background()

			_, all, err := k.list(ctx, c, ListOptions{})
			if err != nil || len(all) != n {
				t.Fatalf("unpaged list = (%d, %v), want %d", len(all), err, n)
			}
			tests := []struct {
				name string
				opts ListOptions
				want []string
			}{
				{"limit alone", ListOptions{Limit: 10}, all[:10]},
				{"offset and limit", ListOptions{Offset: 20, Limit: 10}, all[20:]},
				{"offset alone means to the end", ListOptions{Offset: 5}, all[5:]},
				{"offset at the end", ListOptions{Offset: n}, nil},
				{"offset past the end", ListOptions{Offset: n + 10, Limit: 5}, nil},
				{"limit above the count", ListOptions{Limit: 1000}, all},
			}
			for _, tt := range tests {
				_, got, err := k.list(ctx, c, tt.opts)
				if err != nil {
					t.Fatalf("%s: %v", tt.name, err)
				}
				if !slices.Equal(got, tt.want) {
					t.Fatalf("%s: got %d ids, want %d (same ids, same order)", tt.name, len(got), len(tt.want))
				}
			}

			// Walking the collection in pages of 7 yields every object exactly once, in order.
			var walked []string
			for off := int32(0); ; off += 7 {
				_, page, err := k.list(ctx, c, ListOptions{Offset: off, Limit: 7})
				if err != nil {
					t.Fatalf("page at %d: %v", off, err)
				}
				walked = append(walked, page...)
				if len(page) < 7 {
					break
				}
			}
			if !slices.Equal(walked, all) {
				t.Fatalf("paging by 7 yielded %d ids that differ from the unpaged %d", len(walked), len(all))
			}
		})
	}
}

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
	"sync/atomic"
	"testing"
	"time"

	"github.com/infobloxopen/universal-ddi-go-client/dnsconfig"
	"github.com/infobloxopen/universal-ddi-go-client/ipam"
)

// fakeClock is a settable time source for the resolver's cache timing.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// newTestResolver returns a resolver over a fresh Fake with a controllable clock.
func newTestResolver(t *testing.T) (*OptionCodeResolver, *Fake, *fakeClock) {
	t.Helper()
	f := NewFake()
	clk := newFakeClock()
	r := NewOptionCodeResolver(f)
	r.now = clk.now
	return r, f, clk
}

func mustResolve(t *testing.T, r *OptionCodeResolver, name, space, want string) {
	t.Helper()
	got, err := r.Resolve(context.Background(), name, space)
	if err != nil {
		t.Fatalf("Resolve(%q, %q): %v", name, space, err)
	}
	if got != want {
		t.Fatalf("Resolve(%q, %q) = %q, want %q", name, space, got, want)
	}
}

func TestFakeSeedsStandardOptionCodes(t *testing.T) {
	f := NewFake()
	codes, err := f.ListOptionCodes(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("ListOptionCodes: %v", err)
	}
	type key struct {
		space string
		name  string
	}
	got := map[key]int64{}
	for _, c := range codes {
		got[key{c.OptionSpace, c.Name}] = c.Code
		if c.Id == nil || !strings.HasPrefix(*c.Id, PrefixOptionCode+"/") {
			t.Errorf("option code %q has id %v, want a %s id", c.Name, c.Id, PrefixOptionCode)
		}
	}
	want := map[key]int64{
		{FakeOptionSpaceDHCPv4, "routers"}:             3,
		{FakeOptionSpaceDHCPv4, "domain-name-servers"}: 6,
		{FakeOptionSpaceDHCPv4, "domain-name"}:         15,
		{FakeOptionSpaceDHCPv4, "ntp-servers"}:         42,
		{FakeOptionSpaceDHCPv4, "domain-search"}:       119,
		{FakeOptionSpaceVendor, "ntp-servers"}:         200,
	}
	if len(got) != len(want) {
		t.Fatalf("fake holds %d option codes, want %d: %v", len(got), len(want), got)
	}
	for k, code := range want {
		if got[k] != code {
			t.Errorf("option code %q in %s = %d, want %d", k.name, k.space, got[k], code)
		}
	}

	// Seeding must not shift the ids of the objects tests create.
	view, err := f.CreateView(context.Background(), dnsconfig.View{Name: "v"})
	if err != nil {
		t.Fatalf("CreateView: %v", err)
	}
	if want := PrefixView + "/00000001-0000-4000-8000-000000000001"; *view.Id != want {
		t.Errorf("first view id = %q, want %q: seeding consumed the id counter", *view.Id, want)
	}

	// A filter selects by name, and Reset puts a changed list back.
	byName, err := f.ListOptionCodes(context.Background(), ListOptions{Filter: `name=="ntp-servers"`})
	if err != nil || len(byName) != 2 {
		t.Fatalf("ListOptionCodes(name==ntp-servers) = %d codes, %v; want 2", len(byName), err)
	}
	added, err := f.CreateOptionCode(context.Background(), ipam.OptionCode{
		Code: 252, Name: "wpad", OptionSpace: FakeOptionSpaceDHCPv4, Type: "text",
	})
	if err != nil || added.Id == nil {
		t.Fatalf("CreateOptionCode: %v", err)
	}
	if _, err := f.CreateOptionCode(context.Background(), ipam.OptionCode{
		Code: 253, Name: "wpad", OptionSpace: FakeOptionSpaceDHCPv4, Type: "text",
	}); !IsConflict(err) {
		t.Errorf("a second wpad in the same space: err = %v, want a conflict", err)
	}
	f.Reset()
	all, _ := f.ListOptionCodes(context.Background(), ListOptions{})
	if len(all) != len(want) {
		t.Errorf("after Reset the fake holds %d option codes, want the %d seeded ones", len(all), len(want))
	}
}

func TestResolverResolvesFromTheCacheWithoutListingAgain(t *testing.T) {
	r, f, _ := newTestResolver(t)

	mustResolve(t, r, "routers", "", FakeOptionCodeID(3))
	mustResolve(t, r, "domain-name", "", FakeOptionCodeID(15))
	mustResolve(t, r, "routers", FakeOptionSpaceDHCPv4, FakeOptionCodeID(3))
	mustResolve(t, r, "routers", "", FakeOptionCodeID(3))

	if n := f.CallCount("ListOptionCodes"); n != 1 {
		t.Fatalf("ListOptionCodes called %d times, want 1: hits must come from the cache", n)
	}
}

func TestResolverListsAgainAfterTheTTL(t *testing.T) {
	r, f, clk := newTestResolver(t)

	mustResolve(t, r, "routers", "", FakeOptionCodeID(3))
	clk.advance(OptionCodeTTL - time.Second)
	mustResolve(t, r, "routers", "", FakeOptionCodeID(3))
	if n := f.CallCount("ListOptionCodes"); n != 1 {
		t.Fatalf("ListOptionCodes called %d times just before the TTL, want 1", n)
	}

	clk.advance(time.Second)
	mustResolve(t, r, "routers", "", FakeOptionCodeID(3))
	if n := f.CallCount("ListOptionCodes"); n != 2 {
		t.Fatalf("ListOptionCodes called %d times at the TTL, want 2", n)
	}
	mustResolve(t, r, "routers", "", FakeOptionCodeID(3))
	if n := f.CallCount("ListOptionCodes"); n != 2 {
		t.Fatalf("ListOptionCodes called %d times after the reload, want 2", n)
	}
}

func TestResolverRefreshesOnAMissAtMostOncePerMinute(t *testing.T) {
	r, f, clk := newTestResolver(t)
	ctx := context.Background()

	mustResolve(t, r, "routers", "", FakeOptionCodeID(3))
	// The tenant gains a code after the cache was filled.
	added, err := f.CreateOptionCode(ctx, ipam.OptionCode{
		Code: 252, Name: "wpad", OptionSpace: FakeOptionSpaceDHCPv4, Type: "text",
	})
	if err != nil {
		t.Fatalf("CreateOptionCode: %v", err)
	}

	// A miss right after a list does not list again.
	if _, err := r.Resolve(ctx, "wpad", ""); !errors.Is(err, ErrOptionCodeNotFound) {
		t.Fatalf("Resolve(wpad) = %v, want not found while the list is fresh", err)
	}
	clk.advance(OptionCodeMissRefresh - time.Second)
	if _, err := r.Resolve(ctx, "wpad", ""); !errors.Is(err, ErrOptionCodeNotFound) {
		t.Fatalf("Resolve(wpad) = %v, want not found one second before the refresh interval", err)
	}
	if n := f.CallCount("ListOptionCodes"); n != 1 {
		t.Fatalf("ListOptionCodes called %d times inside the refresh interval, want 1", n)
	}

	// Once the list is a minute old, the next miss lists and finds the new code.
	clk.advance(time.Second)
	mustResolve(t, r, "wpad", "", *added.Id)
	if n := f.CallCount("ListOptionCodes"); n != 2 {
		t.Fatalf("ListOptionCodes called %d times after the refresh interval, want 2", n)
	}

	// The refresh restarts the interval: a name that still does not exist waits again.
	if _, err := r.Resolve(ctx, "still-missing", ""); !errors.Is(err, ErrOptionCodeNotFound) {
		t.Fatalf("Resolve(still-missing) = %v, want not found", err)
	}
	clk.advance(OptionCodeMissRefresh - time.Second)
	if _, err := r.Resolve(ctx, "still-missing", ""); !errors.Is(err, ErrOptionCodeNotFound) {
		t.Fatalf("Resolve(still-missing) = %v, want not found", err)
	}
	if n := f.CallCount("ListOptionCodes"); n != 2 {
		t.Fatalf("ListOptionCodes called %d times for repeated misses, want 2", n)
	}
	clk.advance(time.Second)
	if _, err := r.Resolve(ctx, "still-missing", ""); !errors.Is(err, ErrOptionCodeNotFound) {
		t.Fatalf("Resolve(still-missing) = %v, want not found", err)
	}
	if n := f.CallCount("ListOptionCodes"); n != 3 {
		t.Fatalf("ListOptionCodes called %d times after a second interval, want 3", n)
	}

	// A hit never triggers a refresh, however old the list is below the TTL.
	clk.advance(OptionCodeTTL / 2)
	mustResolve(t, r, "routers", "", FakeOptionCodeID(3))
	if n := f.CallCount("ListOptionCodes"); n != 3 {
		t.Fatalf("a hit listed again: %d calls, want 3", n)
	}
}

func TestResolverAmbiguityNamesTheSpaces(t *testing.T) {
	r, _, _ := newTestResolver(t)
	ctx := context.Background()

	_, err := r.Resolve(ctx, "ntp-servers", "")
	if !errors.Is(err, ErrOptionCodeAmbiguous) {
		t.Fatalf("Resolve(ntp-servers) = %v, want ErrOptionCodeAmbiguous", err)
	}
	if errors.Is(err, ErrOptionCodeNotFound) {
		t.Fatalf("an ambiguous name must not also report not found: %v", err)
	}
	var oce *OptionCodeError
	if !errors.As(err, &oce) {
		t.Fatalf("err = %T, want *OptionCodeError", err)
	}
	if want := []string{FakeOptionSpaceDHCPv4, FakeOptionSpaceVendor}; !slices.Equal(oce.Spaces, want) {
		t.Errorf("Spaces = %v, want %v", oce.Spaces, want)
	}
	for _, want := range []string{`"ntp-servers"`, FakeOptionSpaceDHCPv4, FakeOptionSpaceVendor, "optionSpace"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	// Naming the option space settles it, as the full id or as the bare uuid.
	mustResolve(t, r, "ntp-servers", FakeOptionSpaceDHCPv4, FakeOptionCodeID(42))
	mustResolve(t, r, "ntp-servers", FakeOptionSpaceVendor, FakeOptionCodeID(200))
	mustResolve(t, r, "ntp-servers", BareID(FakeOptionSpaceVendor), FakeOptionCodeID(200))
}

func TestResolverNotFoundNamesTheOptionAndSpace(t *testing.T) {
	r, _, _ := newTestResolver(t)
	ctx := context.Background()

	_, err := r.Resolve(ctx, "no-such-option", "")
	if !errors.Is(err, ErrOptionCodeNotFound) {
		t.Fatalf("Resolve(no-such-option) = %v, want ErrOptionCodeNotFound", err)
	}
	if !strings.Contains(err.Error(), `"no-such-option"`) {
		t.Errorf("error %q does not name the option", err)
	}

	// routers exists, but not in the vendor space.
	_, err = r.Resolve(ctx, "routers", FakeOptionSpaceVendor)
	if !errors.Is(err, ErrOptionCodeNotFound) {
		t.Fatalf("Resolve(routers in vendor space) = %v, want ErrOptionCodeNotFound", err)
	}
	for _, want := range []string{`"routers"`, FakeOptionSpaceVendor} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	// Names match exactly, case included.
	if _, err := r.Resolve(ctx, "Routers", ""); !errors.Is(err, ErrOptionCodeNotFound) {
		t.Errorf("Resolve(Routers) = %v, want not found: names are case sensitive", err)
	}
}

func TestResolverDoesNotCacheAFailedList(t *testing.T) {
	r, f, _ := newTestResolver(t)
	f.PushStatus("ListOptionCodes", http.StatusInternalServerError, "boom")

	_, err := r.Resolve(context.Background(), "routers", "")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusInternalServerError {
		t.Fatalf("Resolve = %v, want the 500 from the list call", err)
	}
	if errors.Is(err, ErrOptionCodeNotFound) {
		t.Fatalf("a failed list must not look like a missing option: %v", err)
	}

	mustResolve(t, r, "routers", "", FakeOptionCodeID(3))
	if n := f.CallCount("ListOptionCodes"); n != 2 {
		t.Fatalf("ListOptionCodes called %d times, want 2: the failure must not have been cached", n)
	}
}

func TestResolverInvalidateForcesAList(t *testing.T) {
	r, f, _ := newTestResolver(t)

	mustResolve(t, r, "routers", "", FakeOptionCodeID(3))
	r.Invalidate()
	mustResolve(t, r, "routers", "", FakeOptionCodeID(3))
	if n := f.CallCount("ListOptionCodes"); n != 2 {
		t.Fatalf("ListOptionCodes called %d times, want 2", n)
	}
}

// listerFunc adapts a function to OptionCodeLister.
type listerFunc func(ctx context.Context, opts ListOptions) ([]ipam.OptionCode, error)

func (f listerFunc) ListOptionCodes(ctx context.Context, opts ListOptions) ([]ipam.OptionCode, error) {
	return f(ctx, opts)
}

func TestResolverIgnoresACodeListedTwice(t *testing.T) {
	routers := ipam.OptionCode{Id: ptr(FakeOptionCodeID(3)), Code: 3, Name: "routers", OptionSpace: FakeOptionSpaceDHCPv4, Type: "address4"}
	r := NewOptionCodeResolver(listerFunc(func(context.Context, ListOptions) ([]ipam.OptionCode, error) {
		// Offset paging can return an object on two pages when the tenant changes between
		// them; a code with no id or no name is skipped as well.
		return []ipam.OptionCode{routers, routers, {Name: "no-id", OptionSpace: FakeOptionSpaceDHCPv4}, {Id: ptr("dhcp/option_code/x")}}, nil
	}))

	mustResolve(t, r, "routers", "", FakeOptionCodeID(3))
	if _, err := r.Resolve(context.Background(), "no-id", ""); !errors.Is(err, ErrOptionCodeNotFound) {
		t.Errorf("a code without an id must not resolve: %v", err)
	}
}

func TestResolverQualifiesABareOptionCodeID(t *testing.T) {
	r := NewOptionCodeResolver(listerFunc(func(context.Context, ListOptions) ([]ipam.OptionCode, error) {
		return []ipam.OptionCode{{Id: ptr("0123-uuid"), Code: 3, Name: "routers", OptionSpace: FakeOptionSpaceDHCPv4}}, nil
	}))
	mustResolve(t, r, "routers", "", PrefixOptionCode+"/0123-uuid")
}

// Concurrent callers on a cold cache share one list call, and the resolver is clean under
// the race detector with hits, misses, ambiguity and invalidation running together.
func TestResolverIsSafeForConcurrentUse(t *testing.T) {
	var calls atomic.Int32
	f := NewFake()
	release := make(chan struct{})
	r := NewOptionCodeResolver(listerFunc(func(ctx context.Context, opts ListOptions) ([]ipam.OptionCode, error) {
		calls.Add(1)
		<-release
		return f.ListOptionCodes(ctx, opts)
	}))

	const callers = 64
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := r.Resolve(context.Background(), "routers", "")
			if err == nil && got != FakeOptionCodeID(3) {
				err = fmt.Errorf("caller %d: resolved to %q", i, got)
			}
			errs <- err
		}()
	}
	// Let every caller reach the gate or the snapshot before the one list call finishes.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d callers listed %d times on a cold cache, want 1", callers, n)
	}

	// Mixed load: hits, ambiguity, misses and Invalidate at once. Only the race detector
	// and the absence of a panic or an unexpected error are asserted.
	r2 := NewOptionCodeResolver(f)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			switch i % 4 {
			case 0:
				if _, err := r2.Resolve(ctx, "routers", ""); err != nil {
					t.Errorf("routers: %v", err)
				}
			case 1:
				if _, err := r2.Resolve(ctx, "ntp-servers", ""); !errors.Is(err, ErrOptionCodeAmbiguous) {
					t.Errorf("ntp-servers: %v, want ambiguous", err)
				}
			case 2:
				if _, err := r2.Resolve(ctx, "missing", ""); !errors.Is(err, ErrOptionCodeNotFound) {
					t.Errorf("missing: %v, want not found", err)
				}
			default:
				r2.Invalidate()
			}
		}()
	}
	wg.Wait()
}

// A caller waiting for another caller's list gives up when its own context ends, and the
// list that is under way still completes for everyone else.
func TestResolverWaiterHonoursItsContext(t *testing.T) {
	f := NewFake()
	started := make(chan struct{})
	release := make(chan struct{})
	r := NewOptionCodeResolver(listerFunc(func(ctx context.Context, opts ListOptions) ([]ipam.OptionCode, error) {
		close(started)
		<-release
		return f.ListOptionCodes(ctx, opts)
	}))

	first := make(chan error, 1)
	go func() {
		_, err := r.Resolve(context.Background(), "routers", "")
		first <- err
	}()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := r.Resolve(ctx, "routers", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting caller got %v, want context.DeadlineExceeded", err)
	}

	close(release)
	if err := <-first; err != nil {
		t.Fatalf("the caller that listed failed: %v", err)
	}
	mustResolve(t, r, "routers", "", FakeOptionCodeID(3))
}

func TestClientsBuiltByTheConstructorsCarryAResolver(t *testing.T) {
	f := NewFake()
	a, b := f.Client(), f.Client()
	if a.OptionCodes == nil || b.OptionCodes == nil {
		t.Fatal("Fake.Client must set OptionCodes")
	}
	if a.OptionCodes == b.OptionCodes {
		t.Error("every Fake.Client needs its own resolver, so its cache lives as long as that client")
	}

	c, err := NewSDKClient(Config{APIKey: "not-a-real-key"})
	if err != nil {
		t.Fatalf("NewSDKClient: %v", err)
	}
	if c.OptionCodes == nil {
		t.Fatal("NewSDKClient must set OptionCodes")
	}
}

// optionCodeServer serves GET /dhcp/option_code from a Fake, honouring _offset, _limit
// and _filter, and records each request's query.
func optionCodeServer(t *testing.T, f *Fake) (*httptest.Server, func() []url.Values) {
	t.Helper()
	var (
		mu      sync.Mutex
		queries []url.Values
	)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != apiPrefix+"/dhcp/option_code" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		mu.Lock()
		queries = append(queries, q)
		mu.Unlock()
		opts := ListOptions{Filter: q.Get("_filter")}
		if v, err := strconv.ParseInt(q.Get("_offset"), 10, 32); err == nil {
			opts.Offset = int32(v)
		}
		if v, err := strconv.ParseInt(q.Get("_limit"), 10, 32); err == nil {
			opts.Limit = int32(v)
		}
		items, err := f.ListOptionCodes(r.Context(), opts)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": items})
	}))
	t.Cleanup(s.Close)
	return s, func() []url.Values {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(queries)
	}
}

func TestSDKClientListsOptionCodesAndResolvesThem(t *testing.T) {
	f := NewFake()
	for i := range listPageSize + 5 {
		if _, err := f.CreateOptionCode(context.Background(), ipam.OptionCode{
			Code: int64(1000 + i), Name: fmt.Sprintf("vendor-%05d", i), OptionSpace: FakeOptionSpaceVendor, Type: "text",
		}); err != nil {
			t.Fatalf("CreateOptionCode %d: %v", i, err)
		}
	}
	srv, requests := optionCodeServer(t, f)
	c := newTestSDK(t, srv.URL)

	all, err := c.DHCP.ListOptionCodes(context.Background(), ListOptions{})
	if err != nil {
		t.Fatalf("ListOptionCodes: %v", err)
	}
	if want := listPageSize + 5 + 6; len(all) != want {
		t.Fatalf("ListOptionCodes returned %d codes, want %d", len(all), want)
	}
	reqs := requests()
	if got, want := param(reqs, "_offset"), []string{"", "1000"}; !slices.Equal(got, want) {
		t.Fatalf("_offset per request = %v, want %v: the list must page", got, want)
	}

	byName, err := c.DHCP.ListOptionCodes(context.Background(), ListOptions{Filter: `name=="routers"`, TFilter: "ignored"})
	if err != nil || len(byName) != 1 || byName[0].Code != 3 {
		t.Fatalf("ListOptionCodes(name==routers) = %v, %v; want the routers code", byName, err)
	}
	last := requests()[len(requests())-1]
	if last.Get("_filter") != `name=="routers"` || last.Has("_tfilter") {
		t.Errorf("last query = %v, want _filter and no _tfilter: option codes carry no tags", last)
	}

	// The resolver works over the real SDK client the same way.
	r := NewOptionCodeResolver(c.DHCP)
	mustResolve(t, r, "routers", "", FakeOptionCodeID(3))
	mustResolve(t, r, "vendor-00007", FakeOptionSpaceVendor, func() string {
		got, _ := f.ListOptionCodes(context.Background(), ListOptions{Filter: `name=="vendor-00007"`})
		return *got[0].Id
	}())
}

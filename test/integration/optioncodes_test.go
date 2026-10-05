//go:build integration

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

package integration

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/infobloxopen/universal-ddi-go-client/ipam"

	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// standardV4Options are the DHCPv4 options the operator's fake seeds and the docs use as
// examples. Whether a tenant names them the same way is part of what this test reports.
var standardV4Options = []struct {
	number int64
	name   string
}{
	{3, "routers"},
	{6, "domain-name-servers"},
	{15, "domain-name"},
	{42, "ntp-servers"},
	{119, "domain-search"},
}

// TestOptionCodes answers what the lookup of DHCP option codes by name (dhcpOptions
// optionName) rests on, and DESIGN.md section 14 open question 8: what the id of an option
// code looks like. It only reads: it lists every option code in the tenant and logs
// VERDICT lines for
//
//   - the id prefix of option codes and of option spaces, to compare with
//     uddi.PrefixOptionCode and uddi.PrefixOptionSpace;
//   - the default DHCPv4 option space id, taken to be the option space that holds the
//     standard DHCPv4 option numbers;
//   - whether "routers" resolves unambiguously, which is what a dhcpOptions entry with
//     optionName: routers and no optionSpace needs.
func TestOptionCodes(t *testing.T) {
	requireAPIKey(t)
	ctx := newCtx(t)

	codes, err := client.DHCP.ListOptionCodes(ctx, uddi.ListOptions{})
	if err != nil {
		t.Fatalf("ListOptionCodes: %v", err)
	}
	if len(codes) == 0 {
		t.Fatal("VERDICT: the tenant lists no DHCP option codes, so no optionName can resolve on it")
	}
	t.Logf("ListOptionCodes returned %d option codes", len(codes))

	// Id prefixes.
	codePrefixes := map[string]int{}
	spacePrefixes := map[string]int{}
	for i := range codes {
		codePrefixes[idPrefix(strv(codes[i].Id))]++
		spacePrefixes[idPrefix(codes[i].OptionSpace)]++
	}
	t.Logf("VERDICT: %s; the operator assumes %q (uddi.PrefixOptionCode)",
		describePrefixes("option code ids", codePrefixes, len(codes)), uddi.PrefixOptionCode)
	t.Logf("VERDICT: %s; the operator assumes %q (uddi.PrefixOptionSpace)",
		describePrefixes("option space ids", spacePrefixes, len(codes)), uddi.PrefixOptionSpace)

	// Default DHCPv4 option space.
	t.Log(defaultV4SpaceVerdict(codes))

	// "routers", through the list and through the resolver the controllers use.
	var routers []ipam.OptionCode
	for i := range codes {
		if codes[i].Name == "routers" {
			routers = append(routers, codes[i])
		}
	}
	filtered, err := client.DHCP.ListOptionCodes(ctx, uddi.ListOptions{Filter: `name=="routers"`})
	switch {
	case err != nil:
		t.Logf("VERDICT: _filter name==\"routers\" on /dhcp/option_code is rejected (%v); the resolver does not use it", err)
	case len(filtered) == len(routers):
		t.Logf("VERDICT: _filter on name works for option codes (%d match(es) for routers, as in the full list)", len(filtered))
	default:
		t.Logf("VERDICT: _filter on name returned %d option codes for routers where the full list has %d; the resolver does not use it",
			len(filtered), len(routers))
	}

	id, resolveErr := client.OptionCodes.Resolve(ctx, "routers", "")
	switch len(routers) {
	case 0:
		t.Logf("VERDICT: no option code is named routers on this tenant, so optionName: routers stays Ready=False/DependencyNotReady (%v)", resolveErr)
	case 1:
		if resolveErr != nil {
			t.Fatalf("routers is defined once, in %s, but the resolver failed: %v", routers[0].OptionSpace, resolveErr)
		}
		if want := uddi.QualifyID(uddi.PrefixOptionCode, strv(routers[0].Id)); id != want {
			t.Fatalf("the resolver returned %q for routers, the list has %q", id, want)
		}
		t.Logf("VERDICT: routers resolves unambiguously to %s (code %d, option space %s)", id, routers[0].Code, routers[0].OptionSpace)
		if got, err := client.OptionCodes.Resolve(ctx, "routers", routers[0].OptionSpace); err != nil || got != id {
			t.Errorf("routers narrowed to its own option space %s resolved to %q, %v; want %q", routers[0].OptionSpace, got, err, id)
		}
	default:
		spaces := make([]string, 0, len(routers))
		for i := range routers {
			spaces = append(spaces, routers[i].OptionSpace)
		}
		sort.Strings(spaces)
		t.Logf("VERDICT: routers is defined in %d option spaces (%s), so optionName: routers needs an optionSpace on this tenant (resolver: %v)",
			len(routers), strings.Join(spaces, ", "), resolveErr)
		if resolveErr == nil {
			t.Errorf("routers is ambiguous but the resolver returned %q", id)
		}
	}
}

// idPrefix returns an id without its last path element: "dhcp/option_code" for
// "dhcp/option_code/<uuid>". An id with no slash has the empty prefix.
func idPrefix(id string) string {
	i := strings.LastIndex(id, "/")
	if i < 0 {
		return ""
	}
	return id[:i]
}

// describePrefixes renders a prefix tally as a sentence fragment.
func describePrefixes(what string, tally map[string]int, total int) string {
	prefixes := make([]string, 0, len(tally))
	for p := range tally {
		prefixes = append(prefixes, p)
	}
	sort.Strings(prefixes)
	parts := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		name := fmt.Sprintf("%q", p)
		if p == "" {
			name = "no prefix (a bare id)"
		}
		parts = append(parts, fmt.Sprintf("%s on %d", name, tally[p]))
	}
	return fmt.Sprintf("%s of the tenant's %d option codes carry %s", what, total, strings.Join(parts, ", "))
}

// defaultV4SpaceVerdict finds the option space that holds the most of the standard
// DHCPv4 option numbers, and says how many of them carry the names the operator's fake
// and the docs use.
func defaultV4SpaceVerdict(codes []ipam.OptionCode) string {
	type tally struct {
		total  int
		hits   int
		byName int
	}
	spaces := map[string]*tally{}
	for i := range codes {
		c := &codes[i]
		tl := spaces[c.OptionSpace]
		if tl == nil {
			tl = &tally{}
			spaces[c.OptionSpace] = tl
		}
		tl.total++
		for _, std := range standardV4Options {
			if c.Code == std.number {
				tl.hits++
				if c.Name == std.name {
					tl.byName++
				}
			}
		}
	}

	ids := make([]string, 0, len(spaces))
	for id := range spaces {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	var (
		best      string
		bestTally *tally
		ties      []string
	)
	for _, id := range ids {
		tl := spaces[id]
		switch {
		case tl.hits == 0:
			continue
		case bestTally == nil || tl.hits > bestTally.hits || (tl.hits == bestTally.hits && tl.byName > bestTally.byName):
			best, bestTally, ties = id, tl, nil
		case tl.hits == bestTally.hits && tl.byName == bestTally.byName:
			ties = append(ties, id)
		}
	}
	if bestTally == nil {
		return fmt.Sprintf("VERDICT: none of the tenant's %d option space(s) holds the standard DHCPv4 option numbers, so there is no default DHCPv4 option space to name",
			len(spaces))
	}
	tl := bestTally
	verdict := fmt.Sprintf("VERDICT: the default DHCPv4 option space id is %s: it holds %d of the %d standard DHCPv4 option numbers (%d under the names routers, domain-name-servers, domain-name, ntp-servers, domain-search) among %d option codes, out of %d option space(s)",
		best, tl.hits, len(standardV4Options), tl.byName, tl.total, len(spaces))
	if len(ties) > 0 {
		verdict += fmt.Sprintf("; %s hold(s) just as many, so this is a guess", strings.Join(ties, ", "))
	}
	return verdict
}

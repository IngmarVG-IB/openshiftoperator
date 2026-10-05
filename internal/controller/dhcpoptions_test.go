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
	"errors"
	"strings"
	"testing"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	uddisync "github.com/IngmarVG-IB/openshiftoperator/internal/sync"
	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

func TestOptionItemsRoundTrip(t *testing.T) {
	in := []uddiv1alpha1.DHCPOption{
		{Type: uddiv1alpha1.DHCPOptionTypeOption, OptionCode: "dhcp/option_code/aaa", OptionValue: "10.0.10.1"},
		{Type: uddiv1alpha1.DHCPOptionTypeGroup, Group: "dhcp/option_group/bbb"},
		{OptionCode: "dhcp/option_code/ccc", OptionValue: "example.com"}, // type defaults to option
	}

	items := toOptionItems(in, nil)
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	if got := *items[0].Type; got != "option" {
		t.Errorf("item 0 type = %q, want option", got)
	}
	if items[0].Group != nil {
		t.Errorf("item 0 should not carry a group")
	}
	if got := *items[1].Group; got != "dhcp/option_group/bbb" {
		t.Errorf("item 1 group = %q", got)
	}
	if items[1].OptionCode != nil || items[1].OptionValue != nil {
		t.Errorf("item 1 should not carry option code or value")
	}
	if got := *items[2].Type; got != "option" {
		t.Errorf("item 2 type = %q, want option (defaulted)", got)
	}

	if !sameOptionItems(items, in, nil) {
		t.Errorf("round trip should compare equal")
	}

	back := fromOptionItems(items)
	if len(back) != 3 || back[2].Type != uddiv1alpha1.DHCPOptionTypeOption {
		t.Errorf("fromOptionItems did not default the type: %+v", back)
	}

	// Order matters.
	swapped := []uddiv1alpha1.DHCPOption{in[1], in[0], in[2]}
	if sameOptionItems(items, swapped, nil) {
		t.Errorf("reordered options must not compare equal")
	}

	// A changed value is detected.
	changed := []uddiv1alpha1.DHCPOption{in[0], in[1], {OptionCode: "dhcp/option_code/ccc", OptionValue: "other"}}
	if sameOptionItems(items, changed, nil) {
		t.Errorf("changed option value must not compare equal")
	}

	// Empty spec clears options: non-nil empty slice for the patch, nil from the API.
	if got := toOptionItems(nil, nil); got == nil || len(got) != 0 {
		t.Errorf("toOptionItems(nil) should be an empty non-nil slice, got %#v", got)
	}
	if got := fromOptionItems(nil); got != nil {
		t.Errorf("fromOptionItems(nil) should be nil, got %#v", got)
	}
	if !sameOptionItems(nil, nil, nil) {
		t.Errorf("nil and nil should compare equal")
	}
}

// A name-based entry reaches Universal DDI, and is compared with what Universal DDI
// holds, by the option code id Resolve stored in deps, exactly like an entry that gave
// the id.
func TestOptionItemsUseResolvedIDForNames(t *testing.T) {
	routers := uddi.FakeOptionCodeID(3)
	in := []uddiv1alpha1.DHCPOption{
		{OptionName: "routers", OptionValue: "10.0.10.1"},
		{OptionCode: "dhcp/option_code/aaa", OptionValue: "1"},
		{Type: uddiv1alpha1.DHCPOptionTypeGroup, Group: "dhcp/option_group/bbb"},
		{OptionName: "routers", OptionSpace: uddi.FakeOptionSpaceDHCPv4, OptionValue: "10.0.10.2"},
	}
	deps := uddisync.Deps{
		optionCodeDepKey(0): routers,
		optionCodeDepKey(3): routers,
	}

	items := toOptionItems(in, deps)
	if got := *items[0].OptionCode; got != routers {
		t.Errorf("item 0 option code = %q, want the resolved %q", got, routers)
	}
	if got := *items[1].OptionCode; got != "dhcp/option_code/aaa" {
		t.Errorf("item 1 option code = %q, want the one given", got)
	}
	if got := *items[3].OptionCode; got != routers {
		t.Errorf("item 3 option code = %q, want the resolved %q", got, routers)
	}
	if !sameOptionItems(items, in, deps) {
		t.Errorf("options built from resolved names should compare equal to the spec")
	}

	// A different resolved id, as when the option code was recreated, is drift.
	other := uddisync.Deps{optionCodeDepKey(0): "dhcp/option_code/other", optionCodeDepKey(3): routers}
	if sameOptionItems(items, in, other) {
		t.Errorf("a name that now resolves to another option code must not compare equal")
	}
}

func TestOptionCodeDepKeysAreIndexedAndDistinct(t *testing.T) {
	if got := optionCodeDepKey(0); got != "optionCode/0" {
		t.Errorf("optionCodeDepKey(0) = %q, want optionCode/0", got)
	}
	// Distinct from the keys the other Deps users pick, such as "space" and "parent".
	for _, k := range []string{depSpace, depParent, depIPSpaceName, fixedAddressDepSpace, rangeDepSpace} {
		if strings.HasPrefix(k, optionCodeDepPrefix) {
			t.Errorf("Deps key %q collides with the option code prefix", k)
		}
	}
}

func TestResolveOptionCodes(t *testing.T) {
	ctx := context.Background()
	fake := uddi.NewFake()
	uc := fake.Client()

	t.Run("names resolve and groups, ids and empty lists are skipped", func(t *testing.T) {
		deps := uddisync.Deps{}
		opts := []uddiv1alpha1.DHCPOption{
			{OptionCode: "dhcp/option_code/aaa"},
			{OptionName: "routers"},
			{Type: uddiv1alpha1.DHCPOptionTypeGroup, Group: "dhcp/option_group/bbb"},
			{OptionName: "ntp-servers", OptionSpace: uddi.FakeOptionSpaceVendor},
		}
		if err := resolveOptionCodes(ctx, uc, opts, deps); err != nil {
			t.Fatalf("resolveOptionCodes: %v", err)
		}
		if got := deps[optionCodeDepKey(1)]; got != uddi.FakeOptionCodeID(3) {
			t.Errorf("routers resolved to %q, want %q", got, uddi.FakeOptionCodeID(3))
		}
		if got := deps[optionCodeDepKey(3)]; got != uddi.FakeOptionCodeID(200) {
			t.Errorf("vendor ntp-servers resolved to %q, want %q", got, uddi.FakeOptionCodeID(200))
		}
		if len(deps) != 2 {
			t.Errorf("deps = %v, want only the two named entries", deps)
		}
		if err := resolveOptionCodes(ctx, nil, nil, uddisync.Deps{}); err != nil {
			t.Errorf("no options must not need a client: %v", err)
		}
	})

	t.Run("an unknown name is a dependency that is not ready and names the option", func(t *testing.T) {
		err := resolveOptionCodes(ctx, uc, []uddiv1alpha1.DHCPOption{{OptionName: "no-such-option"}}, uddisync.Deps{})
		if !errors.Is(err, uddisync.ErrDependencyNotReady) {
			t.Fatalf("err = %v, want one wrapping ErrDependencyNotReady", err)
		}
		if !errors.Is(err, uddi.ErrOptionCodeNotFound) {
			t.Errorf("err = %v, want it to keep ErrOptionCodeNotFound", err)
		}
		if !strings.Contains(err.Error(), `"no-such-option"`) {
			t.Errorf("err = %q, want the option name in the message", err)
		}
	})

	t.Run("an unknown name in a given space names the space", func(t *testing.T) {
		space := uddi.FakeOptionSpaceVendor
		err := resolveOptionCodes(ctx, uc, []uddiv1alpha1.DHCPOption{{OptionName: "routers", OptionSpace: space}}, uddisync.Deps{})
		if !errors.Is(err, uddisync.ErrDependencyNotReady) {
			t.Fatalf("err = %v, want one wrapping ErrDependencyNotReady", err)
		}
		if !strings.Contains(err.Error(), `"routers"`) || !strings.Contains(err.Error(), space) {
			t.Errorf("err = %q, want the option name and the option space in the message", err)
		}
	})

	t.Run("an ambiguous name names the spaces", func(t *testing.T) {
		err := resolveOptionCodes(ctx, uc, []uddiv1alpha1.DHCPOption{{OptionName: "ntp-servers"}}, uddisync.Deps{})
		if !errors.Is(err, uddisync.ErrDependencyNotReady) || !errors.Is(err, uddi.ErrOptionCodeAmbiguous) {
			t.Fatalf("err = %v, want a dependency that is not ready and ambiguous", err)
		}
		for _, want := range []string{`"ntp-servers"`, uddi.FakeOptionSpaceDHCPv4, uddi.FakeOptionSpaceVendor} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %q, want %q in the message", err, want)
			}
		}
	})

	t.Run("a failure to list option codes is not a missing dependency", func(t *testing.T) {
		broken := uddi.NewFake()
		broken.PushStatus("ListOptionCodes", 500, "boom")
		err := resolveOptionCodes(ctx, broken.Client(), []uddiv1alpha1.DHCPOption{{OptionName: "routers"}}, uddisync.Deps{})
		if err == nil || errors.Is(err, uddisync.ErrDependencyNotReady) {
			t.Fatalf("err = %v, want a plain API error", err)
		}
		var apiErr *uddi.APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != 500 {
			t.Errorf("err = %v, want the 500 to stay visible to the caller", err)
		}
	})

	t.Run("a client without a resolver fails instead of panicking", func(t *testing.T) {
		bare := &uddi.Client{}
		err := resolveOptionCodes(ctx, bare, []uddiv1alpha1.DHCPOption{{OptionName: "routers"}}, uddisync.Deps{})
		if err == nil || errors.Is(err, uddisync.ErrDependencyNotReady) {
			t.Fatalf("err = %v, want a plain error", err)
		}
		if err := resolveOptionCodes(ctx, nil, []uddiv1alpha1.DHCPOption{{OptionName: "routers"}}, uddisync.Deps{}); err == nil {
			t.Fatalf("a nil client must fail too")
		}
	})
}

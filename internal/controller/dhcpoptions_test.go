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
	"testing"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

func TestOptionItemsRoundTrip(t *testing.T) {
	in := []uddiv1alpha1.DHCPOption{
		{Type: uddiv1alpha1.DHCPOptionTypeOption, OptionCode: "dhcp/option_code/aaa", OptionValue: "10.0.10.1"},
		{Type: uddiv1alpha1.DHCPOptionTypeGroup, Group: "dhcp/option_group/bbb"},
		{OptionCode: "dhcp/option_code/ccc", OptionValue: "example.com"}, // type defaults to option
	}

	items := toOptionItems(in)
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

	if !sameOptionItems(items, in) {
		t.Errorf("round trip should compare equal")
	}

	back := fromOptionItems(items)
	if len(back) != 3 || back[2].Type != uddiv1alpha1.DHCPOptionTypeOption {
		t.Errorf("fromOptionItems did not default the type: %+v", back)
	}

	// Order matters.
	swapped := []uddiv1alpha1.DHCPOption{in[1], in[0], in[2]}
	if sameOptionItems(items, swapped) {
		t.Errorf("reordered options must not compare equal")
	}

	// A changed value is detected.
	changed := []uddiv1alpha1.DHCPOption{in[0], in[1], {OptionCode: "dhcp/option_code/ccc", OptionValue: "other"}}
	if sameOptionItems(items, changed) {
		t.Errorf("changed option value must not compare equal")
	}

	// Empty spec clears options: non-nil empty slice for the patch, nil from the API.
	if got := toOptionItems(nil); got == nil || len(got) != 0 {
		t.Errorf("toOptionItems(nil) should be an empty non-nil slice, got %#v", got)
	}
	if got := fromOptionItems(nil); got != nil {
		t.Errorf("fromOptionItems(nil) should be nil, got %#v", got)
	}
	if !sameOptionItems(nil, nil) {
		t.Errorf("nil and nil should compare equal")
	}
}

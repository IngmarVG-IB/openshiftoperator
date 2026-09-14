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
	"github.com/infobloxopen/universal-ddi-go-client/ipam"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

// toOptionItems converts CR dhcpOptions into the SDK's ipam.OptionItem list. It is shared
// by the AddressBlock, Subnet, Range and FixedAddress adapters. A nil or empty input
// yields an empty (non-nil) slice so a patch can clear the options in Universal DDI.
func toOptionItems(in []uddiv1alpha1.DHCPOption) []ipam.OptionItem {
	out := make([]ipam.OptionItem, 0, len(in))
	for i := range in {
		o := in[i]
		item := ipam.OptionItem{}
		typ := string(o.Type)
		if typ == "" {
			typ = string(uddiv1alpha1.DHCPOptionTypeOption)
		}
		item.Type = &typ
		switch uddiv1alpha1.DHCPOptionType(typ) {
		case uddiv1alpha1.DHCPOptionTypeGroup:
			group := o.Group
			item.Group = &group
		default:
			code := o.OptionCode
			value := o.OptionValue
			item.OptionCode = &code
			item.OptionValue = &value
		}
		out = append(out, item)
	}
	return out
}

// fromOptionItems converts the SDK's ipam.OptionItem list into CR dhcpOptions, for
// status reporting and for comparing the observed state with the spec.
func fromOptionItems(in []ipam.OptionItem) []uddiv1alpha1.DHCPOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]uddiv1alpha1.DHCPOption, 0, len(in))
	for i := range in {
		item := in[i]
		o := uddiv1alpha1.DHCPOption{
			Type:        uddiv1alpha1.DHCPOptionType(strPtr(item.Type)),
			OptionCode:  strPtr(item.OptionCode),
			OptionValue: strPtr(item.OptionValue),
			Group:       strPtr(item.Group),
		}
		if o.Type == "" {
			o.Type = uddiv1alpha1.DHCPOptionTypeOption
		}
		out = append(out, o)
	}
	return out
}

// sameOptionItems reports whether the observed options match the desired ones. Order is
// significant: DHCP option order is meaningful and the API preserves it.
func sameOptionItems(current []ipam.OptionItem, desired []uddiv1alpha1.DHCPOption) bool {
	have := fromOptionItems(current)
	if len(have) != len(desired) {
		return false
	}
	for i := range desired {
		d := desired[i]
		if d.Type == "" {
			d.Type = uddiv1alpha1.DHCPOptionTypeOption
		}
		if have[i].Type != d.Type {
			return false
		}
		switch d.Type {
		case uddiv1alpha1.DHCPOptionTypeGroup:
			if have[i].Group != d.Group {
				return false
			}
		default:
			if have[i].OptionCode != d.OptionCode || have[i].OptionValue != d.OptionValue {
				return false
			}
		}
	}
	return true
}

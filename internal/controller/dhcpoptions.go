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
	"fmt"
	"strconv"

	"github.com/infobloxopen/universal-ddi-go-client/ipam"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
	uddisync "github.com/IngmarVG-IB/openshiftoperator/internal/sync"
	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// optionCodeDepPrefix starts the Deps key under which Resolve keeps the option code id of
// a dhcpOptions entry that names its code with optionName. The key is
// optionCodeDepKey(i), "optionCode/<i>", where i is the entry's index in spec.dhcpOptions;
// entries that set optionCode or group have no key. The index keeps two entries that name
// the same option, or that differ only in optionSpace, apart without parsing names.
const optionCodeDepPrefix = "optionCode/"

// optionCodeDepKey returns the Deps key of the i-th dhcpOptions entry.
func optionCodeDepKey(i int) string { return optionCodeDepPrefix + strconv.Itoa(i) }

// isOptionGroup reports whether the entry is an option group rather than a single option.
func isOptionGroup(o *uddiv1alpha1.DHCPOption) bool {
	return o.Type == uddiv1alpha1.DHCPOptionTypeGroup
}

// optionCodeFor returns the option code id the i-th entry stands for: the resolved id for
// an entry that names its code, the optionCode as written otherwise.
func optionCodeFor(i int, o *uddiv1alpha1.DHCPOption, deps uddisync.Deps) string {
	if o.OptionName != "" {
		return deps[optionCodeDepKey(i)]
	}
	return o.OptionCode
}

// resolveOptionCodes looks up every optionName in opts through uc and stores the option
// code ids in deps under optionCodeDepKey. It is the DHCP half of Adapter.Resolve for the
// five kinds that carry dhcpOptions.
//
// A name that no option space defines, or that several do while optionSpace is empty,
// returns an error wrapping ErrDependencyNotReady, so the object shows
// Ready=False/DependencyNotReady with the option and space in the message and is retried.
// A failure to list the option codes at all is returned as it is.
func resolveOptionCodes(ctx context.Context, uc *uddi.Client, opts []uddiv1alpha1.DHCPOption, deps uddisync.Deps) error {
	for i := range opts {
		o := &opts[i]
		if o.OptionName == "" || isOptionGroup(o) {
			continue
		}
		if uc == nil || uc.OptionCodes == nil {
			return fmt.Errorf("dhcpOptions[%d]: optionName %q cannot be resolved: the Universal DDI client has no option code resolver", i, o.OptionName)
		}
		id, err := uc.OptionCodes.Resolve(ctx, o.OptionName, o.OptionSpace)
		if err != nil {
			if errors.Is(err, uddi.ErrOptionCodeNotFound) || errors.Is(err, uddi.ErrOptionCodeAmbiguous) {
				return fmt.Errorf("%w: dhcpOptions[%d]: %w", uddisync.ErrDependencyNotReady, i, err)
			}
			return err
		}
		deps[optionCodeDepKey(i)] = id
	}
	return nil
}

// toOptionItems converts CR dhcpOptions into the SDK's ipam.OptionItem list. It is shared
// by the IPSpace, AddressBlock, Subnet, Range and FixedAddress adapters. deps holds the
// option code ids resolveOptionCodes found, so an entry that names its code reaches
// Universal DDI with the same option code id an entry that gives the id does. A nil or
// empty input yields an empty (non-nil) slice so a patch can clear the options in
// Universal DDI.
func toOptionItems(in []uddiv1alpha1.DHCPOption, deps uddisync.Deps) []ipam.OptionItem {
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
			code := optionCodeFor(i, &o, deps)
			value := o.OptionValue
			item.OptionCode = &code
			item.OptionValue = &value
		}
		out = append(out, item)
	}
	return out
}

// fromOptionItems converts the SDK's ipam.OptionItem list into CR dhcpOptions, for
// status reporting and for comparing the observed state with the spec. Universal DDI
// only knows option code ids, so the result never carries optionName or optionSpace.
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

// sameOptionItems reports whether the observed options match the desired ones, comparing
// an entry that names its code by the option code id deps resolved for it. Order is
// significant: DHCP option order is meaningful and the API preserves it.
func sameOptionItems(current []ipam.OptionItem, desired []uddiv1alpha1.DHCPOption, deps uddisync.Deps) bool {
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
			if have[i].OptionCode != optionCodeFor(i, &d, deps) || have[i].OptionValue != d.OptionValue {
				return false
			}
		}
	}
	return true
}

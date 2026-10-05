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

package ipalloc

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	uddiv1alpha1 "github.com/IngmarVG-IB/openshiftoperator/api/v1alpha1"
)

// maxNameLength is the Kubernetes object name length limit AddressName must respect.
const maxNameLength = 253

// nameHashLength is how many hex characters of the sha256 digest AddressName keeps
// when the plain "<policy>-<service>" name would be too long.
const nameHashLength = 10

// maxLabelPrefix is how much of a target name a shortened LabelTargetName value keeps
// before "-<hash>", so the value stays within the 63 character label value limit.
const maxLabelPrefix = 63 - 1 - nameHashLength

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:nameHashLength]
}

// AddressName derives the deterministic name of the Address generated for slot 0 of a
// Service: "<policy>-<service>", the milestone 3 name. When that would exceed the
// Kubernetes 253 character name limit, the service part is replaced with the first 10
// hex characters of sha256(service) so the name stays short and deterministic.
func AddressName(policy, service string) string {
	name := policy + "-" + service
	if len(name) <= maxNameLength {
		return name
	}
	return policy + "-" + shortHash(service)
}

// SlotAddressName derives the name of the Address for one slot of a claim:
// "<policy>-<service>" for slot 0 of a Service (AddressName, so upgrading never renames
// an allocated Address), "<policy>-<service>-<slot>" for its other slots, and
// "<policy>-egressip-<name>-<slot>" for an EgressIP. Above 253 characters the target name
// is replaced with the first 10 hex characters of its sha256, as AddressName does.
func SlotAddressName(policy string, target TargetRef, slot int) string {
	suffix := "-" + strconv.Itoa(slot)
	if target.Kind == TargetEgressIP {
		name := policy + "-egressip-" + target.Name + suffix
		if len(name) <= maxNameLength {
			return name
		}
		return policy + "-egressip-" + shortHash(target.Name) + suffix
	}
	if slot == 0 {
		return AddressName(policy, target.Name)
	}
	name := policy + "-" + target.Name + suffix
	if len(name) <= maxNameLength {
		return name
	}
	return policy + "-" + shortHash(target.Name) + suffix
}

// TargetNameLabelValue is the LabelTargetName value for a target name: the name itself
// when it is a valid label value (every Service name is), otherwise its first 52
// characters plus "-<10 hex of sha256(name)>". AnnotationTargetName then carries the full
// name.
func TargetNameLabelValue(name string) string {
	if len(validation.IsValidLabelValue(name)) == 0 {
		return name
	}
	prefix := name
	if len(prefix) > maxLabelPrefix {
		prefix = prefix[:maxLabelPrefix]
	}
	prefix = strings.TrimRight(prefix, "-_.")
	if prefix == "" {
		return shortHash(name)
	}
	return prefix + "-" + shortHash(name)
}

// DefaultHostname is the hostname registered on a Service's Addresses when it does not
// carry the hostname annotation: "<name>.<namespace>".
func DefaultHostname(ref TargetRef) string {
	return ref.Name + "." + ref.Namespace
}

// poolRefOf derives a PoolRef from an AllocateFrom block; the zero PoolRef when none of
// its refs is set, which the CRD's CEL validation does not allow in practice.
func poolRefOf(af uddiv1alpha1.AllocateFrom) PoolRef {
	switch {
	case af.SubnetRef != nil:
		return PoolRef{Kind: PoolSubnet, Name: af.SubnetRef.Name}
	case af.RangeRef != nil:
		return PoolRef{Kind: PoolRange, Name: af.RangeRef.Name}
	case af.AddressBlockRef != nil:
		return PoolRef{Kind: PoolAddressBlock, Name: af.AddressBlockRef.Name}
	default:
		return PoolRef{}
	}
}

// PolicyPool derives the policy's own pool reference from spec.allocateFrom. It
// returns the zero PoolRef if none of subnetRef, rangeRef or addressBlockRef is set,
// which the CRD's CEL validation does not allow in practice.
func PolicyPool(p *uddiv1alpha1.IPAllocationPolicy) PoolRef {
	return poolRefOf(p.Spec.AllocateFrom)
}

// PolicyPoolIPv6 derives the pool reference from spec.allocateFromIPv6; ok is false when
// the field is unset.
func PolicyPoolIPv6(p *uddiv1alpha1.IPAllocationPolicy) (PoolRef, bool) {
	if p.Spec.AllocateFromIPv6 == nil {
		return PoolRef{}, false
	}
	ref := poolRefOf(*p.Spec.AllocateFromIPv6)
	return ref, ref != PoolRef{}
}

// TargetEnabled reports whether spec.targets lists kind. An empty spec.targets is the
// default, Service only.
func TargetEnabled(p *uddiv1alpha1.IPAllocationPolicy, kind TargetKind) bool {
	if len(p.Spec.Targets) == 0 {
		return kind == TargetService
	}
	for _, t := range p.Spec.Targets {
		if TargetKind(t) == kind {
			return true
		}
	}
	return false
}

// PoolRefs lists every pool the planner may consult for in, sorted and without
// duplicates: allocateFrom, allocateFromIPv6, every claim's allocate-from override and
// the pools existing Addresses were allocated from (whose family places an Address that
// has no address yet). The controller resolves each into PlanInput.Pools.
func PoolRefs(p *uddiv1alpha1.IPAllocationPolicy, claims []Claim, existing []AddressState) []PoolRef {
	seen := map[PoolRef]bool{PolicyPool(p): true}
	if ref, ok := PolicyPoolIPv6(p); ok {
		seen[ref] = true
	}
	for _, c := range claims {
		if c.Pool != nil {
			seen[*c.Pool] = true
		}
	}
	for _, a := range existing {
		if a.Pool != nil {
			seen[*a.Pool] = true
		}
	}
	out := make([]PoolRef, 0, len(seen))
	for ref := range seen {
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// WantsAddress reports whether c should have addresses allocated, ignoring selector
// matching and eligibility: those are the controller's and Compute's job respectively,
// since claims handed to the planner are already selected. An explicit opt-out always
// wins. An EgressIP must always opt in; a Service must opt in under requireAnnotation
// (the spec default), and without it every Service wants an address unless it opted out.
func WantsAddress(p *uddiv1alpha1.IPAllocationPolicy, c Claim) bool {
	if c.OptIn != nil && !*c.OptIn {
		return false
	}
	requireAnnotation := p.Spec.RequireAnnotation == nil || *p.Spec.RequireAnnotation
	if requireAnnotation || c.Target.Kind == TargetEgressIP {
		return c.OptIn != nil && *c.OptIn
	}
	return true
}

// PlanInput is everything Compute needs: the policy (for its metadata, connectionRef,
// targets, allocateFrom, allocateFromIPv6, assignVia, tags and addressLifecycle), the
// claims already selected and annotation-parsed, the pool CRs listed by PoolRefs, and
// the Address CRs this policy currently owns.
type PlanInput struct {
	Policy   *uddiv1alpha1.IPAllocationPolicy
	Claims   []Claim
	Pools    map[PoolRef]PoolState
	Existing []AddressState
}

// Compute turns claims, pool state and existing Addresses into the Addresses an
// IPAllocationPolicy should own, the targets that need their assignment field written
// or cleared, the Addresses to release, and the claims skipped along with why. See
// DESIGN.md section 4.7, pipeline steps 3 to 7 and the milestone 7 extensions.
//
// Releasing is the only thing that can move an address, so it happens only for the
// reasons DESIGN.md names: the target disappeared from the input, it opted out
// (explicitly, or implicitly under requireAnnotation), it is no longer a LoadBalancer,
// or a claim that was planned in full no longer wants a slot (a lowered EgressIP count,
// a Service back to one IP family, LoadBalancerIP mode). Every skip a claim can hit
// after it is known to want addresses (InvalidAnnotation, PoolNotReady,
// NoPoolForFamily, DualStackUnsupported, ManuallyAssigned, NameConflict) freezes it
// instead: its existing Addresses are kept and not re-applied. Addresses of a target
// kind missing from spec.targets, and Addresses whose labels name no target, are never
// touched.
func Compute(in PlanInput) Plan {
	p := newPlanner(in)
	claims := make([]Claim, 0, len(in.Claims))
	for _, c := range in.Claims {
		if TargetEnabled(in.Policy, c.Target.Kind) {
			claims = append(claims, c)
		}
	}
	sort.SliceStable(claims, func(i, j int) bool { return lessTarget(claims[i].Target, claims[j].Target) })
	// Slot 0 of a Service keeps the milestone 3 name, so it is reserved up front: another
	// claim's slot planned in the same pass can never take it.
	for _, c := range claims {
		if c.Target.Kind == TargetService && c.Invalid == "" && c.Eligible && WantsAddress(in.Policy, c) {
			name := SlotAddressName(in.Policy.Name, c.Target, 0)
			if _, taken := p.reserved[name]; !taken {
				p.reserved[name] = keyOf(c.Target)
			}
		}
	}
	for _, c := range claims {
		p.claim(c)
	}
	p.releases()
	p.sortOutputs()
	return p.plan
}

// targetKey identifies a target independently of its UID, which existing Addresses do
// not record.
type targetKey struct {
	kind TargetKind
	name string
}

func keyOf(t TargetRef) targetKey { return targetKey{kind: t.Kind, name: t.Name} }

// existingSlot is one existing Address attributed to a claim slot.
type existingSlot struct {
	slot  int
	state AddressState
}

// keepState is what the release pass may do with a target's existing Addresses: keep
// them all (a frozen claim), keep only the named ones (a claim planned in full), or,
// when the target has no keepState, release them all.
type keepState struct {
	all   bool
	names map[string]bool
}

// plannedSlot is one slot of a claim with its pool resolved and its number chosen.
type plannedSlot struct {
	index   int
	family  IPFamily
	address string
	pool    PoolRef
	state   PoolState
}

type planner struct {
	in         PlanInput
	policy     *uddiv1alpha1.IPAllocationPolicy
	policyPool PoolRef
	ipv6Pool   PoolRef
	hasIPv6    bool
	lbIPMode   bool

	existing map[targetKey][]existingSlot
	byName   map[string]AddressState
	seen     map[targetKey]bool
	refs     map[targetKey]TargetRef
	keep     map[targetKey]keepState
	claimed  map[string]bool
	reserved map[string]targetKey

	plan Plan
}

func newPlanner(in PlanInput) *planner {
	p := &planner{
		in:         in,
		policy:     in.Policy,
		policyPool: PolicyPool(in.Policy),
		lbIPMode:   in.Policy.Spec.AssignVia.Mode == uddiv1alpha1.AssignModeLoadBalancerIP,
		existing:   make(map[targetKey][]existingSlot, len(in.Existing)),
		byName:     make(map[string]AddressState, len(in.Existing)),
		seen:       make(map[targetKey]bool, len(in.Claims)),
		refs:       make(map[targetKey]TargetRef, len(in.Claims)),
		keep:       make(map[targetKey]keepState, len(in.Claims)),
		claimed:    make(map[string]bool),
		reserved:   make(map[string]targetKey),
	}
	p.ipv6Pool, p.hasIPv6 = PolicyPoolIPv6(in.Policy)
	for _, a := range in.Existing {
		p.byName[a.Name] = a
		if ref, slot, ok := a.Target(in.Policy.Namespace); ok {
			k := keyOf(ref)
			p.existing[k] = append(p.existing[k], existingSlot{slot: slot, state: a})
		}
	}
	for k := range p.existing {
		sort.Slice(p.existing[k], func(i, j int) bool {
			a, b := p.existing[k][i], p.existing[k][j]
			if a.slot != b.slot {
				return a.slot < b.slot
			}
			return a.state.Name < b.state.Name
		})
	}
	return p
}

func (p *planner) skip(t TargetRef, reason SkipReason, format string, args ...any) {
	p.plan.Skips = append(p.plan.Skips, Skip{Target: t, Reason: reason, Message: fmt.Sprintf(format, args...)})
}

// claim plans one claim of an enabled target kind.
func (p *planner) claim(c Claim) {
	k := keyOf(c.Target)
	if p.seen[k] {
		return
	}
	p.seen[k] = true
	p.refs[k] = c.Target

	if c.Invalid != "" {
		p.skip(c.Target, SkipInvalidAnnotation, "%s", c.Invalid)
		p.keep[k] = keepState{all: true}
		return
	}
	if !WantsAddress(p.policy, c) {
		return
	}
	if !c.Eligible {
		p.skip(c.Target, SkipNotLoadBalancer, "Service is not of type LoadBalancer")
		return
	}
	// From here on the claim wants addresses: hold every existing one until the slots
	// are known.
	p.keep[k] = keepState{all: true}

	if c.Target.Kind == TargetService && p.lbIPMode && c.FamilyPolicy == RequireDualStack {
		p.skip(c.Target, SkipDualStackUnsupported,
			"assignVia.mode LoadBalancerIP carries one address but the Service is RequireDualStack")
		return
	}

	existing := p.existing[k]
	slots, ok := p.resolveSlots(c, existing)
	if !ok {
		return
	}

	held := make(map[string]bool, len(existing))
	for _, e := range existing {
		if e.state.Address != "" {
			held[e.state.Address] = true
		}
	}
	for _, v := range c.CurrentAssignment {
		if !held[v] {
			p.skip(c.Target, SkipManuallyAssigned, "%s is already %s",
				p.assignField(c.Target.Kind), strings.Join(c.CurrentAssignment, ","))
			return
		}
	}

	desired, ok := p.desired(c, p.place(c, slots, existing), existing)
	if !ok {
		return
	}
	names := make(map[string]bool, len(desired))
	for _, d := range desired {
		names[d.Name] = true
		p.claimed[d.Name] = true
	}
	p.keep[k] = keepState{names: names}
	p.plan.Ensure = append(p.plan.Ensure, desired...)
	p.plan.Allocated++
	p.assign(c, desired)
}

// resolveSlots picks a pool for every requested slot and applies the family rules: a
// pool that is missing or not ready is PoolNotReady; a family without a pool is dropped
// under PreferDualStack (unless the claim already holds an address of that family) and
// is NoPoolForFamily otherwise. LoadBalancerIP mode keeps one slot per Service. ok is
// false when the claim was skipped.
func (p *planner) resolveSlots(c Claim, existing []existingSlot) ([]plannedSlot, bool) {
	reqs := c.Slots
	if len(reqs) == 0 {
		reqs = []SlotRequest{{}}
	}

	if c.Pool != nil && reqs[0].Family != "" {
		st, found := p.in.Pools[*c.Pool]
		if found && st.Ready && st.Family != "" && !requestsFamily(reqs, st.Family) {
			p.skip(c.Target, SkipNoPoolForFamily, "allocate-from pool %s is %s but the Service wants %s",
				c.Pool.String(), st.Family, familyList(reqs))
			return nil, false
		}
	}

	slots := make([]plannedSlot, 0, len(reqs))
	for _, r := range reqs {
		ref, st, res := p.poolFor(c.Pool, r.Family)
		switch res {
		case poolNotReady:
			p.skip(c.Target, SkipPoolNotReady, "pool %s has no status.id yet", ref.String())
			return nil, false
		case poolNone:
			if c.FamilyPolicy == PreferDualStack && !p.holdsFamily(existing, r.Family) {
				continue
			}
			p.skip(c.Target, SkipNoPoolForFamily, "%s", p.noPoolMessage(r.Family))
			return nil, false
		}
		slots = append(slots, plannedSlot{family: r.Family, address: r.Address, pool: ref, state: st})
	}
	if len(slots) == 0 {
		p.skip(c.Target, SkipNoPoolForFamily, "no pool for any of %s: %s", familyList(reqs), p.noPoolMessage(""))
		return nil, false
	}

	if c.Target.Kind == TargetService && p.lbIPMode && len(slots) > 1 {
		// One address only: keep the family the Service already holds, so switching the
		// mode or adding a pool never trades an allocated address for another.
		keep := 0
		for _, e := range existing {
			f := p.familyOf(e.state)
			if i := indexOfFamily(slots, f); f != "" && i >= 0 {
				keep = i
				break
			}
		}
		slots = slots[keep : keep+1]
	}
	return slots, true
}

type poolResult int

const (
	poolFound poolResult = iota
	poolNotReady
	poolNone
)

// lookup checks one candidate pool for family. A pool that is missing or not ready is
// poolNotReady, since its family cannot be trusted yet. A ready pool serves family when
// family is empty (any), the families match, or its family is unknown and acceptUnknown.
func (p *planner) lookup(ref PoolRef, family IPFamily, acceptUnknown bool) (PoolRef, PoolState, poolResult) {
	st, found := p.in.Pools[ref]
	if !found || !st.Ready {
		return ref, st, poolNotReady
	}
	if family == "" || st.Family == family || (st.Family == "" && acceptUnknown) {
		return ref, st, poolFound
	}
	return ref, st, poolNone
}

// poolFor picks the pool a slot of family draws from. A family-agnostic slot uses the
// allocate-from override or allocateFrom. Otherwise an override serving family wins;
// IPv4 then uses allocateFrom, and IPv6 uses allocateFromIPv6, or allocateFrom when that
// pool is IPv6.
func (p *planner) poolFor(override *PoolRef, family IPFamily) (PoolRef, PoolState, poolResult) {
	if family == "" {
		ref := p.policyPool
		if override != nil {
			ref = *override
		}
		return p.lookup(ref, "", true)
	}
	if override != nil {
		if ref, st, res := p.lookup(*override, family, true); res != poolNone {
			return ref, st, res
		}
	}
	switch family {
	case IPv4:
		return p.lookup(p.policyPool, IPv4, true)
	case IPv6:
		if p.hasIPv6 {
			if ref, st, res := p.lookup(p.ipv6Pool, IPv6, true); res != poolNone {
				return ref, st, res
			}
		}
		return p.lookup(p.policyPool, IPv6, false)
	}
	return PoolRef{}, PoolState{}, poolNone
}

func (p *planner) noPoolMessage(family IPFamily) string {
	switch family {
	case IPv4:
		return fmt.Sprintf("no IPv4 pool: allocateFrom %s is not IPv4", p.policyPool.String())
	case IPv6:
		if p.hasIPv6 {
			return fmt.Sprintf("no IPv6 pool: allocateFromIPv6 %s is not IPv6 and allocateFrom %s is not IPv6",
				p.ipv6Pool.String(), p.policyPool.String())
		}
		return fmt.Sprintf("no IPv6 pool: allocateFrom %s is not IPv6 and allocateFromIPv6 is unset", p.policyPool.String())
	}
	return "set allocateFrom and allocateFromIPv6 to pools of the wanted families"
}

// familyOf is an existing Address's IP family: from its address, else from the pool it
// is being allocated from; empty when unknown.
func (p *planner) familyOf(a AddressState) IPFamily {
	if a.Family != "" {
		return a.Family
	}
	if a.Pool != nil {
		return p.in.Pools[*a.Pool].Family
	}
	return ""
}

func (p *planner) holdsFamily(existing []existingSlot, family IPFamily) bool {
	if family == "" {
		return false
	}
	for _, e := range existing {
		if p.familyOf(e.state) == family {
			return true
		}
	}
	return false
}

// place numbers the slots. Family-agnostic slots (an EgressIP's, a Service without
// spec.ipFamilies) are numbered in request order. A Service's family slots keep the
// number of the existing Address of the same family, so a slot never changes family;
// the remaining families take the lowest free numbers in request order, skipping
// numbers still held by an Address of another family.
func (p *planner) place(c Claim, slots []plannedSlot, existing []existingSlot) []plannedSlot {
	if c.Target.Kind == TargetEgressIP || slots[0].family == "" {
		for i := range slots {
			slots[i].index = i
		}
		return slots
	}

	occupied := make(map[int]IPFamily, len(existing))
	for _, e := range existing {
		if f, ok := occupied[e.slot]; ok && f != "" {
			continue
		}
		occupied[e.slot] = p.familyOf(e.state)
	}
	used := make(map[int]bool, len(slots))
	for i := range slots {
		slots[i].index = -1
	}
	for _, e := range existing {
		f := occupied[e.slot]
		if f == "" || used[e.slot] {
			continue
		}
		for i := range slots {
			if slots[i].index < 0 && slots[i].family == f {
				slots[i].index = e.slot
				used[e.slot] = true
				break
			}
		}
	}
	next := 0
	for i := range slots {
		if slots[i].index >= 0 {
			continue
		}
		for {
			f, occ := occupied[next]
			if !used[next] && (!occ || f == "" || f == slots[i].family) {
				break
			}
			next++
		}
		slots[i].index = next
		used[next] = true
		next++
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i].index < slots[j].index })
	return slots
}

// desired builds the DesiredAddress of every placed slot. A slot that already has an
// Address keeps that Address's name; a new slot gets SlotAddressName. ok is false, with a
// NameConflict skip, when a name already belongs to another target's slot, was taken by
// another claim earlier in this plan, or is the reserved slot 0 name of another Service.
func (p *planner) desired(c Claim, slots []plannedSlot, existing []existingSlot) ([]DesiredAddress, bool) {
	out := make([]DesiredAddress, 0, len(slots))
	for _, s := range slots {
		derived := SlotAddressName(p.policy.Name, c.Target, s.index)
		name := existingName(existing, s.index, derived)
		if name == "" {
			name = derived
		}
		if msg := p.nameConflict(c, s.index, name); msg != "" {
			p.skip(c.Target, SkipNameConflict, "%s", msg)
			return nil, false
		}
		out = append(out, p.buildDesiredAddress(c, s, name))
	}
	return out, true
}

// nameConflict explains why slot of c may not use name; empty when it may.
func (p *planner) nameConflict(c Claim, slot int, name string) string {
	if a, found := p.byName[name]; found {
		ref, s, ok := a.Target(p.policy.Namespace)
		if ok && keyOf(ref) == keyOf(c.Target) && s == slot {
			return ""
		}
		owner := "an Address without target labels"
		if ok {
			owner = fmt.Sprintf("slot %d of %s", s, ref.String())
		}
		return fmt.Sprintf("Address %s for slot %d already belongs to %s", name, slot, owner)
	}
	if p.claimed[name] {
		return fmt.Sprintf("Address name %s is already planned for another target", name)
	}
	if owner, ok := p.reserved[name]; ok && owner != keyOf(c.Target) {
		return fmt.Sprintf("Address name %s for slot %d is the slot 0 name of Service %s", name, slot, owner.name)
	}
	return ""
}

// existingName returns the name of the existing Address for slot, preferring derived
// when several claim the same slot; empty when the slot has none.
func existingName(existing []existingSlot, slot int, derived string) string {
	name := ""
	for _, e := range existing {
		if e.slot != slot {
			continue
		}
		if e.state.Name == derived {
			return derived
		}
		if name == "" {
			name = e.state.Name
		}
	}
	return name
}

// buildDesiredAddress assembles the DesiredAddress for one placed slot of c.
func (p *planner) buildDesiredAddress(c Claim, s plannedSlot, name string) DesiredAddress {
	pol := p.policy
	tags := make(map[string]string, len(pol.Spec.Tags)+1)
	for k, val := range pol.Spec.Tags {
		tags[k] = val
	}

	lbl := map[string]string{
		LabelPolicy:     pol.Name,
		LabelTargetKind: string(c.Target.Kind),
		LabelTargetName: TargetNameLabelValue(c.Target.Name),
		LabelSlot:       strconv.Itoa(s.index),
	}
	var ann map[string]string
	if lbl[LabelTargetName] != c.Target.Name {
		ann = map[string]string{AnnotationTargetName: c.Target.Name}
	}

	var comment string
	switch c.Target.Kind {
	case TargetEgressIP:
		tags[TagSource] = fmt.Sprintf("EgressIP/%s", c.Target.Name)
		comment = fmt.Sprintf("Allocated by IPAllocationPolicy %s/%s for EgressIP %s slot %d",
			pol.Namespace, pol.Name, c.Target.Name, s.index)
	default:
		lbl[LabelService] = c.Target.Name
		tags[TagSource] = fmt.Sprintf("Service/%s/%s", c.Target.Namespace, c.Target.Name)
		comment = fmt.Sprintf("Allocated by IPAllocationPolicy %s/%s for Service %s",
			pol.Namespace, pol.Name, c.Target.Name)
		if s.index > 0 {
			comment += fmt.Sprintf(" slot %d", s.index)
		}
	}

	spec := uddiv1alpha1.AddressSpec{
		CommonSpec: uddiv1alpha1.CommonSpec{
			ConnectionRef: pol.Spec.ConnectionRef,
			Tags:          tags,
			Lifecycle:     pol.Spec.AddressLifecycle,
		},
		SpaceRef: s.state.SpaceRef,
		Comment:  comment,
	}
	if c.Hostname != "" {
		spec.Names = []uddiv1alpha1.AddressName{{Name: c.Hostname, Type: "user"}}
	}
	if s.address != "" {
		spec.Address = s.address
	} else {
		spec.NextAvailableFrom = nextAvailableFromFor(s.pool)
	}

	return DesiredAddress{
		Name:        name,
		Target:      c.Target,
		Slot:        s.index,
		Family:      s.family,
		Labels:      lbl,
		Annotations: ann,
		Spec:        spec,
	}
}

// assign plans the assignment of a claim planned in full: written only once every slot
// is Ready, as the slot addresses in slot order; counted as assigned when the field
// already holds exactly that. A slot whose Address exists but is not Ready is
// AddressPending, with that Address's Ready reason and message.
func (p *planner) assign(c Claim, desired []DesiredAddress) {
	values := make([]string, 0, len(desired))
	allReady := true
	pendingMsg := ""
	for _, d := range desired {
		a, found := p.byName[d.Name]
		if !found {
			allReady = false
			continue
		}
		if a.Ready && a.Address != "" {
			values = append(values, a.Address)
			continue
		}
		allReady = false
		if pendingMsg == "" {
			pendingMsg = fmt.Sprintf("%s: %s", a.Reason, a.Message)
			if len(desired) > 1 {
				pendingMsg = fmt.Sprintf("slot %d: %s", d.Slot, pendingMsg)
			}
		}
	}
	switch {
	case allReady && equalStrings(c.CurrentAssignment, values):
		p.plan.Assigned++
	case allReady:
		p.plan.Assign = append(p.plan.Assign, Assignment{Target: c.Target, Values: values})
	case pendingMsg != "":
		p.skip(c.Target, SkipAddressPending, "%s", pendingMsg)
	}
}

// releases plans the release of every existing Address of an enabled target kind that
// its claim does not keep, and the Unassign of every target whose claim is released as
// a whole.
func (p *planner) releases() {
	keys := make([]targetKey, 0, len(p.existing))
	for k := range p.existing {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].kind != keys[j].kind {
			return keys[i].kind < keys[j].kind
		}
		return keys[i].name < keys[j].name
	})

	for _, k := range keys {
		if !TargetEnabled(p.policy, k.kind) {
			continue
		}
		ks, kept := p.keep[k]
		if ks.all {
			continue
		}
		ref, ok := p.refs[k]
		if !ok {
			ref = TargetRef{Kind: k.kind, Name: k.name}
			if k.kind == TargetService {
				ref.Namespace = p.policy.Namespace
			}
		}
		var addrs []string
		for _, e := range p.existing[k] {
			if kept && ks.names[e.state.Name] {
				continue
			}
			p.plan.Release = append(p.plan.Release, Release{
				Target: ref, Slot: e.slot, AddressName: e.state.Name, Address: e.state.Address,
			})
			if e.state.Address != "" {
				addrs = append(addrs, e.state.Address)
			}
		}
		if !kept && len(addrs) > 0 {
			p.plan.Unassign = append(p.plan.Unassign, Unassign{Target: ref, Addresses: addrs})
		}
	}
}

func (p *planner) sortOutputs() {
	pl := &p.plan
	sort.SliceStable(pl.Ensure, func(i, j int) bool {
		if pl.Ensure[i].Target != pl.Ensure[j].Target {
			return lessTarget(pl.Ensure[i].Target, pl.Ensure[j].Target)
		}
		return pl.Ensure[i].Slot < pl.Ensure[j].Slot
	})
	sort.SliceStable(pl.Assign, func(i, j int) bool { return lessTarget(pl.Assign[i].Target, pl.Assign[j].Target) })
	sort.SliceStable(pl.Unassign, func(i, j int) bool { return lessTarget(pl.Unassign[i].Target, pl.Unassign[j].Target) })
	sort.SliceStable(pl.Release, func(i, j int) bool {
		a, b := pl.Release[i], pl.Release[j]
		if keyOf(a.Target) != keyOf(b.Target) {
			return lessTarget(a.Target, b.Target)
		}
		if a.Slot != b.Slot {
			return a.Slot < b.Slot
		}
		return a.AddressName < b.AddressName
	})
	sort.SliceStable(pl.Skips, func(i, j int) bool {
		a, b := pl.Skips[i], pl.Skips[j]
		if keyOf(a.Target) != keyOf(b.Target) {
			return lessTarget(a.Target, b.Target)
		}
		if a.Reason != b.Reason {
			return a.Reason < b.Reason
		}
		return a.Message < b.Message
	})
}

// lessTarget orders targets by kind, then name.
func lessTarget(a, b TargetRef) bool {
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	return a.Name < b.Name
}

// assignField names the field an allocated address is written into, for
// ManuallyAssigned skip messages: spec.egressIPs for an EgressIP; for a Service the
// annotation key in Annotation mode (applying the default) or "spec.loadBalancerIP" in
// LoadBalancerIP mode.
func (p *planner) assignField(kind TargetKind) string {
	switch {
	case kind == TargetEgressIP:
		return "spec.egressIPs"
	case p.lbIPMode:
		return "spec.loadBalancerIP"
	case p.policy.Spec.AssignVia.Annotation != "":
		return p.policy.Spec.AssignVia.Annotation
	default:
		return DefaultAssignAnnotation
	}
}

// nextAvailableFromFor builds the NextAvailableFrom block naming pool.
func nextAvailableFromFor(pool PoolRef) *uddiv1alpha1.NextAvailableFrom {
	ref := &uddiv1alpha1.ObjectRef{Name: pool.Name}
	switch pool.Kind {
	case PoolSubnet:
		return &uddiv1alpha1.NextAvailableFrom{SubnetRef: ref}
	case PoolRange:
		return &uddiv1alpha1.NextAvailableFrom{RangeRef: ref}
	case PoolAddressBlock:
		return &uddiv1alpha1.NextAvailableFrom{AddressBlockRef: ref}
	default:
		return nil
	}
}

func requestsFamily(reqs []SlotRequest, f IPFamily) bool {
	for _, r := range reqs {
		if r.Family == f {
			return true
		}
	}
	return false
}

func familyList(reqs []SlotRequest) string {
	parts := make([]string, 0, len(reqs))
	for _, r := range reqs {
		parts = append(parts, string(r.Family))
	}
	return strings.Join(parts, ",")
}

func indexOfFamily(slots []plannedSlot, f IPFamily) int {
	for i, s := range slots {
		if s.family == f {
			return i
		}
	}
	return -1
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

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
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/infobloxopen/universal-ddi-go-client/ipam"
)

// Cache timing of OptionCodeResolver.
const (
	// OptionCodeTTL is how long a list of option codes is trusted. After that the next
	// lookup lists them again.
	OptionCodeTTL = 10 * time.Minute
	// OptionCodeMissRefresh is the shortest time between two lists. A name the cache does
	// not know lists again, so a code created a moment ago is found without waiting for
	// the TTL, but only when the cached list is at least this old: a CR naming a code that
	// does not exist, retried every 30 seconds, costs one list call a minute, not two.
	OptionCodeMissRefresh = time.Minute
)

var (
	// ErrOptionCodeNotFound is wrapped by the error Resolve returns for a name that no
	// option space defines, or that the requested option space does not define.
	ErrOptionCodeNotFound = errors.New("uddi: DHCP option code not found")
	// ErrOptionCodeAmbiguous is wrapped by the error Resolve returns for a name that
	// several option spaces define while the caller named no option space.
	ErrOptionCodeAmbiguous = errors.New("uddi: DHCP option code is ambiguous")
)

// OptionCodeError reports a name Resolve could not turn into one option code. Its text
// names the option and the option space, which is what ends up in a CR's condition.
type OptionCodeError struct {
	// Name is the option name that was looked up.
	Name string
	// Space is the option space the caller asked for, as the caller wrote it; empty when
	// the caller named none.
	Space string
	// Spaces lists, sorted, the option spaces that define Name when it is ambiguous.
	Spaces []string
	// Matches is how many option codes matched when it is ambiguous.
	Matches int

	kind error
}

// Error describes what failed, naming the option and the option space.
func (e *OptionCodeError) Error() string {
	switch {
	case errors.Is(e.kind, ErrOptionCodeAmbiguous) && len(e.Spaces) > 1:
		return fmt.Sprintf("DHCP option code %q exists in several option spaces (%s): set optionSpace to choose one",
			e.Name, strings.Join(e.Spaces, ", "))
	case errors.Is(e.kind, ErrOptionCodeAmbiguous):
		space := e.Space
		if space == "" && len(e.Spaces) == 1 {
			space = e.Spaces[0]
		}
		return fmt.Sprintf("DHCP option code %q matches %d option codes in option space %q", e.Name, e.Matches, space)
	case e.Space != "":
		return fmt.Sprintf("DHCP option code %q not found in option space %q", e.Name, e.Space)
	default:
		return fmt.Sprintf("DHCP option code %q not found in any option space", e.Name)
	}
}

// Unwrap lets errors.Is match ErrOptionCodeNotFound and ErrOptionCodeAmbiguous.
func (e *OptionCodeError) Unwrap() error { return e.kind }

// OptionCodeLister is the one call the resolver makes. DHCP satisfies it.
type OptionCodeLister interface {
	ListOptionCodes(ctx context.Context, opts ListOptions) ([]ipam.OptionCode, error)
}

// OptionCodeResolver turns a DHCP option name, optionally narrowed to one option space,
// into the option code id a dhcpOptions entry needs. It lists every option code of the
// tenant once and answers from that list for OptionCodeTTL, listing again early only when
// a name is missing and the list is at least OptionCodeMissRefresh old.
//
// One resolver belongs to one Client, and so to one Connection and one tenant: the
// Connection resolver caches a Client per Connection, which is what bounds the cache's
// life. It is safe for concurrent use; callers that arrive while a list is under way wait
// for it instead of listing again.
type OptionCodeResolver struct {
	lister      OptionCodeLister
	ttl         time.Duration
	missRefresh time.Duration
	now         func() time.Time

	// gate lets one caller at a time list. It is a channel rather than a mutex so a
	// waiting caller gives up when its context ends.
	gate chan struct{}

	mu  sync.Mutex
	cur *optionCodeIndex
}

// NewOptionCodeResolver returns a resolver that lists option codes through lister.
func NewOptionCodeResolver(lister OptionCodeLister) *OptionCodeResolver {
	return &OptionCodeResolver{
		lister:      lister,
		ttl:         OptionCodeTTL,
		missRefresh: OptionCodeMissRefresh,
		now:         time.Now,
		gate:        make(chan struct{}, 1),
	}
}

// optionCodeIndex is one immutable listing of the tenant's option codes.
type optionCodeIndex struct {
	fetchedAt time.Time
	byName    map[string][]optionCodeEntry
}

type optionCodeEntry struct {
	id    string
	space string
}

// Resolve returns the option code id of the option called name. space is an option space
// id such as "dhcp/option_space/<uuid>"; with it only that space is searched, without it
// every space is, and the name must exist in exactly one. Names match exactly, including
// case.
//
// A name that no space defines returns an *OptionCodeError wrapping
// ErrOptionCodeNotFound, a name several spaces define an *OptionCodeError wrapping
// ErrOptionCodeAmbiguous. A failure to list the option codes is returned as it is, and
// nothing is cached from it.
func (r *OptionCodeResolver) Resolve(ctx context.Context, name, space string) (string, error) {
	seen := r.snapshot()
	if seen != nil {
		age := r.now().Sub(seen.fetchedAt)
		if age < r.ttl {
			id, err := seen.lookup(name, space)
			if !errors.Is(err, ErrOptionCodeNotFound) || age < r.missRefresh {
				return id, err
			}
		}
	}

	idx, err := r.refresh(ctx, seen)
	if err != nil {
		return "", err
	}
	return idx.lookup(name, space)
}

// Invalidate drops the cached list, so the next Resolve lists again whatever its age.
func (r *OptionCodeResolver) Invalidate() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cur = nil
}

func (r *OptionCodeResolver) snapshot() *optionCodeIndex {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cur
}

// refresh lists the option codes, unless another caller already did since seen was read,
// in which case that caller's list is used.
func (r *OptionCodeResolver) refresh(ctx context.Context, seen *optionCodeIndex) (*optionCodeIndex, error) {
	select {
	case r.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-r.gate }()

	// A caller that held the gate before us may have listed already. An Invalidate in
	// between leaves nothing to use, so then we list ourselves.
	if cur := r.snapshot(); cur != nil && cur != seen {
		return cur, nil
	}

	codes, err := r.lister.ListOptionCodes(ctx, ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing DHCP option codes: %w", err)
	}
	idx := newOptionCodeIndex(codes, r.now())

	r.mu.Lock()
	r.cur = idx
	r.mu.Unlock()
	return idx, nil
}

func newOptionCodeIndex(codes []ipam.OptionCode, fetchedAt time.Time) *optionCodeIndex {
	idx := &optionCodeIndex{fetchedAt: fetchedAt, byName: make(map[string][]optionCodeEntry, len(codes))}
	seen := make(map[string]bool, len(codes))
	for i := range codes {
		c := &codes[i]
		id := QualifyID(PrefixOptionCode, strv(c.Id))
		// A code listed twice, which offset paging can do when the tenant changes between
		// two pages, must not make its own name look ambiguous.
		if id == "" || c.Name == "" || seen[id] {
			continue
		}
		seen[id] = true
		idx.byName[c.Name] = append(idx.byName[c.Name], optionCodeEntry{id: id, space: c.OptionSpace})
	}
	return idx
}

// lookup finds name, in space when space is not empty.
func (idx *optionCodeIndex) lookup(name, space string) (string, error) {
	var matches []optionCodeEntry
	for _, e := range idx.byName[name] {
		if space == "" || sameOptionSpace(e.space, space) {
			matches = append(matches, e)
		}
	}
	switch len(matches) {
	case 0:
		return "", &OptionCodeError{Name: name, Space: space, kind: ErrOptionCodeNotFound}
	case 1:
		return matches[0].id, nil
	}
	spaces := make([]string, 0, len(matches))
	for _, m := range matches {
		spaces = append(spaces, m.space)
	}
	slices.Sort(spaces)
	return "", &OptionCodeError{
		Name:    name,
		Space:   space,
		Spaces:  slices.Compact(spaces),
		Matches: len(matches),
		kind:    ErrOptionCodeAmbiguous,
	}
}

// sameOptionSpace compares two option space ids, reading a bare uuid as an id in
// "dhcp/option_space".
func sameOptionSpace(a, b string) bool {
	return QualifyID(PrefixOptionSpace, a) == QualifyID(PrefixOptionSpace, b)
}

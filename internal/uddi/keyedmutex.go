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

import "sync"

// KeyedMutex serialises work per key. The Universal DDI next-available endpoints can
// hand the same address, subnet or block to two concurrent callers, so every create
// whose address field is a "<parent id>/nextavailable..." expression is serialised on
// the parent id, the same way the Infoblox Terraform provider does it.
//
// The zero value is ready to use and safe for concurrent use.
type KeyedMutex struct {
	mu    sync.Mutex
	locks map[string]*keyedEntry
}

type keyedEntry struct {
	mu   sync.Mutex
	refs int
}

// Lock blocks until no other caller holds the lock for key.
func (k *KeyedMutex) Lock(key string) {
	k.mu.Lock()
	if k.locks == nil {
		k.locks = map[string]*keyedEntry{}
	}
	e, ok := k.locks[key]
	if !ok {
		e = &keyedEntry{}
		k.locks[key] = e
	}
	e.refs++
	k.mu.Unlock()

	e.mu.Lock()
}

// Unlock releases the lock for key. Calling it without a matching Lock panics.
func (k *KeyedMutex) Unlock(key string) {
	k.mu.Lock()
	e, ok := k.locks[key]
	if !ok {
		k.mu.Unlock()
		panic("uddi: KeyedMutex.Unlock of an unlocked key " + key)
	}
	e.refs--
	if e.refs == 0 {
		delete(k.locks, key)
	}
	k.mu.Unlock()

	e.mu.Unlock()
}

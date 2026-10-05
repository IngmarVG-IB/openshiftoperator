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

// Package tags builds and inspects the ownership tags the operator stamps on every
// Universal DDI object it creates or adopts. See DESIGN.md section 6.
package tags

import (
	"fmt"
	"sort"

	"github.com/IngmarVG-IB/openshiftoperator/internal/uddi"
)

// Ownership tag keys. Underscores are used because the allowed character set for
// Universal DDI tag keys is not formally documented.
const (
	// KeyManaged marks an object as managed by this operator.
	KeyManaged = "uddi_operator_managed"
	// KeyCluster carries the OpenShift cluster id.
	KeyCluster = "uddi_operator_cluster"
	// KeyOwner carries "<namespace>/<Kind>/<name>".
	KeyOwner = "uddi_operator_owner"
	// KeyUID carries the Kubernetes UID of the owning CR. This is the guard checked
	// before any destructive update or delete.
	KeyUID = "uddi_operator_uid"
)

// ValueManaged is the value of the KeyManaged tag.
const ValueManaged = "true"

// Owner formats the KeyOwner value.
func Owner(namespace, kind, name string) string {
	return fmt.Sprintf("%s/%s/%s", namespace, kind, name)
}

// Ownership returns the tags that identify one CR's Universal DDI object. They are
// never overridable by user-supplied tags.
func Ownership(clusterID, namespace, kind, name, uid string) map[string]string {
	out := map[string]string{
		KeyManaged: ValueManaged,
		KeyOwner:   Owner(namespace, kind, name),
		KeyUID:     uid,
	}
	if clusterID != "" {
		out[KeyCluster] = clusterID
	}
	return out
}

// Merge combines the three tag sources in the order DESIGN.md specifies: the
// Connection's defaultTags first, then the CR's spec.tags, then the ownership tags,
// which always win. The result is a fresh map and is never nil.
func Merge(owner, connectionDefaults, spec map[string]string) map[string]string {
	out := make(map[string]string, len(owner)+len(connectionDefaults)+len(spec))
	for k, v := range connectionDefaults {
		out[k] = v
	}
	for k, v := range spec {
		out[k] = v
	}
	for k, v := range owner {
		out[k] = v
	}
	return out
}

// IsOwnershipKey reports whether key is one of the ownership tag keys, which the
// operator never removes from an object.
func IsOwnershipKey(key string) bool {
	switch key {
	case KeyManaged, KeyCluster, KeyOwner, KeyUID:
		return true
	}
	return false
}

// Keys returns the keys of m, sorted. It is what the loop records in
// status.appliedTags. The result is nil for an empty map.
func Keys(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Desired is the tag state the loop wants on a Universal DDI object: the merged tags to
// set, and the keys the operator applied at its last successful sync that are no longer
// wanted and so have to go. See DESIGN.md section 6, "Tag removal".
type Desired struct {
	// Want is the merged tag map (ownership tags, Connection defaultTags, spec.tags).
	Want map[string]string
	// Remove lists keys to delete from the object, sorted. It never contains a key of
	// Want or an ownership key.
	Remove []string
}

// NewDesired builds a Desired from the merged tags and the keys recorded in
// status.appliedTags. Remove is previouslyApplied minus the keys of want, minus the
// ownership keys, sorted and without duplicates. An empty previouslyApplied, as on the
// first sync after an upgrade from a version that did not record it, removes nothing.
func NewDesired(want map[string]string, previouslyApplied []string) Desired {
	d := Desired{Want: want}
	seen := make(map[string]bool, len(previouslyApplied))
	for _, k := range previouslyApplied {
		if seen[k] || IsOwnershipKey(k) {
			continue
		}
		seen[k] = true
		if _, ok := want[k]; ok {
			continue
		}
		d.Remove = append(d.Remove, k)
	}
	sort.Strings(d.Remove)
	return d
}

// ForPatch returns the tag map to send on an update and whether it differs from what
// Universal DDI currently holds. It starts from current, deletes every Remove key, then
// sets every Want key. Keys present on the object that the operator never wrote are
// kept, because the CRD does not claim exclusive ownership of the object's tags; only
// keys the operator applied before and no longer wants are removed. A Remove key that is
// already absent from current is not a change.
//
// The result is meant to be sent as the whole tags field of a PATCH, which relies on
// Universal DDI replacing the tag map rather than merging into it (DESIGN.md section 6).
func ForPatch(current map[string]string, d Desired) (map[string]string, bool) {
	out := make(map[string]string, len(current)+len(d.Want))
	for k, v := range current {
		out[k] = v
	}
	for _, k := range d.Remove {
		delete(out, k)
	}
	for k, v := range d.Want {
		out[k] = v
	}
	return out, !sameTags(current, out)
}

// sameTags reports whether two tag maps hold the same keys and values. A nil map and an
// empty map are the same.
func sameTags(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// OwnedBy reports whether the tags carry this operator's uid tag for the given CR UID.
// An empty uid never matches, so a CR whose UID is unknown can never delete anything.
func OwnedBy(tags map[string]string, uid string) bool {
	if uid == "" {
		return false
	}
	return tags[KeyUID] == uid
}

// Managed reports whether the tags carry the operator's managed marker.
func Managed(tags map[string]string) bool {
	return tags[KeyManaged] == ValueManaged
}

// UIDFilter builds the _tfilter expression that finds the object belonging to one CR.
func UIDFilter(uid string) string {
	return uddi.TagFilter(map[string]string{KeyUID: uid})
}

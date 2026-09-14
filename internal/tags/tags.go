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

// ForPatch returns the tag map to send on an update and whether it differs from what
// Universal DDI currently holds. Keys present on the object but absent from the desired
// set are preserved, because the CRD does not claim exclusive ownership of the object's
// tags. A consequence is that dropping a key from spec.tags does not remove it from
// Universal DDI; removing it there is a manual step for now.
func ForPatch(current, desired map[string]string) (map[string]string, bool) {
	out := make(map[string]string, len(current)+len(desired))
	for k, v := range current {
		out[k] = v
	}
	changed := false
	for k, v := range desired {
		if cur, ok := current[k]; !ok || cur != v {
			changed = true
		}
		out[k] = v
	}
	return out, changed
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

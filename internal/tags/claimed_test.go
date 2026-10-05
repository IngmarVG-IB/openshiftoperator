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

package tags

import "testing"

func TestClaimedByOther(t *testing.T) {
	const cluster, uid = "cluster-1", "uid-mine"
	owner := Owner("team-a", "View", "default")
	managed := func(c, o, u string) map[string]string {
		return map[string]string{KeyManaged: ValueManaged, KeyCluster: c, KeyOwner: o, KeyUID: u}
	}

	tests := []struct {
		name      string
		tags      map[string]string
		wantOther bool
		claimant  string
	}{
		{"untagged object", map[string]string{"env": "prod"}, false, ""},
		{"our own uid", managed(cluster, owner, uid), false, ""},
		{"earlier incarnation of the same CR", managed(cluster, owner, "uid-old"), false, ""},
		{"another CR in this cluster", managed(cluster, Owner("team-b", "View", "default"), "uid-b"), true,
			"team-b/View/default"},
		{"same owner value on another cluster", managed("cluster-2", owner, "uid-c2"), true,
			"team-a/View/default on cluster cluster-2"},
		{"uid tag without the managed marker", map[string]string{KeyUID: "uid-x"}, false, ""},
		{"managed marker without a uid", map[string]string{KeyManaged: ValueManaged}, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			claimant, other := ClaimedByOther(tt.tags, cluster, owner, uid)
			if other != tt.wantOther || claimant != tt.claimant {
				t.Fatalf("ClaimedByOther = (%q, %v), want (%q, %v)", claimant, other, tt.claimant, tt.wantOther)
			}
		})
	}
}

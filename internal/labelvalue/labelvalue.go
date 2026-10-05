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

// Package labelvalue turns Kubernetes object names into label values. Object names may be
// up to 253 characters, label values only 63, so the policies that label generated objects
// with the name of the Node, Route or EgressIP they came from must shorten long names in a
// stable way or the apply fails.
package labelvalue

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// MaxLength is the longest label value Kubernetes accepts.
const MaxLength = 63

// hashLength is the number of hex characters of the sha256 suffix used for long names.
const hashLength = 10

// For returns name unchanged when it fits in a label value, and otherwise its first
// characters followed by a dash and a hash of the whole name, 63 characters at most. The
// result is deterministic, so a controller can compare For(name) with a label it reads
// back. Object names only contain lower-case alphanumerics, dashes and dots, which are all
// valid in label values; the truncated part is trimmed so the value still ends in an
// alphanumeric character.
func For(name string) string {
	if len(name) <= MaxLength {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	suffix := hex.EncodeToString(sum[:])[:hashLength]
	prefix := strings.TrimRight(name[:MaxLength-hashLength-1], "-.")
	return prefix + "-" + suffix
}

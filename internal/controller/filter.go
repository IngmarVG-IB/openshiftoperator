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

import "strings"

// filterEscaper escapes the two characters that can break out of a double-quoted
// Universal DDI _filter literal: the quote itself and the backslash that escapes it.
var filterEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// quoteFilterValue renders s as a double-quoted Universal DDI _filter literal,
// including the surrounding quotes.
//
// Every adapter's natural-key lookup builds a _filter expression out of user-controlled
// spec values (a view name, a zone fqdn, a record name), so those values must be quoted
// rather than concatenated: a name containing a double quote would otherwise terminate
// the literal early and turn the rest of the name into filter syntax, which at best
// fails the lookup and at worst matches the wrong object. internal/uddi.TagFilter does
// the same job for the single-quoted literals _tfilter uses.
func quoteFilterValue(s string) string {
	return `"` + filterEscaper.Replace(s) + `"`
}

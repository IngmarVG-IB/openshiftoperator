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
	"strings"
	"testing"
)

func TestTagFilterEscapesQuotesAndBackslashes(t *testing.T) {
	got := TagFilter(map[string]string{"k": `it's a \ trap\`})
	want := `'k'=='it\'s a \\ trap\\'`
	if got != want {
		t.Fatalf("TagFilter = %s, want %s", got, want)
	}

	// A value ending in a backslash must not escape the closing quote: the quote at the
	// very end has to be preceded by an even number of backslashes.
	if !strings.HasSuffix(got, `\\'`) || strings.HasSuffix(got, `\\\'`) {
		t.Fatalf("closing quote is escaped away in %s", got)
	}

	// Every quote inside the value must be preceded by an odd number of backslashes, so it
	// can never terminate the literal early.
	inner := strings.TrimSuffix(strings.TrimPrefix(got, `'k'=='`), `'`)
	for i := 0; i < len(inner); i++ {
		if inner[i] != '\'' {
			continue
		}
		n := 0
		for j := i - 1; j >= 0 && inner[j] == '\\'; j-- {
			n++
		}
		if n%2 == 0 {
			t.Fatalf("unescaped quote at %d in %q", i, inner)
		}
	}
}

func TestTagFilterSortsKeysAndJoinsWithAnd(t *testing.T) {
	got := TagFilter(map[string]string{"b": "2", "a": "1"})
	if got != `'a'=='1' and 'b'=='2'` {
		t.Fatalf("unexpected filter: %s", got)
	}
	if TagFilter(nil) != "" {
		t.Fatalf("empty map must yield an empty filter")
	}
}

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

import "testing"

func TestQuoteFilterValue(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", `""`},
		{"default", `"default"`},
		{"apps.example.com.", `"apps.example.com."`},
		// A double quote must not terminate the literal.
		{`we"ird`, `"we\"ird"`},
		// A trailing backslash must not escape the closing quote.
		{`back\slash`, `"back\\slash"`},
		{`ends\`, `"ends\\"`},
		// The classic injection attempt: close the literal and append a clause.
		{`x" or name=="admin`, `"x\" or name==\"admin"`},
	}
	for _, c := range cases {
		if got := quoteFilterValue(c.in); got != c.want {
			t.Errorf("quoteFilterValue(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

// SPDX-License-Identifier: Apache-2.0

package gateway

import "testing"

func TestParseFunctionPath(t *testing.T) {
	cases := []struct {
		in              string
		project, name   string
		ok              bool
	}{
		{"/fn/hello", "", "hello", true},
		{"/fn/", "", "", true},
		{"/v1/acme/fn/hello", "acme", "hello", true},
		{"/v1/acme/fn/h-1_2", "acme", "h-1_2", true},
		{"/v1/acme", "", "", false},
		{"/v1/acme/", "", "", false},
		{"/v1/acme/notfn/x", "", "", false},
		{"/notroute", "", "", false},
	}
	for _, c := range cases {
		p, n, ok := parseFunctionPath(c.in)
		if ok != c.ok || p != c.project || n != c.name {
			t.Errorf("%q: got (%q,%q,%v), want (%q,%q,%v)",
				c.in, p, n, ok, c.project, c.name, c.ok)
		}
	}
}

package runtime

import "testing"

func TestJSONNumbersExact(t *testing.T) {
	for _, tc := range []struct {
		raw string
		ok  bool
	}{
		{`{"id":9007199254740993}`, false}, {`{"id":9007199254740992}`, true},
		{`[0.1,1e2,1.00,-17]`, true}, {`[0.10000000000000000001]`, false},
		{`[1e999999999]`, false}, {`[1e-999999999]`, false}, {`{"name":"9007199254740993"}`, true},
	} {
		if got := JSONNumbersExact([]byte(tc.raw)); got != tc.ok {
			t.Errorf("%s got %v", tc.raw, got)
		}
	}
}

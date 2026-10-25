package runtime

import (
	"bytes"
	"encoding/json"
	"io"
	"math/big"
	"strconv"
	"strings"
)

// JSONNumbersExact rejects numbers whose decimal value changes when copied
// through Sessions' float64-backed protobuf values. It does not check keys.
func JSONNumbersExact(raw []byte) bool {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	for {
		token, err := d.Token()
		if err == io.EOF {
			return true
		}
		if err != nil {
			return false
		}
		number, ok := token.(json.Number)
		if !ok {
			continue
		}
		text := number.String()
		// Bound rational expansion, including zero with an enormous exponent.
		if i := strings.IndexAny(text, "eE"); i >= 0 {
			exponent, err := strconv.Atoi(text[i+1:])
			if err != nil || exponent < -400 || exponent > 400 {
				return false
			}
		}
		value, err := number.Float64()
		if err != nil {
			return false
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			return false
		}
		original, ok := new(big.Rat).SetString(text)
		if !ok {
			return false
		}
		copied, ok := new(big.Rat).SetString(string(encoded))
		if !ok || original.Cmp(copied) != 0 {
			return false
		}
	}
}

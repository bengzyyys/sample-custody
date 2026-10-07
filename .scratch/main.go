package main

import (
	"encoding/json"
	"fmt"
)

type dec struct {
	calls int
	keys  []string
}

func (d *dec) UnmarshalJSON(data []byte) error {
	d.calls++
	var m map[string]int
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	for k := range m {
		d.keys = append(d.keys, k)
	}
	return nil
}

type top struct {
	V   int `json:"version"`
	Trs dec `json:"transfers"`
}

func main() {
	raw := []byte(`{"version":1,"samples":{"x":1},"transfers":{"a":1,"b":2},"other":{},"transfers":{"c":4},"transfers":null}`)
	var t top
	if err := json.Unmarshal(raw, &t); err != nil {
		fmt.Println("err:", err)
		return
	}
	fmt.Printf("calls=%d keys=%v version=%d\n", t.Trs.calls, t.Trs.keys, t.V)

	// absent field
	var t2 top
	json.Unmarshal([]byte(`{"version":1}`), &t2)
	fmt.Printf("absent: calls=%d keys=%v\n", t2.Trs.calls, t2.Trs.keys)
}

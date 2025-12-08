package test

import "testing"

func TestProblemedMapReadWrite(t *testing.T) {
	m := make(map[int]int)
	go func() {
		for {
			_ = m[1]
		}
	}()

	go func() {
		for {
			m[2] = 2
		}
	}()
	select{}
}
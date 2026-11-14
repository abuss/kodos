package main

import (
	"testing"

	lua "github.com/yuin/gopher-lua"
)

// Probe gopher-lua for io.popen and os.execute presence.
// Result: gopher-lua does NOT provide io.popen or os.execute out of the box —
// we need shims for both.
func TestGopherLuaProbe(t *testing.T) {
	L := lua.NewState()
	defer L.Close()

	// Check io.popen
	err := L.DoString(`
		local ok = type(io) == "table" and type(io.popen) == "function"
		if not ok then error("io.popen missing") end
	`)
	if err != nil {
		t.Logf("io.popen: MISSING (%v) — shim needed", err)
	} else {
		t.Log("io.popen: present")
	}

	// Check os.execute
	err2 := L.DoString(`
		local ok = type(os) == "table" and type(os.execute) == "function"
		if not ok then error("os.execute missing") end
	`)
	if err2 != nil {
		t.Logf("os.execute: MISSING (%v) — shim needed", err2)
	} else {
		t.Log("os.execute: present")
	}
}

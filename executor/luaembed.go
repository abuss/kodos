package main

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	lua "github.com/yuin/gopher-lua"
)

//go:embed lua
var luaFS embed.FS

// embeddedModules maps module name -> source (built once at init time).
var embeddedModules map[string]string

func init() {
	embeddedModules = make(map[string]string)
	fs.WalkDir(luaFS, "lua", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".lua") {
			return err
		}
		rel := strings.TrimPrefix(path, "lua/")        // "resolve.lua"
		modSlash := strings.TrimSuffix(rel, ".lua")    // "resolve" or "modules/users"
		modDot := strings.ReplaceAll(modSlash, "/", ".") // "modules.users"
		content, err := luaFS.ReadFile(path)
		if err != nil {
			return err
		}
		src := string(content)
		embeddedModules[modSlash] = src
		if modDot != modSlash {
			embeddedModules[modDot] = src
		}
		return nil
	})
}

// runLuaScript runs the named embedded Lua script with the given args under
// gopher-lua. Every embedded .lua is accessible via require(). searchDir (may
// be "") is added to package.path for on-disk requires (Task 11). Returns
// captured stdout.
// cwd during execution: the lua/ directory adjacent to the kodos binary,
// so relative paths in scripts (../../example/eszkoz/layers/...) resolve
// identically to running them with host lua from executor/lua/.
func runLuaScript(name string, args []string, searchDir string) (string, error) {
	// Change to executor/lua/ so on-disk relative paths work (layers/, etc.)
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("executable: %w", err)
	}
	luaDir := filepath.Join(filepath.Dir(exe), "lua")
	origDir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if err := os.Chdir(luaDir); err != nil {
		// Intentional fallback: if lua/ isn't next to the binary (e.g. go run),
		// keep cwd and let on-disk relative paths resolve from there.
		_ = err
	} else {
		defer os.Chdir(origDir)
	}

	L := lua.NewState()
	defer L.Close()

	// Capture stdout: replace print and io.write.
	var buf bytes.Buffer
	var errBuf bytes.Buffer
	L.SetGlobal("print", L.NewFunction(func(L *lua.LState) int {
		n := L.GetTop()
		parts := make([]string, n)
		for i := 1; i <= n; i++ {
			parts[i-1] = L.ToStringMeta(L.Get(i)).String()
		}
		fmt.Fprintln(&buf, strings.Join(parts, "\t"))
		return 0
	}))
	if iot, ok := L.GetGlobal("io").(*lua.LTable); ok {
		iot.RawSetString("write", L.NewFunction(func(L *lua.LState) int {
			for i := 1; i <= L.GetTop(); i++ {
				v := L.Get(i)
				if _, ok := v.(*lua.LUserData); ok {
					continue // defensive guard: module-level io.write never receives a file-handle userdata as first arg
				}
				buf.WriteString(L.ToStringMeta(v).String())
			}
			return 0
		}))
		// Redirect io.stderr:write to errBuf so error messages are captured
		// and can be included in os.exit error strings.
		// Create a fake stderr file table that writes to errBuf.
		stderrTbl := L.NewTable()
		stderrTbl.RawSetString("write", L.NewFunction(func(L *lua.LState) int {
			for i := 1; i <= L.GetTop(); i++ {
				v := L.Get(i)
				if _, ok := v.(*lua.LUserData); ok {
					continue
				}
				if _, ok := v.(*lua.LTable); ok {
					continue // skip self (stderrTbl) when called as method
				}
				errBuf.WriteString(L.ToStringMeta(v).String())
			}
			return 0
		}))
		iot.RawSetString("stderr", stderrTbl)
	}

	// Override dofile to check embedded FS first.
	L.SetGlobal("dofile", L.NewFunction(func(L *lua.LState) int {
		path := L.CheckString(1)
		// Check if the base filename (or path) matches an embedded file.
		key := strings.TrimSuffix(path, ".lua")
		src, ok := embeddedModules[key]
		if !ok {
			base := filepath.Base(key)
			src, ok = embeddedModules[base]
		}

		var fn *lua.LFunction
		var loadErr error
		if ok {
			fn, loadErr = L.Load(strings.NewReader(src), "@"+filepath.Base(path))
		} else {
			fn, loadErr = L.LoadFile(path)
		}
		if loadErr != nil {
			L.RaiseError("dofile %s: %v", path, loadErr)
			return 0
		}
		top := L.GetTop()
		if err := L.CallByParam(lua.P{Fn: fn, NRet: lua.MultRet, Protect: true}); err != nil {
			L.RaiseError("%v", err)
			return 0
		}
		return L.GetTop() - top
	}))

	argTable := L.NewTable()
	argTable.RawSetInt(0, lua.LString(name))
	for i, a := range args {
		argTable.RawSetInt(i+1, lua.LString(a))
	}
	L.SetGlobal("arg", argTable)

	// Register embedded modules in package.preload.
	// gopher-lua's require: loaders[1](name) returns loader fn OR error string.
	// We store the actual loader fn (compiled chunk) directly in preload, which
	// loaders[1] (loLoaderPreload) returns. require then calls that fn.
	preload := L.GetField(L.GetField(L.Get(lua.EnvironIndex), "package"), "preload")
	pt := preload.(*lua.LTable)
	for modname, src := range embeddedModules {
		capturedSrc := src
		capturedName := "@" + modname + ".lua"
		fn, err := L.Load(strings.NewReader(capturedSrc), capturedName)
		if err != nil {
			return "", fmt.Errorf("compile embedded %s: %w", modname, err)
		}
		pt.RawSetString(modname, fn)
	}

	// Override os.exit so scripts that call it raise a Lua error instead of
	// killing the Go process. Exit code 0 raises "exit:0", non-zero raises
	// "exit:N". Callers can pcall and check the message.
	if osTable, ok := L.GetGlobal("os").(*lua.LTable); ok {
		osTable.RawSetString("exit", L.NewFunction(func(L *lua.LState) int {
			code := L.OptInt(1, 0)
			if code == 0 {
				L.RaiseError("exit:0")
			} else {
				// Include captured stderr so callers (pcall) can inspect error messages.
				L.RaiseError("exit:%d\n%s", code, errBuf.String())
			}
			return 0
		}))
	}

	// kodos_run(scriptname, arg1, ...) — runs another embedded script in a
	// fresh VM and returns its captured stdout as a string. Used by
	// test-generate/test-lock in KODOS_INPROCESS mode instead of io.popen.
	L.SetGlobal("kodos_run", L.NewFunction(func(L *lua.LState) int {
		n := L.GetTop()
		if n < 1 {
			L.RaiseError("kodos_run: script name required")
			return 0
		}
		script := L.CheckString(1)
		var innerArgs []string
		for i := 2; i <= n; i++ {
			innerArgs = append(innerArgs, L.CheckString(i))
		}
		out, err := runLuaScript(script, innerArgs, searchDir)
		if err != nil {
			L.RaiseError("kodos_run %s: %v", script, err)
			return 0
		}
		L.Push(lua.LString(out))
		return 1
	}))

	// Add searchDir to package.path if provided.
	if searchDir != "" {
		pkgPath := L.GetField(L.GetField(L.Get(lua.EnvironIndex), "package"), "path")
		extra := filepath.Join(searchDir, "?.lua") + ";" + filepath.Join(searchDir, "?/init.lua")
		L.SetField(L.GetField(L.Get(lua.EnvironIndex), "package"), "path",
			lua.LString(extra+";"+pkgPath.String()))
	}

	// Load and run the named script from embedded FS.
	content, err := luaFS.ReadFile("lua/" + name)
	if err != nil {
		return "", fmt.Errorf("script %q not found in embedded lua/: %w", name, err)
	}
	fn, err := L.Load(strings.NewReader(string(content)), "@"+name)
	if err != nil {
		return "", fmt.Errorf("compile %s: %w", name, err)
	}
	L.Push(fn)
	if err := L.PCall(0, lua.MultRet, nil); err != nil {
		msg := err.Error()
		// os.exit shim: "exit:0" is a clean exit, non-zero is an error.
		if strings.HasPrefix(msg, "exit:0") {
			return buf.String(), nil
		}
		return buf.String(), err
	}
	return buf.String(), nil
}

// cmdLua implements `kodos lua <script.lua> [args…]`.
func cmdLua(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kodos lua <script.lua> [args...]")
	}
	out, err := runLuaScript(args[0], args[1:], "")
	if out != "" {
		os.Stdout.WriteString(out)
	}
	return err
}

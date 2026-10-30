package neoscript

import (
	"encoding/json"
	"io"
	"math"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neoroute"
	lua "github.com/yuin/gopher-lua"
)

// Decode the snapshot once, directly to Lua's plain-value representation.
type workerWireRequest struct {
	Source   string          `json:"source"`
	Validate bool            `json:"validate,omitempty"`
	Input    json.RawMessage `json:"input"`
}

// WorkerMain must be called before configuration/auth initialization in the host
// main (and in test TestMain). It never runs unless given the exact private mode.
func WorkerMain() (handled bool, code int) {
	if len(os.Args) != 2 || os.Args[1] != workerArgument {
		return false, 0
	}
	if err := confine(); err != nil {
		writeResponse(workerResponse{Error: "confinement unavailable: " + err.Error()})
		return true, 1
	}
	var request workerWireRequest
	decoder := json.NewDecoder(io.LimitReader(os.Stdin, MaxInput+6*MaxSource+129))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeResponse(workerResponse{Error: "invalid worker input"})
		return true, 1
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeResponse(workerResponse{Error: "trailing or oversized worker input"})
		return true, 1
	}
	response := evaluateWorker(request)
	writeResponse(response)
	return true, 0
}

func writeResponse(response workerResponse) {
	if len(response.Error) > 1024 {
		response.Error = response.Error[:1024] + " (truncated)"
	}
	data, err := json.Marshal(response)
	if err != nil || len(data) > MaxOutput {
		data = []byte(`{"error":"script worker response exceeds bound"}`)
	}
	_, _ = os.Stdout.Write(data)
}

func evaluateWorker(request workerWireRequest) (response workerResponse) {
	// Ordinary interpreter panics become errors. Fatal Go OOM remains confined to
	// this process; the parent catches its exit and reaps it before releasing a slot.
	defer func() {
		if recovered := recover(); recovered != nil {
			response = workerResponse{Error: "interpreter panic"}
		}
	}()
	if len(request.Source) == 0 || len(request.Source) > MaxSource || !utf8.ValidString(request.Source) {
		return workerResponse{Error: "invalid source size or encoding"}
	}
	state := lua.NewState(lua.Options{SkipOpenLibs: true, CallStackSize: 128, RegistrySize: 256, RegistryMaxSize: 4096, RegistryGrowStep: 256, MinimizeStackMemory: true})
	defer state.Close()
	fn, err := state.Load(strings.NewReader(request.Source), "routing-script")
	if err != nil {
		return workerResponse{Error: "syntax: " + err.Error()}
	}
	if request.Validate {
		return workerResponse{}
	}
	configureLibraries(state)
	var snapshot any
	if err = json.Unmarshal(request.Input, &snapshot); err != nil {
		return workerResponse{Error: "invalid snapshot"}
	}
	readonly := make(map[*lua.LTable]*lua.LTable)
	inputValue := toLua(state, snapshot, readonly)
	state.SetGlobal("input", inputValue)
	configureIteration(state, readonly)
	configureTable(state, readonly)
	state.Push(fn)
	if err = state.PCall(0, lua.MultRet, nil); err != nil {
		return workerResponse{Error: "runtime: " + err.Error()}
	}
	var input policySnapshot
	if err = json.Unmarshal(request.Input, &input); err != nil {
		return workerResponse{Error: "invalid policy snapshot"}
	}
	policies, configured, err := evaluatePolicies(state, input, inputValue, readonly)
	if err != nil {
		return workerResponse{Error: err.Error()}
	}
	if configured && state.GetTop() == 0 {
		return workerResponse{Proposal: neoroute.ScriptProposal{Policies: policies}}
	}
	if state.GetTop() != 1 {
		return workerResponse{Error: "invalid output: return exactly one stage/account table"}
	}
	table, ok := state.Get(1).(*lua.LTable)
	if !ok || table.Metatable != lua.LNil {
		return workerResponse{Error: "invalid output: expected a plain table"}
	}
	proposal := neoroute.ScriptProposal{Policies: policies}
	fields := 0
	invalid := false
	table.ForEach(func(key, value lua.LValue) {
		fields++
		name, ok := key.(lua.LString)
		if !ok {
			invalid = true
			return
		}
		text, ok := value.(lua.LString)
		if !ok || len(text) > 256 || strings.TrimSpace(string(text)) == "" {
			invalid = true
			return
		}
		switch string(name) {
		case "stageId":
			proposal.StageID = string(text)
		case "accountId":
			proposal.AccountID = string(text)
		default:
			invalid = true
		}
	})
	if invalid || fields != 2 || proposal.StageID == "" || proposal.AccountID == "" {
		return workerResponse{Error: "invalid output: exactly nonempty string stageId/accountId are required"}
	}
	return workerResponse{Proposal: proposal}
}

func configureLibraries(state *lua.LState) {
	lua.OpenBase(state)
	allowed := map[string]bool{"assert": true, "error": true, "tonumber": true, "tostring": true, "type": true, "select": true}
	global := state.Get(lua.GlobalsIndex).(*lua.LTable)
	var remove []lua.LValue
	global.ForEach(func(k, v lua.LValue) {
		if !allowed[k.String()] {
			remove = append(remove, k)
		}
	})
	for _, key := range remove {
		global.RawSet(key, lua.LNil)
	}
	lua.OpenString(state)
	stringsTable := state.GetGlobal("string").(*lua.LTable)
	stringsTable.RawSetString("dump", lua.LNil)
	lua.OpenMath(state)
	mathTable := state.GetGlobal("math").(*lua.LTable)
	mathTable.RawSetString("random", lua.LNil)
	mathTable.RawSetString("randomseed", lua.LNil)
	// Openers push their modules; no library value is a chunk return value.
	state.SetTop(0)
}

func toLua(state *lua.LState, value any, readonly map[*lua.LTable]*lua.LTable) lua.LValue {
	switch value := value.(type) {
	case nil:
		return lua.LNil
	case bool:
		return lua.LBool(value)
	case string:
		return lua.LString(value)
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			state.RaiseError("nonfinite snapshot number")
		}
		return lua.LNumber(value)
	case []any:
		backing := state.CreateTable(len(value), 0)
		for i, item := range value {
			backing.RawSetInt(i+1, toLua(state, item, readonly))
		}
		return readonlyTable(state, backing, readonly)
	case map[string]any:
		backing := state.CreateTable(0, len(value))
		for key, item := range value {
			backing.RawSetString(key, toLua(state, item, readonly))
		}
		return readonlyTable(state, backing, readonly)
	default:
		state.RaiseError("unsupported snapshot value")
		return lua.LNil
	}
}

func readonlyTable(state *lua.LState, backing *lua.LTable, readonly map[*lua.LTable]*lua.LTable) *lua.LTable {
	proxy := state.NewTable()
	readonly[proxy] = backing
	metatable := state.NewTable()
	metatable.RawSetString("__index", backing)
	metatable.RawSetString("__metatable", lua.LString("read-only input"))
	metatable.RawSetString("__newindex", state.NewFunction(func(l *lua.LState) int { l.RaiseError("script input is read-only"); return 0 }))
	metatable.RawSetString("__len", state.NewFunction(func(l *lua.LState) int { l.Push(lua.LNumber(backing.Len())); return 1 }))
	state.SetMetatable(proxy, metatable)
	return proxy
}

func configureIteration(state *lua.LState, readonly map[*lua.LTable]*lua.LTable) {
	unwrap := func(table *lua.LTable) *lua.LTable {
		if backing, ok := readonly[table]; ok {
			return backing
		}
		return table
	}
	state.SetGlobal("next", state.NewFunction(func(l *lua.LState) int {
		table := unwrap(l.CheckTable(1))
		key, value := table.Next(l.Get(2))
		l.Push(key)
		if key == lua.LNil {
			return 1
		}
		l.Push(value)
		return 2
	}))
	state.SetGlobal("pairs", state.NewFunction(func(l *lua.LState) int {
		table := unwrap(l.CheckTable(1))
		iterator := l.NewFunction(func(l *lua.LState) int {
			key, value := table.Next(l.Get(2))
			l.Push(key)
			if key == lua.LNil {
				return 1
			}
			l.Push(value)
			return 2
		})
		l.Push(iterator)
		l.Push(lua.LNil)
		l.Push(lua.LNil)
		return 3
	}))
	state.SetGlobal("ipairs", state.NewFunction(func(l *lua.LState) int {
		table := unwrap(l.CheckTable(1))
		iterator := l.NewFunction(func(l *lua.LState) int {
			index := l.CheckInt(2) + 1
			value := table.RawGetInt(index)
			if value == lua.LNil {
				return 0
			}
			l.Push(lua.LNumber(index))
			l.Push(value)
			return 2
		})
		l.Push(iterator)
		l.Push(lua.LNil)
		l.Push(lua.LNumber(0))
		return 3
	}))
}

func configureTable(state *lua.LState, readonly map[*lua.LTable]*lua.LTable) {
	lua.OpenTable(state)
	table := state.GetGlobal("table").(*lua.LTable)
	for _, name := range []string{"insert", "remove", "sort", "concat", "getn", "maxn"} {
		original := table.RawGetString(name).(*lua.LFunction)
		mutating := name == "insert" || name == "remove" || name == "sort"
		table.RawSetString(name, state.NewFunction(func(l *lua.LState) int {
			target := l.CheckTable(1)
			if backing, ok := readonly[target]; ok {
				if mutating {
					l.RaiseError("script input is read-only")
					return 0
				}
				l.Replace(1, backing)
			}
			return original.GFunction(l)
		}))
	}
	state.SetTop(0)
}

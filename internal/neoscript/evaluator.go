// Package neoscript evaluates optional routing proposals in disposable confined workers.
package neoscript

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/neoroute"
)

const (
	MaxSource      = 16 << 10
	MaxInput       = 256 << 10
	MaxOutput      = 8 << 10
	wallLimit      = 500 * time.Millisecond
	workerArgument = "--internal-neoproxy-script-worker"
)

// Evaluator implements neoroute.ScriptHook. Its zero value is ready to use.
// Errors fail the configured route; callers must never substitute another route.
type Evaluator struct {
	once  sync.Once
	slots chan struct{}
}

func New() *Evaluator { return &Evaluator{} }

// Validate checks syntax inside the same hard confinement used for execution.
func (e *Evaluator) Validate(source string) error {
	_, err := e.invoke(context.Background(), workerRequest{Source: source, Validate: true})
	return err
}

func (e *Evaluator) Evaluate(ctx context.Context, in neoroute.ScriptInput) (neoroute.ScriptProposal, error) {
	source := in.Source
	in.Source = ""
	in.VirtualModel.Script = ""
	response, err := e.invoke(ctx, workerRequest{Source: source, Input: in})
	if err != nil {
		return neoroute.ScriptProposal{}, err
	}
	proposal := response.Proposal
	if err = validateProposal(proposal, in); err != nil {
		return neoroute.ScriptProposal{}, err
	}
	return proposal, nil
}

type workerRequest struct {
	Source   string               `json:"source"`
	Validate bool                 `json:"validate,omitempty"`
	Input    neoroute.ScriptInput `json:"input"`
}
type workerResponse struct {
	Proposal neoroute.ScriptProposal `json:"proposal"`
	Error    string                  `json:"error,omitempty"`
}

func (e *Evaluator) invoke(ctx context.Context, request workerRequest) (workerResponse, error) {
	if err := ctx.Err(); err != nil {
		return workerResponse{}, err
	}
	if len(request.Source) == 0 || len(request.Source) > MaxSource || !utf8.ValidString(request.Source) {
		return workerResponse{}, errors.New("script source must be nonempty UTF-8 and at most 16384 bytes")
	}
	// Reject oversized strings/collections before encoding/json allocates its buffer.
	remaining := MaxInput
	if !boundedValue(reflect.ValueOf(request.Input), &remaining, 0) {
		return workerResponse{}, errors.New("script input exceeds 262144-byte bound")
	}
	data, err := json.Marshal(request)
	if err != nil {
		return workerResponse{}, fmt.Errorf("encode script input: %w", err)
	}
	if len(data) > MaxInput+6*MaxSource+128 {
		return workerResponse{}, errors.New("script worker input exceeds bound")
	}
	e.once.Do(func() { e.slots = make(chan struct{}, 2) })
	select {
	case e.slots <- struct{}{}:
		defer func() { <-e.slots }()
	default:
		return workerResponse{}, errors.New("script evaluator busy: two workers already active")
	}
	executable, err := os.Executable()
	if err != nil {
		return workerResponse{}, fmt.Errorf("locate script worker: %w", err)
	}
	childContext, cancel := context.WithTimeout(ctx, wallLimit)
	defer cancel()
	command := exec.CommandContext(childContext, executable, workerArgument)
	command.Env = []string{"GOMAXPROCS=1", "GOMEMLIMIT=64MiB", "GOTRACEBACK=none"}
	command.Dir = "/"
	configureChild(command)
	command.Stdin = bytes.NewReader(data)
	var stdout, stderr cappedBuffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err = command.Run(); err != nil {
		if ctx.Err() != nil {
			return workerResponse{}, ctx.Err()
		}
		if childContext.Err() != nil {
			return workerResponse{}, errors.New("script execution exceeded 500ms limit")
		}
		// Child crashes may include interpreter internals. Keep the public error bounded
		// and distinguish exhaustion/confinement from a valid worker protocol response.
		var response workerResponse
		if !stdout.overflow && json.Unmarshal(stdout.data, &response) == nil && response.Error != "" {
			return workerResponse{}, fmt.Errorf("script: %s", response.Error)
		}
		return workerResponse{}, fmt.Errorf("script worker failed (resource exhaustion or confinement failure): %w", err)
	}
	if stdout.overflow || stderr.overflow {
		return workerResponse{}, errors.New("script worker output exceeded 8192-byte bound")
	}
	var response workerResponse
	decoder := json.NewDecoder(bytes.NewReader(stdout.data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&response); err != nil {
		return workerResponse{}, fmt.Errorf("invalid script worker response: %w", err)
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return workerResponse{}, errors.New("script worker returned trailing output")
	}
	if response.Error != "" {
		return workerResponse{}, fmt.Errorf("script: %s", response.Error)
	}
	return response, nil
}

// cappedBuffer drains pipes without retaining unbounded output or blocking Wait.
type cappedBuffer struct {
	data     []byte
	overflow bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := MaxOutput - len(b.data)
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	b.data = append(b.data, p...)
	return n, nil
}

// The estimate includes worst-case JSON string escaping and collection overhead.
// ScriptInput is value-only; this walk does not invoke custom marshal methods.
func boundedValue(v reflect.Value, remaining *int, depth int) bool {
	if depth > 16 {
		return false
	}
	*remaining -= 32
	if *remaining < 0 {
		return false
	}
	if v.Type() == reflect.TypeFor[json.RawMessage]() {
		*remaining -= v.Len()
		return *remaining >= 0
	}
	switch v.Kind() {
	case reflect.String:
		if v.Len() > *remaining/6 {
			return false
		}
		*remaining -= 6 * v.Len()
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			return boundedValue(v.Elem(), remaining, depth+1)
		}
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			*remaining -= 6 * len(v.Type().Field(i).Name)
			if !boundedValue(v.Field(i), remaining, depth+1) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		if v.Len() > MaxInput/32 {
			return false
		}
		for i := 0; i < v.Len(); i++ {
			if !boundedValue(v.Index(i), remaining, depth+1) {
				return false
			}
		}
	case reflect.Map, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return false
	}
	return *remaining >= 0
}

func validateProposal(p neoroute.ScriptProposal, in neoroute.ScriptInput) error {
	if p.StageID == "" && p.AccountID == "" {
		for _, stage := range in.Stages {
			if stage.Lua != nil {
				return nil
			}
		}
	}
	if strings.TrimSpace(p.StageID) == "" || strings.TrimSpace(p.AccountID) == "" || len(p.StageID) > 256 || len(p.AccountID) > 256 {
		return errors.New("invalid script output: stageId and accountId must be nonempty strings of at most 256 bytes")
	}
	for _, stage := range in.Stages {
		if stage.StageID != p.StageID || stage.Skipped != "" {
			continue
		}
		for _, candidate := range stage.Candidates {
			if candidate.AccountID == p.AccountID {
				return nil
			}
		}
	}
	return errors.New("invalid script output: selected stage/account is not an eligible candidate")
}

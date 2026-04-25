// Package emission provides an event emitter.
// copy form https://raw.githubusercontent.com/chuckpreslar/emission/master/emitter.go
// fix issue with nest once
//
// Performance: common listener signatures (func(), func(error), func([]byte),
// func(uint16)) are dispatched without any reflection at emit time.
// Unknown signatures use a pre-built wrapper with pooled []reflect.Value to
// eliminate per-emit heap allocations.  Generic helpers On1/Once1 allow
// callers to register any func(T) with zero reflection at emit time.

package emission

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
)

// Default number of maximum listeners for an event.
const DefaultMaxListeners = 10

// Error presented when an invalid argument is provided as a listener function
var ErrNoneFunction = errors.New("Kind of Value for listener is not Func.")

// RecoveryListener ...
type RecoveryListener func(interface{}, interface{}, error)

// listenerEntry pairs a pre-built dispatch wrapper with the original function
// pointer so that RemoveListener can identify and remove it.
type listenerEntry struct {
	call func([]interface{}) // reflection-free dispatch wrapper
	ptr  uintptr             // reflect.ValueOf(original).Pointer(); 0 if unavailable
}

// Pooled argument slices for the reflection fallback path.
// Indexed by argument count (0–3); counts > 3 allocate directly.
var rvPool = [4]*sync.Pool{
	0: {New: func() interface{} { v := make([]reflect.Value, 0); return &v }},
	1: {New: func() interface{} { v := make([]reflect.Value, 1); return &v }},
	2: {New: func() interface{} { v := make([]reflect.Value, 2); return &v }},
	3: {New: func() interface{} { v := make([]reflect.Value, 3); return &v }},
}

// Emitter is a reflect-minimal event emitter.
type Emitter struct {
	events       map[interface{}][]listenerEntry
	onces        map[interface{}][]listenerEntry
	recoverer    RecoveryListener
	maxListeners int
}

// NewEmitter returns a new Emitter object, defaulting the
// number of maximum listeners per event to the DefaultMaxListeners
// constant and initializing its events map.
func NewEmitter() *Emitter {
	return &Emitter{
		events:       make(map[interface{}][]listenerEntry),
		onces:        make(map[interface{}][]listenerEntry),
		maxListeners: DefaultMaxListeners,
	}
}

// On is an alias for AddListener.
func (e *Emitter) On(event, listener interface{}) *Emitter {
	return e.AddListener(event, listener)
}

// AddListener appends the listener argument to the event arguments slice.
// If the reflect Value of the listener does not have a Kind of Func then
// AddListener panics (or calls the RecoveryListener if one has been set).
func (e *Emitter) AddListener(event, listener interface{}) *Emitter {
	entry, ok := buildEntry(listener)
	if !ok {
		if e.recoverer == nil {
			panic(ErrNoneFunction)
		}
		e.recoverer(event, listener, ErrNoneFunction)
		return e
	}
	if e.maxListeners != -1 && e.maxListeners < len(e.events[event])+1 {
		fmt.Fprintf(os.Stdout, "Warning: event `%v` has exceeded the maximum "+
			"number of listeners of %d.\n", event, e.maxListeners)
	}
	e.events[event] = append(e.events[event], entry)
	return e
}

// RemoveListener removes the listener from the event's listener slice.
func (e *Emitter) RemoveListener(event, listener interface{}) *Emitter {
	rv := reflect.ValueOf(listener)
	if rv.Kind() != reflect.Func {
		if e.recoverer == nil {
			panic(ErrNoneFunction)
		}
		e.recoverer(event, listener, ErrNoneFunction)
		return e
	}
	ptr := rv.Pointer()
	if entries, ok := e.events[event]; ok {
		out := entries[:0]
		for _, ent := range entries {
			if ent.ptr != ptr {
				out = append(out, ent)
			}
		}
		e.events[event] = out
	}
	if entries, ok := e.onces[event]; ok {
		out := entries[:0]
		for _, ent := range entries {
			if ent.ptr != ptr {
				out = append(out, ent)
			}
		}
		e.onces[event] = out
	}
	return e
}

// Off is an alias for RemoveListener.
func (e *Emitter) Off(event, listener interface{}) *Emitter {
	return e.RemoveListener(event, listener)
}

// Once registers a listener that fires at most once for the given event.
func (e *Emitter) Once(event, listener interface{}) *Emitter {
	entry, ok := buildEntry(listener)
	if !ok {
		if e.recoverer == nil {
			panic(ErrNoneFunction)
		}
		e.recoverer(event, listener, ErrNoneFunction)
		return e
	}
	if e.maxListeners != -1 && e.maxListeners < len(e.onces[event])+1 {
		fmt.Fprintf(os.Stdout, "Warning: event `%v` has exceeded the maximum "+
			"number of listeners of %d.\n", event, e.maxListeners)
	}
	e.onces[event] = append(e.onces[event], entry)
	return e
}

// Emit calls each listener registered for event with the supplied arguments.
func (e *Emitter) Emit(event interface{}, arguments ...interface{}) *Emitter {
	if entries, ok := e.events[event]; ok {
		for _, ent := range entries {
			e.dispatch(ent, event, arguments)
		}
	}
	// Execute onces; preserve any new onces registered during execution
	// (fix issue with nested Once — same semantics as original).
	if entries, ok := e.onces[event]; ok {
		origLen := len(entries)
		for _, ent := range entries {
			e.dispatch(ent, event, arguments)
		}
		e.onces[event] = e.onces[event][origLen:]
	}
	return e
}

func (e *Emitter) dispatch(ent listenerEntry, event interface{}, args []interface{}) {
	if e.recoverer != nil {
		defer func() {
			if r := recover(); r != nil {
				e.recoverer(event, ent.ptr, fmt.Errorf("%v", r))
			}
		}()
	}
	ent.call(args)
}

// RecoverWith sets the listener to call when a panic occurs.
func (e *Emitter) RecoverWith(listener RecoveryListener) *Emitter {
	e.recoverer = listener
	return e
}

// SetMaxListeners sets the maximum number of listeners per event.
// Pass -1 for unlimited.
func (e *Emitter) SetMaxListeners(max int) *Emitter {
	e.maxListeners = max
	return e
}

// GetListenerCount returns the number of listeners registered for event.
func (e *Emitter) GetListenerCount(event interface{}) (count int) {
	if entries, ok := e.events[event]; ok {
		count = len(entries)
	}
	return
}

// On1 registers a typed listener with zero reflection at emit time.
// Use instead of e.On(event, fn) when the argument type is not covered by the
// built-in fast paths (func(), func(error), func([]byte), func(uint16)).
func On1[T any](e *Emitter, event interface{}, fn func(T)) *Emitter {
	e.events[event] = append(e.events[event], listenerEntry{
		call: func(args []interface{}) {
			if len(args) > 0 {
				fn(args[0].(T))
			}
		},
	})
	return e
}

// Once1 registers a typed one-shot listener with zero reflection at emit time.
func Once1[T any](e *Emitter, event interface{}, fn func(T)) *Emitter {
	e.onces[event] = append(e.onces[event], listenerEntry{
		call: func(args []interface{}) {
			if len(args) > 0 {
				fn(args[0].(T))
			}
		},
	})
	return e
}

// buildEntry creates a listenerEntry for the given listener.
// Returns (entry, true) on success, (zero, false) if listener is not a Func.
//
// Fast path: common signatures are wrapped with a direct type assertion so
// that no reflection happens when the listener is actually called.
// Slow path: an unknown function type is wrapped with a pooled-reflect
// wrapper that avoids heap allocation for 0–3 argument calls.
func buildEntry(listener interface{}) (listenerEntry, bool) {
	// Fast path — type-assert well-known signatures.
	switch fn := listener.(type) {
	case func():
		return listenerEntry{call: func(_ []interface{}) { fn() }}, true
	case func(error):
		return listenerEntry{call: func(args []interface{}) {
			var err error
			if len(args) > 0 && args[0] != nil {
				err = args[0].(error)
			}
			fn(err)
		}}, true
	case func([]byte):
		return listenerEntry{call: func(args []interface{}) {
			if len(args) > 0 {
				fn(args[0].([]byte))
			}
		}}, true
	case func(uint16):
		return listenerEntry{call: func(args []interface{}) {
			if len(args) > 0 {
				fn(args[0].(uint16))
			}
		}}, true
	}

	// Reflection fallback — verify the value is a Func, then build a wrapper
	// that reuses pooled []reflect.Value slices to avoid per-call allocation.
	rv := reflect.ValueOf(listener)
	if rv.Kind() != reflect.Func {
		return listenerEntry{}, false
	}
	t := rv.Type()
	numIn := t.NumIn()
	ptr := rv.Pointer()

	// Pre-capture the argument types to avoid repeated Type().In() calls.
	ins := make([]reflect.Type, numIn)
	for i := range ins {
		ins[i] = t.In(i)
	}

	call := func(args []interface{}) {
		if numIn == 0 {
			rv.Call(nil)
			return
		}

		// Borrow a pre-sized slice from the pool (avoids allocation for 0–3 args).
		var vals []reflect.Value
		var poolPtr *[]reflect.Value
		if numIn < len(rvPool) {
			poolPtr = rvPool[numIn].Get().(*[]reflect.Value)
			vals = (*poolPtr)[:numIn]
		} else {
			vals = make([]reflect.Value, numIn)
		}

		for i := 0; i < numIn; i++ {
			if i < len(args) && args[i] != nil {
				vals[i] = reflect.ValueOf(args[i])
			} else {
				vals[i] = reflect.Zero(ins[i])
			}
		}
		rv.Call(vals)

		// Zero out borrowed slice before returning to pool to avoid memory leaks.
		if poolPtr != nil {
			for i := range vals {
				vals[i] = reflect.Value{}
			}
			rvPool[numIn].Put(poolPtr)
		}
	}

	return listenerEntry{call: call, ptr: ptr}, true
}

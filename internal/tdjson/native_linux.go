//go:build tdlib && cgo && linux

package tdjson

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdlib.h>
#include <string.h>

typedef int (*create_fn)(void);
typedef void (*send_fn)(int, const char*);
typedef const char* (*receive_fn)(double);
typedef const char* (*execute_fn)(const char*);
typedef struct { void *handle; create_fn create; send_fn send; receive_fn receive; execute_fn execute; } bridge;
static bridge* bridge_open(const char* path) {
 bridge *b = calloc(1,sizeof(bridge));
 if (!b) return NULL;
 b->handle=dlopen(path,RTLD_NOW|RTLD_LOCAL);
 if (!b->handle) {free(b);return NULL;}
 b->create=(create_fn)dlsym(b->handle,"td_create_client_id");
 b->send=(send_fn)dlsym(b->handle,"td_send");
 b->receive=(receive_fn)dlsym(b->handle,"td_receive");
 b->execute=(execute_fn)dlsym(b->handle,"td_execute");
 if (!b->create || !b->send || !b->receive || !b->execute) {
  dlclose(b->handle);free(b);return NULL;
 }
 // Before the receive loop starts: disable raw TDLib logging, which may include
 // authentication material. Never call execute concurrently with receive.
 b->execute("{\"@type\":\"setLogStream\",\"log_stream\":{\"@type\":\"logStreamEmpty\"}}");
 return b;
}
static int bridge_create(bridge* b) {return b->create();}
static void bridge_send(bridge* b,int id,const char* request) {b->send(id,request);}
static char* bridge_receive(bridge* b,double timeout) {
 const char *s=b->receive(timeout);
 return s ? strdup(s) : NULL;
}
*/
import "C"

import (
	"errors"
	"path/filepath"
	"sync/atomic"
	"time"
	"unsafe"
)

var nativeOpened atomic.Bool

type nativeTransport struct{ bridge *C.bridge }

// OpenNative accepts only an explicit absolute library path; native code is a
// deployment trust boundary, never a user-controlled request parameter.
func OpenNative(path string) (Transport, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("TDLib library path must be absolute")
	}
	if !nativeOpened.CompareAndSwap(false, true) {
		return nil, errors.New("only one native TDLib transport is allowed per process")
	}
	p := C.CString(path)
	defer C.free(unsafe.Pointer(p))
	b := C.bridge_open(p)
	if b == nil {
		nativeOpened.Store(false)
		return nil, errors.New("could not load TDLib JSON library or required symbols")
	}
	return &nativeTransport{bridge: b}, nil
}
func (n *nativeTransport) CreateClientID() (int, error) { return int(C.bridge_create(n.bridge)), nil }
func (n *nativeTransport) Send(id int, request []byte) error {
	p := C.CString(string(request))
	defer C.free(unsafe.Pointer(p))
	C.bridge_send(n.bridge, C.int(id), p)
	return nil
}
func (n *nativeTransport) Receive(timeout time.Duration) ([]byte, error) {
	p := C.bridge_receive(n.bridge, C.double(timeout.Seconds()))
	if p == nil {
		return nil, nil
	}
	defer C.free(unsafe.Pointer(p))
	return []byte(C.GoString(p)), nil
}

// Do not dlclose: TDLib has process-wide native threads. The OS reclaims the
// handle on process exit. Engine.Close has already closed all client instances.
func (n *nativeTransport) Close() error { return nil }

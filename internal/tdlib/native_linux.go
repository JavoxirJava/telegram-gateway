//go:build linux && cgo

package tdlib

/*
#cgo LDFLAGS: -ldl
#include <dlfcn.h>
#include <stdlib.h>
static void* lib;
static void* (*client_create)(void);
static void (*client_send)(void*,const char*);
static const char* (*client_receive)(void*,double);
static void (*client_destroy)(void*);
static const char* (*client_execute)(void*,const char*);
static const char* load_td(const char *path) {
 lib=dlopen(path,RTLD_NOW|RTLD_LOCAL);
 if(!lib) return dlerror();
 client_create=dlsym(lib,"td_json_client_create");
 client_send=dlsym(lib,"td_json_client_send");
 client_receive=dlsym(lib,"td_json_client_receive");
 client_destroy=dlsym(lib,"td_json_client_destroy");
 client_execute=dlsym(lib,"td_json_client_execute");
 if(!client_create||!client_send||!client_receive||!client_destroy||!client_execute) return "TDLib JSON symbols missing";
 client_execute(NULL,"{\"@type\":\"setLogVerbosityLevel\",\"new_verbosity_level\":0}");
 return NULL;
}
static void* create_td() {return client_create();}
static void send_td(void *p,const char *q) {client_send(p,q);}
static const char* receive_td(void *p) {return client_receive(p,0.25);}
static void destroy_td(void *p) {client_destroy(p);}
*/
import "C"
import (
	"errors"
	"os"
	"sync"
	"unsafe"
)

var nativeOnce sync.Once
var nativeErr error

type nativeTransport struct{ ptr unsafe.Pointer }

func newTransport() (transport, error) {
	nativeOnce.Do(func() {
		path := os.Getenv("TDLIB_LIBRARY")
		if path == "" {
			path = "libtdjson.so"
		}
		p := C.CString(path)
		defer C.free(unsafe.Pointer(p))
		if e := C.load_td(p); e != nil {
			nativeErr = errors.New(C.GoString(e))
		}
	})
	if nativeErr != nil {
		return nil, nativeErr
	}
	p := C.create_td()
	if p == nil {
		return nil, errors.New("TDLib client creation failed")
	}
	return &nativeTransport{ptr: p}, nil
}
func (t *nativeTransport) Send(raw []byte) {
	q := C.CString(string(raw))
	defer C.free(unsafe.Pointer(q))
	C.send_td(t.ptr, q)
}
func (t *nativeTransport) Receive() []byte {
	p := C.receive_td(t.ptr)
	if p == nil {
		return nil
	}
	return []byte(C.GoString(p))
}
func (t *nativeTransport) Destroy() { C.destroy_td(t.ptr) }

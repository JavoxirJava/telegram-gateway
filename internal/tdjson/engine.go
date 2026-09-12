// Package tdjson owns the process-wide TDLib JSON receive loop. It is not an
// HTTP API. Only explicitly allowed read/authentication operations can be sent.
package tdjson

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrClosed     = errors.New("TDLib client closed")
	ErrOverloaded = errors.New("TDLib request capacity reached")
	ErrReadOnly   = errors.New("operation is not allowed by the read-only TDLib adapter")
)

// Transport must copy received native memory before returning. Receive is called
// by exactly one goroutine; Send may be called concurrently. Close must not
// unload native TDLib code while its process-wide background threads exist.
type Transport interface {
	CreateClientID() (int, error)
	Send(int, []byte) error
	Receive(time.Duration) ([]byte, error)
	Close() error
}

// UpdateHandler runs in receive order for one client. It must not synchronously
// call Client.Call: the matching response is on the same ordered mailbox.
// Durable consumers must commit before returning; an error stops the engine.
type UpdateHandler func(context.Context, json.RawMessage) error

// ErrorTransformer sees sanitized native errors even after the caller timed out.
// It runs on the ordered client mailbox: do not call this client's Call method,
// and bound any external I/O. It must preserve an error rather than swallow it.
type ErrorTransformer func(context.Context, error) error

type Engine struct {
	transport Transport
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	clients   map[int]*Client
	closing   bool
	failure   error
	wg        sync.WaitGroup
	prefix    string
	next      atomic.Uint64
}

type response struct {
	body json.RawMessage
	err  error
}

type Client struct {
	engine         *Engine
	id             int
	handler        UpdateHandler
	errorTransform ErrorTransformer
	frames         chan json.RawMessage
	done           chan struct{}
	mu             sync.Mutex
	closing        bool
	closed         bool
	nativeClosed   bool
	failure        error
	pending        map[string]chan response
}

const maxPending = 64
const maxFrame = 16 << 20

func New(transport Transport) (*Engine, error) {
	if transport == nil {
		return nil, errors.New("TDLib transport is required")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{transport: transport, ctx: ctx, cancel: cancel, clients: make(map[int]*Client), prefix: hex.EncodeToString(nonce[:])}
	e.wg.Add(1)
	go e.receive()
	return e, nil
}

func (e *Engine) NewClient(handler UpdateHandler, transforms ...ErrorTransformer) (*Client, error) {
	if len(transforms) > 1 {
		return nil, errors.New("only one native error transformer is supported")
	}
	var transform ErrorTransformer
	if len(transforms) == 1 {
		transform = transforms[0]
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closing || e.ctx.Err() != nil {
		return nil, ErrClosed
	}
	id, err := e.transport.CreateClientID()
	if err != nil {
		return nil, fmt.Errorf("create TDLib client: %w", err)
	}
	if id <= 0 {
		return nil, errors.New("TDLib returned invalid client identifier")
	}
	if _, ok := e.clients[id]; ok {
		return nil, errors.New("TDLib reused an active client identifier")
	}
	c := &Client{engine: e, id: id, handler: handler, errorTransform: transform, frames: make(chan json.RawMessage, 64), done: make(chan struct{}), pending: make(map[string]chan response)}
	e.clients[id] = c
	e.wg.Add(1)
	go c.process()
	return c, nil
}

func (e *Engine) receive() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer e.wg.Done()
	for e.ctx.Err() == nil {
		data, err := e.transport.Receive(200 * time.Millisecond)
		if err != nil {
			e.fail(errors.New("TDLib receive transport failed"))
			return
		}
		if len(data) == 0 {
			continue
		}
		if len(data) > maxFrame {
			e.fail(errors.New("TDLib frame exceeds size limit"))
			return
		}
		var header struct {
			ClientID int `json:"@client_id"`
		}
		if err := json.Unmarshal(data, &header); err != nil {
			e.fail(errors.New("TDLib returned invalid JSON"))
			return
		}
		e.mu.Lock()
		c := e.clients[header.ClientID]
		e.mu.Unlock()
		if c == nil {
			continue
		}
		frame := append(json.RawMessage(nil), data...)
		// Bounded backpressure, never silently drop updates (especially deletions).
		select {
		case c.frames <- frame:
		case <-c.done:
		case <-e.ctx.Done():
			return
		}
	}
}

func (c *Client) process() {
	defer c.engine.wg.Done()
	defer func() {
		c.engine.mu.Lock()
		delete(c.engine.clients, c.id)
		c.engine.mu.Unlock()
	}()
	defer c.finish(ErrClosed)
	defer func() {
		if recover() != nil {
			c.engine.fail(errors.New("TDLib update handler panicked"))
		}
	}()
	for {
		select {
		case <-c.engine.ctx.Done():
			return
		case <-c.done:
			return
		case raw := <-c.frames:
			var h struct {
				Type  string `json:"@type"`
				Extra string `json:"@extra"`
				State struct {
					Type string `json:"@type"`
				} `json:"authorization_state"`
			}
			if err := json.Unmarshal(raw, &h); err != nil {
				c.engine.fail(errors.New("invalid TDLib frame header"))
				return
			}
			if strings.HasPrefix(h.Type, "update") {
				if c.handler != nil {
					if err := c.handler(c.engine.ctx, raw); err != nil {
						c.engine.fail(errors.New("TDLib ordered update handler failed"))
						return
					}
				}
				if h.Type == "updateAuthorizationState" && h.State.Type == "authorizationStateClosed" {
					c.mu.Lock()
					c.nativeClosed = true
					c.mu.Unlock()
					c.finish(ErrClosed)
					return
				}
			} else if h.Extra != "" {
				responseErr := decodeError(raw)
				if responseErr != nil && c.errorTransform != nil {
					if mapped := c.errorTransform(c.engine.ctx, responseErr); mapped != nil {
						responseErr = mapped
					}
				}
				c.mu.Lock()
				ch := c.pending[h.Extra]
				delete(c.pending, h.Extra)
				c.mu.Unlock()
				if ch != nil {
					ch <- response{body: raw, err: responseErr}
				}
			}
		}
	}
}

// Call copies request fields, so adding @extra cannot mutate the caller's map.
// A cancelled call removes its waiter; late successes are ignored, but native
// errors still reach the error transformer. Cancellation does not undo a request
// already accepted by Telegram.
func (c *Client) Call(ctx context.Context, method string, fields map[string]any) (json.RawMessage, error) {
	if !allowed(method) {
		return nil, ErrReadOnly
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	request := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		if strings.HasPrefix(k, "@") {
			return nil, errors.New("reserved TDLib request field")
		}
		request[k] = v
	}
	extra := fmt.Sprintf("%s-%d", c.engine.prefix, c.engine.next.Add(1))
	request["@type"] = method
	request["@extra"] = extra
	body, err := json.Marshal(request)
	if err != nil {
		return nil, errors.New("invalid TDLib request fields")
	}
	if len(body) > 1<<20 {
		return nil, errors.New("TDLib request exceeds size limit")
	}
	ch := make(chan response, 1)
	c.mu.Lock()
	if c.closed || c.closing {
		c.mu.Unlock()
		return nil, ErrClosed
	}
	if len(c.pending) >= maxPending {
		c.mu.Unlock()
		return nil, ErrOverloaded
	}
	c.pending[extra] = ch
	// Serialize close vs send: no request is submitted after close begins.
	err = c.engine.transport.Send(c.id, body)
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, extra); c.mu.Unlock() }()
	if err != nil {
		return nil, errors.New("TDLib send transport failed")
	}
	select {
	case r := <-ch:
		return r.body, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, c.Err()
	case <-c.engine.ctx.Done():
		return nil, ErrClosed
	}
}

func (c *Client) beginClose() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.closing {
		return nil
	}
	c.closing = true
	if err := c.engine.transport.Send(c.id, []byte(`{"@type":"close"}`)); err != nil {
		return errors.New("send TDLib close failed")
	}
	return nil
}

func (c *Client) Close(ctx context.Context) error {
	if err := c.beginClose(); err != nil {
		return err
	}
	select {
	case <-c.done:
		c.mu.Lock()
		clean := c.nativeClosed
		c.mu.Unlock()
		if !clean {
			return c.Err()
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) Done() <-chan struct{} { return c.done }
func (c *Client) Err() error            { c.mu.Lock(); defer c.mu.Unlock(); return c.failure }
func (c *Client) finish(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.failure = err
	for id, ch := range c.pending {
		ch <- response{err: err}
		delete(c.pending, id)
	}
	close(c.done)
}

func (e *Engine) fail(err error) {
	e.mu.Lock()
	if e.failure == nil {
		e.failure = err
	}
	e.closing = true
	clients := make([]*Client, 0, len(e.clients))
	for _, c := range e.clients {
		clients = append(clients, c)
	}
	e.mu.Unlock()
	e.cancel()
	for _, c := range clients {
		c.finish(err)
	}
}

// Close drains native clients through authorizationStateClosed. A timeout is an
// error, not proof of a clean native shutdown; the host must then terminate the
// process before reusing its session directories.
func (e *Engine) Close(ctx context.Context) error {
	e.mu.Lock()
	e.closing = true
	clients := make([]*Client, 0, len(e.clients))
	for _, c := range e.clients {
		clients = append(clients, c)
	}
	e.mu.Unlock()
	var closeErr error
	for _, c := range clients {
		if err := c.beginClose(); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
	}
	for _, c := range clients {
		select {
		case <-c.done:
		case <-ctx.Done():
			e.fail(ctx.Err())
			return ctx.Err()
		}
	}
	e.cancel()
	finished := make(chan struct{})
	go func() { e.wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-ctx.Done():
		return ctx.Err()
	}
	e.mu.Lock()
	failure := e.failure
	e.mu.Unlock()
	return errors.Join(closeErr, failure, e.transport.Close())
}

func allowed(method string) bool {
	switch method {
	case "getAuthorizationState", "setTdlibParameters", "setAuthenticationPhoneNumber", "checkAuthenticationCode", "checkAuthenticationPassword", "setAuthenticationEmailAddress", "checkAuthenticationEmailCode", "requestQrCodeAuthentication", "getMe", "getMessage", "getMessages", "getChat", "getUser", "loadChats", "getChatHistory", "getContacts", "getSupergroupMembers", "getBasicGroupFullInfo", "downloadFile", "getFile", "getOption":
		return true
	default:
		return false
	}
}

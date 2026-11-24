// Package codextest fakes a codex home's background app-server for tests.
package codextest

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Fake is a running fake server; Names records thread/name/set calls.
type Fake struct {
	mu    sync.Mutex
	names map[string]string
}

// Names returns the thread names set so far.
func (f *Fake) Names() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	for k, v := range f.names {
		out[k] = v
	}
	return out
}

// Server answers initialize, thread/loaded/list, thread/read and
// thread/name/set on a unix socket linked from home's app-server-control
// dir, as codex does.
func Server(t *testing.T, home string, loaded []string) *Fake {
	fake := &Fake{names: map[string]string{}}
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "spcx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	if err := os.MkdirAll(filepath.Join(home, "app-server-control"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(socket, filepath.Join(home, "app-server-control", "app-server-control.sock")); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go fake.serve(conn, loaded)
		}
	}()
	return fake
}

func (f *Fake) serve(conn net.Conn, loaded []string) {
	func() {
		defer conn.Close()
		reader := bufio.NewReader(conn)
		if _, err := http.ReadRequest(reader); err != nil {
			return
		}
		io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		for {
			head := make([]byte, 2)
			if _, err := io.ReadFull(reader, head); err != nil {
				return
			}
			n := int(head[1] & 0x7f)
			if n == 126 {
				ext := make([]byte, 2)
				io.ReadFull(reader, ext)
				n = int(binary.BigEndian.Uint16(ext))
			}
			mask := make([]byte, 4)
			io.ReadFull(reader, mask)
			payload := make([]byte, n)
			io.ReadFull(reader, payload)
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
			var request struct {
				ID     *int   `json:"id"`
				Method string `json:"method"`
				Params struct {
					ThreadID string `json:"threadId"`
					Name     string `json:"name"`
				} `json:"params"`
			}
			if json.Unmarshal(payload, &request) != nil || request.ID == nil {
				continue
			}
			result := map[string]any{}
			switch request.Method {
			case "thread/loaded/list":
				result = map[string]any{"data": loaded, "nextCursor": nil}
			case "thread/name/set":
				f.mu.Lock()
				f.names[request.Params.ThreadID] = request.Params.Name
				f.mu.Unlock()
			case "thread/read":
				f.mu.Lock()
				result = map[string]any{"thread": map[string]any{"id": request.Params.ThreadID, "name": f.names[request.Params.ThreadID]}}
				f.mu.Unlock()
			}
			out, _ := json.Marshal(map[string]any{"id": *request.ID, "result": result})
			frame := []byte{0x81}
			if len(out) < 126 {
				frame = append(frame, byte(len(out)))
			} else {
				frame = append(frame, 126, byte(len(out)>>8), byte(len(out)))
			}
			conn.Write(append(frame, out...))
		}
	}()
}

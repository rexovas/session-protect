package guard

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"
)

// Codex runs its conversations through a background app-server, shared by
// the terminal UI and other clients, that listens on a local socket in
// each codex home. Its thread/loaded/list method names the conversations
// it has open — including ones no codex process in a terminal shows, which
// is what a process listing alone would miss.

// codexControlSocket is where a codex home's app-server listens.
func codexControlSocket(codexHome string) string {
	return filepath.Join(codexHome, "app-server-control", "app-server-control.sock")
}

// ErrNoCodexServer means the home's app-server isn't running: nothing is
// open through it.
var ErrNoCodexServer = errors.New("codex app-server not running")

// CodexLoaded asks a codex home's app-server which conversations it has
// loaded. It returns ErrNoCodexServer when no server is listening, and
// another error when one is listening but can't be asked (the caller should
// then treat the answer as unknown, not as "nothing open").
func CodexLoaded(codexHome string) (map[string]bool, error) {
	socket, err := filepath.EvalSymlinks(codexControlSocket(codexHome))
	if err != nil {
		return nil, ErrNoCodexServer
	}
	conn, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return nil, ErrNoCodexServer // stale socket: the server is gone
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	ws, err := wsHandshake(conn)
	if err != nil {
		return nil, err
	}
	if _, err := ws.call(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "session-protect", "version": "1"}}); err != nil {
		return nil, err
	}
	if err := ws.send(map[string]any{"method": "initialized"}); err != nil {
		return nil, err
	}
	loaded := map[string]bool{}
	var cursor *string
	for page := 0; page < 100; page++ {
		params := map[string]any{}
		if cursor != nil {
			params["cursor"] = *cursor
		}
		raw, err := ws.call(2+page, "thread/loaded/list", params)
		if err != nil {
			return nil, err
		}
		var result struct {
			Data       []json.RawMessage `json:"data"`
			NextCursor *string           `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, err
		}
		for _, item := range result.Data {
			var id string
			if json.Unmarshal(item, &id) != nil {
				var thread struct {
					ID string `json:"id"`
				}
				_ = json.Unmarshal(item, &thread)
				id = thread.ID
			}
			if id != "" {
				loaded[id] = true
			}
		}
		if result.NextCursor == nil || *result.NextCursor == "" {
			return loaded, nil
		}
		cursor = result.NextCursor
	}
	return loaded, nil
}

// wsConn is the least of a WebSocket client: text frames out (masked, as
// clients must), text frames in.
type wsConn struct {
	conn   net.Conn
	reader *bufio.Reader
}

func wsHandshake(conn net.Conn) (*wsConn, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(nonce)
	request := "GET /rpc HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\n\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil {
		return nil, err
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSwitchingProtocols {
		return nil, fmt.Errorf("codex app-server refused the connection (%s)", response.Status)
	}
	return &wsConn{conn: conn, reader: reader}, nil
}

func (w *wsConn) send(message any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	header := []byte{0x81}
	switch n := len(data); {
	case n < 126:
		header = append(header, 0x80|byte(n))
	case n < 1<<16:
		header = append(header, 0x80|126, byte(n>>8), byte(n))
	default:
		header = append(header, 0x80|127)
		header = binary.BigEndian.AppendUint64(header, uint64(n))
	}
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return err
	}
	frame := append(header, mask...)
	for i, b := range data {
		frame = append(frame, b^mask[i%4])
	}
	_, err = w.conn.Write(frame)
	return err
}

// next reads the next text message, skipping other frames.
func (w *wsConn) next() ([]byte, error) {
	for {
		head := make([]byte, 2)
		if _, err := io.ReadFull(w.reader, head); err != nil {
			return nil, err
		}
		n := uint64(head[1] & 0x7f)
		switch n {
		case 126:
			ext := make([]byte, 2)
			if _, err := io.ReadFull(w.reader, ext); err != nil {
				return nil, err
			}
			n = uint64(binary.BigEndian.Uint16(ext))
		case 127:
			ext := make([]byte, 8)
			if _, err := io.ReadFull(w.reader, ext); err != nil {
				return nil, err
			}
			n = binary.BigEndian.Uint64(ext)
		}
		if n > 64<<20 {
			return nil, fmt.Errorf("codex app-server sent an oversized frame")
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(w.reader, payload); err != nil {
			return nil, err
		}
		switch head[0] & 0x0f {
		case 0x1:
			return payload, nil
		case 0x8:
			return nil, fmt.Errorf("codex app-server closed the connection")
		}
	}
}

// call sends a request and returns its result, skipping notifications.
func (w *wsConn) call(id int, method string, params any) (json.RawMessage, error) {
	if err := w.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	for {
		payload, err := w.next()
		if err != nil {
			return nil, err
		}
		var message struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(payload, &message) != nil || message.ID == nil || *message.ID != id {
			continue
		}
		if message.Error != nil {
			return nil, fmt.Errorf("codex app-server: %s: %s", method, strings.TrimSpace(message.Error.Message))
		}
		return message.Result, nil
	}
}

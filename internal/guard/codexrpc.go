package guard

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// CodexClient speaks codex's app-server protocol to one codex home.
type CodexClient struct {
	call  func(method string, params any) (json.RawMessage, error)
	close func()
}

// Call sends a request and returns its result.
func (c *CodexClient) Call(method string, params any) (json.RawMessage, error) {
	return c.call(method, params)
}

// Close ends the connection, and stops a server started for it.
func (c *CodexClient) Close() { c.close() }

// OpenCodex connects to a codex home's running app-server, or starts a
// temporary one (`codex app-server` with CODEX_HOME set) when none runs.
func OpenCodex(codexHome string) (*CodexClient, error) {
	if socket, err := filepath.EvalSymlinks(codexControlSocket(codexHome)); err == nil {
		if conn, err := net.DialTimeout("unix", socket, 2*time.Second); err == nil {
			ws, err := wsHandshake(conn)
			if err != nil {
				conn.Close()
				return nil, err
			}
			next := 0
			client := &CodexClient{
				call: func(method string, params any) (json.RawMessage, error) {
					next++
					_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
					return ws.call(next, method, params)
				},
				close: func() { conn.Close() },
			}
			if err := initialize(client, func(m any) error { return ws.send(m) }); err != nil {
				conn.Close()
				return nil, err
			}
			return client, nil
		}
	}
	return startCodex(codexHome)
}

func initialize(client *CodexClient, notify func(any) error) error {
	if _, err := client.Call("initialize", map[string]any{"clientInfo": map[string]string{"name": "session-protect", "version": "1"}}); err != nil {
		return err
	}
	return notify(map[string]any{"method": "initialized"})
}

// startCodex runs a temporary app-server on the home over stdio.
func startCodex(codexHome string) (*CodexClient, error) {
	binary, err := exec.LookPath("codex")
	if err != nil {
		return nil, fmt.Errorf("codex is not on PATH: %w", err)
	}
	cmd := exec.Command(binary, "app-server")
	cmd.Env = append(withoutCodexHome(os.Environ()), "CODEX_HOME="+codexHome)
	cmd.Dir = codexHome
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	lines := make(chan []byte, 64)
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 64<<20)
		for scanner.Scan() {
			lines <- append([]byte(nil), scanner.Bytes()...)
		}
		close(lines)
	}()
	send := func(message any) error {
		data, err := json.Marshal(message)
		if err != nil {
			return err
		}
		_, err = stdin.Write(append(data, '\n'))
		return err
	}
	next := 0
	client := &CodexClient{
		call: func(method string, params any) (json.RawMessage, error) {
			next++
			id := next
			if err := send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
				return nil, err
			}
			timeout := time.After(60 * time.Second)
			for {
				select {
				case line, ok := <-lines:
					if !ok {
						return nil, errors.New("codex app-server exited")
					}
					if result, done, err := matchResponse(line, id, method); done {
						return result, err
					}
				case <-timeout:
					return nil, fmt.Errorf("codex app-server: %s timed out", method)
				}
			}
		},
		close: func() {
			_ = stdin.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		},
	}
	if err := initialize(client, send); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

func matchResponse(line []byte, id int, method string) (json.RawMessage, bool, error) {
	var message struct {
		ID     *int            `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(line, &message) != nil || message.ID == nil || *message.ID != id {
		return nil, false, nil
	}
	if message.Error != nil {
		return nil, true, fmt.Errorf("codex app-server: %s: %s", method, message.Error.Message)
	}
	return message.Result, true, nil
}

func withoutCodexHome(environ []string) []string {
	out := environ[:0:0]
	for _, entry := range environ {
		if len(entry) < len("CODEX_HOME=") || entry[:len("CODEX_HOME=")] != "CODEX_HOME=" {
			out = append(out, entry)
		}
	}
	return out
}

// SetCodexNames gives threads in a codex home their names through codex's
// own API. Codex indexes a moved rollout without the name it had (it keeps
// names in its database, not the rollout), and then auto-titles it on next
// use; this restores them. It waits for each thread to be indexed and
// returns one line per thread it could not name.
func SetCodexNames(codexHome string, names map[string]string) []string {
	if len(names) == 0 {
		return nil
	}
	client, err := OpenCodex(codexHome)
	if err != nil {
		return []string{fmt.Sprintf("couldn't reach codex to restore %d thread name(s): %v", len(names), err)}
	}
	defer client.Close()
	var failures []string
	for id, name := range names {
		indexed := false
		for attempt := 0; attempt < 15; attempt++ {
			if _, err := client.Call("thread/read", map[string]any{"threadId": id}); err == nil {
				indexed = true
				break
			}
			time.Sleep(2 * time.Second)
		}
		if !indexed {
			failures = append(failures, fmt.Sprintf("%s: codex hasn't indexed it yet; name it %q in codex", id, name))
			continue
		}
		if _, err := client.Call("thread/name/set", map[string]any{"threadId": id, "name": name}); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", id, err))
		}
	}
	return failures
}

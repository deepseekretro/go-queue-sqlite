package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
)

// wsConn 封装底层 TCP 连接，实现最小 WebSocket 帧读写
type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
}

// upgradeWS 将 HTTP 连接升级为 WebSocket（RFC 6455）
func upgradeWS(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	key := r.Header.Get("Sec-Websocket-Key")
	if key == "" {
		return nil, fmt.Errorf("missing Sec-Websocket-Key")
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, fmt.Errorf("hijacking not supported")
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		return nil, err
	}

	h := sha1.New()
	h.Write([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	accept := base64.StdEncoding.EncodeToString(h.Sum(nil))

	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	conn.Write([]byte(resp))

	return &wsConn{conn: conn, br: buf.Reader}, nil
}

// ReadMessage 读取一个 WebSocket 帧，返回 (opcode, payload, error)
func (c *wsConn) ReadMessage() (int, []byte, error) {
	header := make([]byte, 2)
	if _, err := readFull(c.br, header); err != nil {
		return 0, nil, err
	}

	opcode := int(header[0] & 0x0F)
	masked := header[1]&0x80 != 0
	payloadLen := int(header[1] & 0x7F)

	switch payloadLen {
	case 126:
		ext := make([]byte, 2)
		readFull(c.br, ext)
		payloadLen = int(ext[0])<<8 | int(ext[1])
	case 127:
		ext := make([]byte, 8)
		readFull(c.br, ext)
		payloadLen = 0
		for _, b := range ext {
			payloadLen = payloadLen<<8 | int(b)
		}
	}

	var maskKey [4]byte
	if masked {
		readFull(c.br, maskKey[:])
	}

	payload := make([]byte, payloadLen)
	readFull(c.br, payload)

	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}

	if opcode == 8 {
		return opcode, nil, fmt.Errorf("websocket: connection closed by client")
	}
	return opcode, payload, nil
}

// WriteMessage 发送一个 WebSocket 文本/二进制帧（服务端不需要 mask）
func (c *wsConn) WriteMessage(opcode int, data []byte) error {
	length := len(data)
	var header []byte
	header = append(header, byte(0x80|opcode))
	switch {
	case length <= 125:
		header = append(header, byte(length))
	case length <= 65535:
		header = append(header, 126, byte(length>>8), byte(length))
	default:
		header = append(header, 127,
			0, 0, 0, 0,
			byte(length>>24), byte(length>>16), byte(length>>8), byte(length))
	}
	frame := append(header, data...)
	_, err := c.conn.Write(frame)
	return err
}

func (c *wsConn) Close() error {
	return c.conn.Close()
}

// readFull 从 bufio.Reader 中精确读取 len(buf) 字节
func readFull(r *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

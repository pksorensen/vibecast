package websocket

import (
	"bufio"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// Connect performs a plain WebSocket handshake to host on the given path.
func Connect(host, path string) (net.Conn, error) {
	conn, err := net.Dial("tcp", host)
	if err != nil {
		return nil, err
	}

	keyBytes := make([]byte, 16)
	rand.Read(keyBytes)
	wsKey := base64.StdEncoding.EncodeToString(keyBytes)

	req, _ := http.NewRequest("GET", "http://"+host+path, nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", wsKey)
	req.Write(conn)

	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ws handshake failed: %w", err)
	}
	if resp.StatusCode != 101 {
		conn.Close()
		return nil, fmt.Errorf("ws handshake: status %d", resp.StatusCode)
	}

	return conn, nil
}

// ConnectWithProtocol connects with an optional subprotocol and TLS support for non-localhost hosts.
func ConnectWithProtocol(host, path, protocol string) (net.Conn, *bufio.Reader, error) {
	isLocal := strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.0.0.1")

	var conn net.Conn
	var err error

	if isLocal {
		conn, err = net.Dial("tcp", host)
	} else {
		dialHost := host
		if !strings.Contains(host, ":") {
			dialHost = host + ":443"
		}
		conn, err = tls.Dial("tcp", dialHost, &tls.Config{ServerName: strings.Split(host, ":")[0]})
	}
	if err != nil {
		return nil, nil, err
	}

	keyBytes := make([]byte, 16)
	rand.Read(keyBytes)
	wsKey := base64.StdEncoding.EncodeToString(keyBytes)

	httpHost := host

	scheme := "http"
	if !isLocal {
		scheme = "https"
	}

	req, _ := http.NewRequest("GET", scheme+"://"+httpHost+path, nil)
	req.Header.Set("Host", httpHost)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", wsKey)
	if protocol != "" {
		req.Header.Set("Sec-WebSocket-Protocol", protocol)
	}
	req.Write(conn)

	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		conn.Close()
		return nil, nil, fmt.Errorf("ws handshake failed: %w", err)
	}
	if resp.StatusCode != 101 {
		conn.Close()
		return nil, nil, fmt.Errorf("ws handshake: status %d", resp.StatusCode)
	}

	return conn, reader, nil
}

// SendText sends a WebSocket text frame.
func SendText(conn net.Conn, data []byte) error {
	return SendFrame(conn, 0x01, data)
}

// SendBinary sends a WebSocket binary frame.
func SendBinary(conn net.Conn, data []byte) error {
	return SendFrame(conn, 0x02, data)
}

// SendPong sends a WebSocket pong frame.
func SendPong(conn net.Conn, data []byte) error {
	return SendFrame(conn, 0x0A, data)
}

// SendFrame sends a masked WebSocket frame with the given opcode.
func SendFrame(conn net.Conn, opcode byte, data []byte) error {
	frame := []byte{0x80 | opcode}
	length := len(data)
	if length < 126 {
		frame = append(frame, byte(length)|0x80)
	} else if length < 65536 {
		frame = append(frame, 126|0x80, byte(length>>8), byte(length&0xff))
	} else {
		frame = append(frame, 127|0x80)
		lenBytes := make([]byte, 8)
		binary.BigEndian.PutUint64(lenBytes, uint64(length))
		frame = append(frame, lenBytes...)
	}

	mask := make([]byte, 4)
	rand.Read(mask)
	frame = append(frame, mask...)

	masked := make([]byte, length)
	for i := 0; i < length; i++ {
		masked[i] = data[i] ^ mask[i%4]
	}
	frame = append(frame, masked...)

	_, err := conn.Write(frame)
	return err
}

// ReadFrame reads a single WebSocket frame.
func ReadFrame(reader *bufio.Reader) (opcode byte, payload []byte, err error) {
	header := make([]byte, 2)
	if _, err = io.ReadFull(reader, header); err != nil {
		return 0, nil, err
	}

	opcode = header[0] & 0x0f
	masked := header[1]&0x80 != 0
	payloadLen := uint64(header[1] & 0x7f)

	if payloadLen == 126 {
		ext := make([]byte, 2)
		if _, err = io.ReadFull(reader, ext); err != nil {
			return 0, nil, err
		}
		payloadLen = uint64(binary.BigEndian.Uint16(ext))
	} else if payloadLen == 127 {
		ext := make([]byte, 8)
		if _, err = io.ReadFull(reader, ext); err != nil {
			return 0, nil, err
		}
		payloadLen = binary.BigEndian.Uint64(ext)
	}

	var mask []byte
	if masked {
		mask = make([]byte, 4)
		if _, err = io.ReadFull(reader, mask); err != nil {
			return 0, nil, err
		}
	}

	payload = make([]byte, payloadLen)
	if _, err = io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}

	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}

	return opcode, payload, nil
}

// ReadMessage reads a WebSocket message payload.
func ReadMessage(reader *bufio.Reader) ([]byte, error) {
	_, payload, err := ReadFrame(reader)
	return payload, err
}

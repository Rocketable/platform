package discordvoice

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hpke"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDaveMediaFrameRoundTrip(t *testing.T) {
	session := &daveSession{mediaSecret: bytes.Repeat([]byte{0x42}, mlsHashSize)}
	frame := []byte{0x11, 0x22, 0x33, 0x44}

	sealed, err := session.encryptOpus("123", frame)
	require.NoError(t, err)
	require.NotEqual(t, frame, sealed)

	opened, err := session.decryptOpus("123", sealed)
	require.NoError(t, err)
	assert.Equal(t, frame, opened)
}

func TestDaveOpusPassthroughWithoutMediaSecret(t *testing.T) {
	session := new(daveSession)
	silence := []byte{daveOpusSilence0, daveOpusSilence1, daveOpusSilence2}
	frame := []byte{0x11, 0x22}

	got, err := session.encryptOpus("123", silence)
	require.NoError(t, err)
	assert.Equal(t, silence, got)

	got, err = session.decryptOpus("123", silence)
	require.NoError(t, err)
	assert.Equal(t, silence, got)

	got, err = session.encryptOpus("123", frame)
	require.NoError(t, err)
	assert.Equal(t, frame, got)

	got, err = session.decryptOpus("123", frame)
	require.NoError(t, err)
	assert.Equal(t, frame, got)
}

func TestDaveMediaKeyRejectsInvalidUserID(t *testing.T) {
	session := new(daveSession)

	_, err := session.mediaKey("not-a-user-id", 0)
	require.ErrorContains(t, err, "parse Discord user ID for DAVE media key")
}

func TestDaveLEB128RoundTrip(t *testing.T) {
	for _, value := range []uint32{0, 1, 127, 128, 16_384, 1<<24 + 7} {
		encoded := appendDaveLEB128(nil, value)
		got, n, err := readDaveLEB128(encoded)
		require.NoError(t, err)
		assert.Equal(t, value, got)
		assert.Equal(t, len(encoded), n)
	}

	_, _, err := readDaveLEB128([]byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x00})
	require.ErrorContains(t, err, "DAVE media LEB128 is too long")
}

func TestRTPPayloadAEADRoundTrip(t *testing.T) {
	header := []byte{0x80, rtpPayloadTypeOpus, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3}
	secret := bytes.Repeat([]byte{0x24}, 32)
	opus := []byte{0x01, 0x02, 0x03}

	sealed, err := sealRTPPayload(rtpEncryptionModeAES256GCMRTPSize, secret, header, opus, 1)
	require.NoError(t, err)

	opened, err := openRTPPayload(rtpEncryptionModeAES256GCMRTPSize, secret, header, sealed)
	require.NoError(t, err)
	assert.Equal(t, opus, opened)
}

func TestVoiceConnectionRTPPacketRoundTripWithDAVE(t *testing.T) {
	v := newVoiceConnection(nil, "guild", "channel", true, false)
	copy(v.secret[:], bytes.Repeat([]byte{0x24}, 32))
	v.mode = rtpEncryptionModeAES256GCMRTPSize
	v.dave = &daveSession{mediaSecret: bytes.Repeat([]byte{0x42}, mlsHashSize)}
	v.userID = "123"
	v.ssrc = 42
	v.sequence = 7
	v.timestamp = 480
	v.ssrcUsers[42] = "123"

	opus := []byte{0x11, 0x22, 0x33}
	packet, err := v.encryptRTPPacket(opus)
	require.NoError(t, err)

	headerLen, err := rtpAEADHeaderLen(packet)
	require.NoError(t, err)
	inner, err := openRTPPayload(v.mode, v.secret[:], packet[:headerLen], packet[headerLen:])
	require.NoError(t, err)
	require.NotEqual(t, opus, inner)
	_, err = parseDaveMediaFrame(inner)
	require.NoError(t, err)

	got, err := v.decryptRTPPacket(packet)
	require.NoError(t, err)
	assert.Equal(t, opus, got.Opus)
	assert.Equal(t, byte(0x80), got.Flags)
	assert.Equal(t, byte(rtpPayloadTypeOpus), got.PayloadType)
	assert.Equal(t, uint16(7), got.Sequence)
	assert.Equal(t, uint32(480), got.Timestamp)
	assert.Equal(t, uint32(42), got.SSRC)
}

func TestVoiceConnectionRTPPacketErrors(t *testing.T) {
	v := newVoiceConnection(nil, "guild", "channel", true, false)
	copy(v.secret[:], bytes.Repeat([]byte{0x24}, 32))
	v.mode = rtpEncryptionModeAES256GCMRTPSize
	v.ssrc = 42
	v.userID = "123"

	_, err := v.encryptRTPPacket(nil)
	require.ErrorContains(t, err, "discord voice Opus packet is empty")

	v.dave = &daveSession{mediaSecret: bytes.Repeat([]byte{0x42}, mlsHashSize)}
	v.userID = "not-a-user-id"
	_, err = v.encryptRTPPacket([]byte{0x11})
	require.ErrorContains(t, err, "parse Discord user ID for DAVE media key")

	v.dave = nil
	v.userID = "123"
	v.mode = "unsupported"
	_, err = v.encryptRTPPacket([]byte{0x11})
	require.ErrorContains(t, err, "unsupported Discord voice encryption mode")

	v.mode = rtpEncryptionModeAES256GCMRTPSize
	_, err = v.decryptRTPPacket([]byte{0x80})
	require.ErrorContains(t, err, "discord voice RTP packet is too short")

	shortPacket := []byte{0x80, rtpPayloadTypeOpus, 0, 1, 0, 0, 0, 2, 0, 0, 0, 42}
	_, err = v.decryptRTPPacket(shortPacket)
	require.ErrorContains(t, err, "discord voice RTP packet is too short")

	header := []byte{0x80, rtpPayloadTypeOpus, 0, 1, 0, 0, 0, 2, 0, 0, 0, 42}
	sealed, err := sealRTPPayload(v.mode, v.secret[:], header, []byte{0x11, 0x22, 0x33}, 1)
	require.NoError(t, err)

	v.dave = &daveSession{mediaSecret: bytes.Repeat([]byte{0x42}, mlsHashSize)}
	v.ssrcUsers[42] = "123"
	_, err = v.decryptRTPPacket(slices.Concat(header, sealed))
	require.ErrorContains(t, err, "DAVE media frame")
}

func TestRTPHeaderLengthAndExtensionStripping(t *testing.T) {
	if _, err := rtpAEADHeaderLen([]byte{0x80}); err == nil {
		t.Fatal("rtpAEADHeaderLen(short packet) succeeded")
	}

	header := []byte{0x90, rtpPayloadTypeOpus, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3, 0xbe, 0xde, 0, 1}

	got, err := rtpAEADHeaderLen(header)
	if err != nil {
		t.Fatalf("rtpAEADHeaderLen(extension header) returned error: %v", err)
	}

	if got != len(header) {
		t.Fatalf("rtpAEADHeaderLen(extension header) = %d; want %d", got, len(header))
	}

	if _, err := rtpAEADHeaderLen(header[:14]); err == nil {
		t.Fatal("rtpAEADHeaderLen(truncated extension) succeeded")
	}

	opus := []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee}
	if got := stripRTPPayloadExtension(header, opus); !bytes.Equal(got, opus[4:]) {
		t.Fatalf("stripRTPPayloadExtension() = %x; want %x", got, opus[4:])
	}

	if got := stripRTPPayloadExtension(header[:12], opus); !bytes.Equal(got, opus) {
		t.Fatalf("stripRTPPayloadExtension(no extension) = %x; want %x", got, opus)
	}

	if got := stripRTPPayloadExtension(header, opus[:4]); !bytes.Equal(got, opus[:4]) {
		t.Fatalf("stripRTPPayloadExtension(oversized extension) = %x; want %x", got, opus[:4])
	}
}

func TestRTPPayloadAEADErrors(t *testing.T) {
	header := []byte{0x80, rtpPayloadTypeOpus, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3}
	secret := bytes.Repeat([]byte{0x24}, 32)
	opus := []byte{0x01, 0x02, 0x03}

	if _, err := sealRTPPayload("unsupported", secret, header, opus, 1); err == nil {
		t.Fatal("sealRTPPayload(unsupported mode) succeeded")
	}

	if _, err := sealRTPPayload(rtpEncryptionModeAES256GCMRTPSize, secret[:8], header, opus, 1); err == nil {
		t.Fatal("sealRTPPayload(short AES key) succeeded")
	}

	if _, err := openRTPPayload("unsupported", secret, header, []byte{0, 0, 0, 0}); err == nil {
		t.Fatal("openRTPPayload(unsupported mode) succeeded")
	}

	if _, err := openRTPPayload(rtpEncryptionModeXChaCha20Poly1305RTPSize, secret[:8], header, []byte{0, 0, 0, 0}); err == nil {
		t.Fatal("openRTPPayload(short XChaCha key) succeeded")
	}

	sealed, err := sealRTPPayload(rtpEncryptionModeAES256GCMRTPSize, secret, header, opus, 1)
	if err != nil {
		t.Fatalf("sealRTPPayload() returned error: %v", err)
	}

	sealed[0] ^= 1
	if _, err := openRTPPayload(rtpEncryptionModeAES256GCMRTPSize, secret, header, sealed); err == nil {
		t.Fatal("openRTPPayload(tampered ciphertext) succeeded")
	}
}

func TestVoiceConnectionStateTransitions(t *testing.T) {
	v := newVoiceConnection(nil, "guild", "channel", true, false)
	v.setSpeakerID(42, "human")

	if got := v.speakerID(42); got != "human" {
		t.Fatalf("speakerID(42) = %q; want human", got)
	}

	if err := v.setSpeaking(true); err != nil {
		t.Fatalf("setSpeaking(true) returned error: %v", err)
	}

	v.cond.L.Lock()
	speaking := v.speaking
	v.cond.L.Unlock()

	if !speaking {
		t.Fatal("setSpeaking(true) left speaking false")
	}

	errDead := errors.New("dead")
	v.die(errDead)

	select {
	case <-v.dead:
	default:
		t.Fatal("die() did not close dead channel")
	}

	v.cond.L.Lock()
	status := v.status
	errGot := v.err
	v.cond.L.Unlock()

	if status != voiceConnectionStatusDead {
		t.Fatalf("voiceConnection status = %v; want %v", status, voiceConnectionStatusDead)
	}

	if errGot != errDead {
		t.Fatalf("voiceConnection error = %v; want %v", errGot, errDead)
	}

	v.die(errors.New("second"))
	v.cond.L.Lock()
	errGot = v.err
	v.cond.L.Unlock()

	if errGot != errDead {
		t.Fatalf("second die() error = %v; want %v", errGot, errDead)
	}
}

func TestVoiceConnectionHandleSpeakingEventRecordsSpeaker(t *testing.T) {
	v := newVoiceConnection(nil, "guild", "channel", true, false)
	data, err := json.Marshal(voiceSpeakingUpdate{UserID: "human", SSRC: 42, Speaking: 1})
	require.NoError(t, err)

	v.handleVoiceEvent(nil, voiceOpSpeaking, data)

	if got := v.speakerID(42); got != "human" {
		t.Fatalf("speakerID(42) = %q; want human", got)
	}
}

func TestVoiceConnectionGatewayWritesRejectDisconnectedConnection(t *testing.T) {
	v := newVoiceConnection(nil, "guild", "channel", true, false)
	v.die(nil)

	require.ErrorContains(t, v.writeVoiceHeartbeat(nil), "discord voice gateway is not connected")
	require.ErrorContains(t, v.writeVoiceJSON(nil, map[string]string{"kind": "heartbeat"}), "discord voice gateway is not connected")
	require.ErrorContains(t, v.writeVoiceBinary(nil, daveMlsKeyPackageOp, []byte{0xaa}), "discord voice gateway is not connected")
}

func TestGatewayOpenAndJoinErrorsStayLocal(t *testing.T) {
	errDial := errors.New("dial disabled")
	s := &wire{
		dialer: &websocket.Dialer{NetDialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, errDial
		}},
		log:   slog.New(slog.DiscardHandler),
		ready: make(chan struct{}),
	}

	err := s.openGateway()
	require.Error(t, err)
	require.ErrorIs(t, err, errDial)
	require.ErrorContains(t, err, "dial Discord gateway")

	_, err = s.joinVoice(t.Context(), "guild", "channel", false, false)
	require.ErrorContains(t, err, "discord gateway is not connected")
}

func TestGatewayEventAndHeartbeatEdges(t *testing.T) {
	s := &wire{log: slog.New(slog.DiscardHandler), identify: identify{Intents: 3}, ready: make(chan struct{})}

	s.handleGatewayEvent(nil, gatewayOpHeartbeat, "", nil)
	s.handleGatewayEvent(nil, gatewayOpIdentify, "", nil)
	s.handleGatewayEvent(nil, gatewayOpVoiceStateUpdate, "", nil)
	s.handleGatewayEvent(nil, gatewayOpDispatch, "UNKNOWN", nil)
	s.handleGatewayEvent(nil, gatewayOpHello, "", []byte(`{"heartbeat_interval":0}`))
	s.gatewayHeartbeat(nil, 0)
	assert.Nil(t, s.user)

	select {
	case <-s.ready:
		t.Fatal("gateway ready channel closed without READY dispatch")
	default:
	}
}

func TestGatewayHeartbeatSendsUntilWireCloses(t *testing.T) {
	type heartbeatPayload struct {
		Op gatewayOpcode `json:"op"`
		D  any           `json:"d"`
	}

	payloads := make(chan heartbeatPayload, 1)
	errs := make(chan error, 1)
	upgrader := websocket.Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errs <- err
			return
		}

		defer func() { _ = conn.Close() }()

		var payload heartbeatPayload
		if err := conn.ReadJSON(&payload); err != nil {
			errs <- err
			return
		}

		payloads <- payload
	}))
	defer server.Close()

	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}

	require.NoError(t, err)

	s := &wire{log: slog.New(slog.DiscardHandler), wsConn: conn}
	done := make(chan struct{})

	go func() {
		s.gatewayHeartbeat(conn, time.Millisecond)
		close(done)
	}()

	defer func() {
		_ = conn.Close()

		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()

		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("gateway heartbeat did not stop after wire close")
		}
	}()

	select {
	case err := <-errs:
		t.Fatalf("gateway heartbeat server error: %v", err)
	case payload := <-payloads:
		assert.Equal(t, gatewayOpHeartbeat, payload.Op)
		assert.Nil(t, payload.D)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for gateway heartbeat")
	}
}

func TestVoiceConnectionConnectHeartbeatAndDAVEEdges(t *testing.T) {
	v := newVoiceConnection(&wire{log: slog.New(slog.DiscardHandler)}, "guild", "channel", false, false)

	v.voiceHeartbeat(nil, 0)
	v.cond.L.Lock()
	status := v.status
	v.cond.L.Unlock()
	assert.Equal(t, voiceConnectionStatusNew, status)

	err := v.connect(t.Context(), "123", "session", nil)
	require.ErrorContains(t, err, "discord voice server endpoint missing")

	v.cond.L.Lock()
	v.userID = "not-a-user-id"
	v.cond.L.Unlock()
	err = v.startDAVE(nil, 1)
	require.ErrorContains(t, err, "parse Discord user ID for DAVE MLS credential")
	assert.Nil(t, v.dave)
}

func TestVoiceConnectionWritesGatewayMessages(t *testing.T) {
	type heartbeatPayload struct {
		Op voiceOpcode `json:"op"`
		D  struct {
			SeqAck uint16 `json:"seq_ack"`
		} `json:"d"`
	}

	type speakingPayload struct {
		Op voiceOpcode `json:"op"`
		D  struct {
			Speaking int    `json:"speaking"`
			Delay    int    `json:"delay"`
			SSRC     uint32 `json:"ssrc"`
		} `json:"d"`
	}

	type serverResult struct {
		heartbeat heartbeatPayload
		speaking  speakingPayload
		binary    []byte
		err       error
	}

	results := make(chan serverResult, 1)
	upgrader := websocket.Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			results <- serverResult{err: err}
			return
		}

		defer func() { _ = conn.Close() }()

		var heartbeat heartbeatPayload
		if err := conn.ReadJSON(&heartbeat); err != nil {
			results <- serverResult{err: err}
			return
		}

		messageType, data, err := conn.ReadMessage()
		if err != nil {
			results <- serverResult{err: err}
			return
		}

		if messageType != websocket.BinaryMessage {
			results <- serverResult{err: errors.New("expected binary gateway message")}
			return
		}

		var speaking speakingPayload
		if err := conn.ReadJSON(&speaking); err != nil {
			results <- serverResult{err: err}
			return
		}

		results <- serverResult{heartbeat: heartbeat, speaking: speaking, binary: data}
	}))
	defer server.Close()

	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}

	require.NoError(t, err)

	defer func() { _ = conn.Close() }()

	v := newVoiceConnection(nil, "guild", "channel", true, false)
	v.wsConn = conn
	v.seqAck = 42
	v.ssrc = 43

	require.NoError(t, v.writeVoiceHeartbeat(conn))
	require.NoError(t, v.writeVoiceBinary(conn, daveMlsKeyPackageOp, []byte{0xaa, 0xbb}))
	require.NoError(t, v.setSpeaking(true))

	select {
	case result := <-results:
		require.NoError(t, result.err)
		assert.Equal(t, voiceOpHeartbeat, result.heartbeat.Op)
		assert.Equal(t, uint16(42), result.heartbeat.D.SeqAck)
		assert.Equal(t, []byte{daveMlsKeyPackageOp, 0xaa, 0xbb}, result.binary)
		assert.Equal(t, voiceOpSpeaking, result.speaking.Op)
		assert.Equal(t, 1, result.speaking.D.Speaking)
		assert.Equal(t, 0, result.speaking.D.Delay)
		assert.Equal(t, uint32(43), result.speaking.D.SSRC)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for voice gateway writes")
	}
}

func TestGatewayReadLoopDispatchesReady(t *testing.T) {
	done := make(chan error, 1)
	upgrader := websocket.Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			done <- err
			return
		}

		defer func() { _ = conn.Close() }()

		done <- conn.WriteJSON(struct {
			Op gatewayOpcode `json:"op"`
			T  string        `json:"t"`
			D  struct {
				User user `json:"user"`
			} `json:"d"`
		}{Op: gatewayOpDispatch, T: "READY", D: struct {
			User user `json:"user"`
		}{User: user{ID: "bot-user"}}})
	}))
	defer server.Close()

	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}

	require.NoError(t, err)

	defer func() { _ = conn.Close() }()

	s := &wire{log: slog.New(slog.DiscardHandler), ready: make(chan struct{})}
	go s.gatewayReadLoop(conn)

	doneChecked := false

	select {
	case <-s.ready:
	case err := <-done:
		doneChecked = true

		require.NoError(t, err)

		select {
		case <-s.ready:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for gateway READY dispatch")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for gateway READY dispatch")
	}

	if !doneChecked {
		require.NoError(t, <-done)
	}

	s.mu.Lock()
	got := s.user
	s.mu.Unlock()
	require.NotNil(t, got)
	assert.Equal(t, "bot-user", got.ID)
}

func TestVoiceReadLoopHandlesTextAndBinaryMessages(t *testing.T) {
	done := make(chan error, 1)
	upgrader := websocket.Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			done <- err
			return
		}

		defer func() { _ = conn.Close() }()

		if err := conn.WriteJSON(struct {
			Op voiceOpcode         `json:"op"`
			D  voiceSpeakingUpdate `json:"d"`
		}{Op: voiceOpSpeaking, D: voiceSpeakingUpdate{UserID: "human", SSRC: 42, Speaking: 1}}); err != nil {
			done <- err
			return
		}

		done <- conn.WriteMessage(websocket.BinaryMessage, []byte{0x12, 0x34, 0x00})
	}))
	defer server.Close()

	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}

	require.NoError(t, err)

	defer func() { _ = conn.Close() }()

	v := newVoiceConnection(nil, "guild", "channel", true, false)
	go v.voiceReadLoop(conn)

	doneChecked := false

	select {
	case <-v.dead:
	case err := <-done:
		doneChecked = true

		require.NoError(t, err)

		select {
		case <-v.dead:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for voice read loop to close")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for voice read loop")
	}

	if !doneChecked {
		require.NoError(t, <-done)
	}

	assert.Equal(t, "human", v.speakerID(42))

	v.cond.L.Lock()
	seqAck := v.seqAck
	status := v.status
	errGot := v.err
	v.cond.L.Unlock()

	assert.Equal(t, uint16(0x1234), seqAck)
	assert.Equal(t, voiceConnectionStatusDead, status)
	require.ErrorContains(t, errGot, "read Discord voice gateway")
}

func TestVoiceConnectionRTPPacketRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		mode rtpEncryptionMode
	}{
		{"aes_gcm", rtpEncryptionModeAES256GCMRTPSize},
		{"xchacha20_poly1305", rtpEncryptionModeXChaCha20Poly1305RTPSize},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := newVoiceConnection(nil, "guild", "channel", true, false)
			v.mode = tt.mode
			copy(v.secret[:], bytes.Repeat([]byte{0x36}, len(v.secret)))
			v.userID = "speaker"
			v.ssrc = 0x10203040
			v.sequence = 7
			v.timestamp = 9
			v.nonce = 11
			v.ssrcUsers[v.ssrc] = v.userID

			opus := []byte{0x11, 0x22, 0x33, 0x44}
			packet, err := v.encryptRTPPacket(opus)
			require.NoError(t, err)
			require.NotEqual(t, opus, packet)

			got, err := v.decryptRTPPacket(packet)
			require.NoError(t, err)
			assert.Equal(t, byte(0x80), got.Flags)
			assert.Equal(t, byte(rtpPayloadTypeOpus), got.PayloadType)
			assert.Equal(t, uint16(7), got.Sequence)
			assert.Equal(t, uint32(9), got.Timestamp)
			assert.Equal(t, uint32(0x10203040), got.SSRC)
			assert.Equal(t, opus, got.Opus)
			assert.Equal(t, uint16(8), v.sequence)
			assert.Equal(t, uint32(9+rtpTimestampStep), v.timestamp)
			assert.Equal(t, uint32(12), v.nonce)
		})
	}
}

func TestWireForwardPacketsAssumesOnlyConfiguredHuman(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	v := newVoiceConnection(nil, "guild", "channel", true, false)

	v.opusRecv = make(chan *opusPacket, 8)
	defer close(v.opusRecv)

	w := &wire{
		voiceChannelID: "channel",
		humanUserID:    "human",
		events:         make(chan voiceEvent, 8),
		user:           &user{ID: "bot"},
		voiceConn:      v,
		voiceStates: map[string][]*voiceStateUpdate{
			"guild": {
				{GuildID: "guild", ChannelID: "channel", UserID: "bot"},
				{GuildID: "guild", ChannelID: "channel", UserID: "human"},
			},
		},
	}

	go w.forwardPackets(ctx, v, "guild")

	v.opusRecv <- nil

	v.opusRecv <- &opusPacket{SSRC: 1}

	v.opusRecv <- &opusPacket{SSRC: 1, Opus: []byte{daveOpusSilence0, daveOpusSilence1, daveOpusSilence2}}

	v.setSpeakerID(2, "other")

	v.opusRecv <- &opusPacket{SSRC: 2, Opus: []byte{0x99}}

	opus := []byte{0x11, 0x22, 0x33}
	v.opusRecv <- &opusPacket{Flags: 0x80, PayloadType: rtpPayloadTypeOpus, Sequence: 7, Timestamp: 9, SSRC: 3, Opus: opus}

	event := receiveVoiceEvent(t, w.events)

	if event.err != nil {
		t.Fatalf("forwardPackets() event error = %v; want nil", event.err)
	}

	if event.packet.SpeakerID != "human" {
		t.Fatalf("forwardPackets() speaker = %q; want human", event.packet.SpeakerID)
	}

	if event.packet.SSRC != 3 || event.packet.Sequence != 7 || event.packet.Timestamp != 9 {
		t.Fatalf("forwardPackets() packet = %+v; want SSRC 3 sequence 7 timestamp 9", event.packet)
	}

	opus[0] = 0xff
	if bytes.Equal(event.packet.Opus, opus) {
		t.Fatal("forwardPackets() did not clone Opus payload")
	}

	if got := v.speakerID(3); got != "human" {
		t.Fatalf("speakerID(3) = %q; want human", got)
	}

	w.mu.Lock()
	w.voiceStates["guild"] = append(w.voiceStates["guild"], &voiceStateUpdate{GuildID: "guild", ChannelID: "channel", UserID: "other"})
	w.mu.Unlock()
	v.setSpeakerID(4, "human")

	v.opusRecv <- &opusPacket{SSRC: 5, Opus: []byte{0x55}}

	v.opusRecv <- &opusPacket{SSRC: 4, Opus: []byte{0x44}}

	event = receiveVoiceEvent(t, w.events)

	if event.packet.SSRC != 4 {
		t.Fatalf("forwardPackets() emitted SSRC %d; want 4", event.packet.SSRC)
	}
}

func TestWireForwardPacketsStopsOnClosedReceiver(t *testing.T) {
	v := newVoiceConnection(nil, "guild", "channel", true, false)
	close(v.opusRecv)

	w := &wire{events: make(chan voiceEvent, 1)}
	done := make(chan struct{})

	go func() {
		w.forwardPackets(t.Context(), v, "guild")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("forwardPackets() did not stop after receiver closed")
	}
}

func TestWireWatchVoiceConnectionPublishesError(t *testing.T) {
	w := &wire{events: make(chan voiceEvent, 1)}
	v := newVoiceConnection(nil, "guild", "channel", true, false)
	errDead := errors.New("dead")

	go w.watchVoiceConnection(v)

	v.die(errDead)

	event := receiveVoiceEvent(t, w.events)

	if event.err != errDead {
		t.Fatalf("watchVoiceConnection() error = %v; want %v", event.err, errDead)
	}
}

func TestWireWatchVoiceConnectionIgnoresCleanClose(t *testing.T) {
	w := &wire{events: make(chan voiceEvent, 1)}
	v := newVoiceConnection(nil, "guild", "channel", true, false)
	v.die(nil)

	w.watchVoiceConnection(v)

	select {
	case event := <-w.events:
		t.Fatalf("watchVoiceConnection() event = %+v; want none", event)
	default:
	}
}

func TestWireCloseMarksConnectionsDead(t *testing.T) {
	v := newVoiceConnection(nil, "guild", "channel", true, false)
	w := &wire{voiceConnections: map[string]*voiceConnection{"guild": v}}

	require.NoError(t, w.close())

	select {
	case <-v.dead:
	default:
		t.Fatal("close() did not close voice connection dead channel")
	}

	if err := v.validate(); err == nil {
		t.Fatal("validate() after close succeeded")
	}
}

func TestWireRESTJSONSendsAuthBodyAndDecodes(t *testing.T) {
	w := &wire{
		token: "Bot token",
		client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost {
				t.Fatalf("restJSON() method = %q; want %q", req.Method, http.MethodPost)
			}

			if req.URL.Path != "/api/v10/channels/123" {
				t.Fatalf("restJSON() path = %q; want /api/v10/channels/123", req.URL.Path)
			}

			if got, want := req.Header.Get("Authorization"), "Bot token"; got != want {
				t.Fatalf("restJSON() authorization = %q; want %q", got, want)
			}

			if got, want := req.Header.Get("Content-Type"), "application/json"; got != want {
				t.Fatalf("restJSON() content type = %q; want %q", got, want)
			}

			body, errRead := io.ReadAll(req.Body)
			if errRead != nil {
				t.Fatalf("read request body: %v", errRead)
			}

			if got, want := string(body), `{"name":"voice"}`; got != want {
				t.Fatalf("restJSON() body = %s; want %s", got, want)
			}

			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(bytes.NewBufferString(`{"id":"123","guild_id":"guild"}`)), Header: make(http.Header)}, nil
		})},
	}

	var got channel

	err := w.restJSON(http.MethodPost, "/channels/123", map[string]string{"name": "voice"}, &got)
	require.NoError(t, err)
	assert.Equal(t, channel{ID: "123", GuildID: "guild"}, got)
}

func TestWireChannelResolvesDiscordChannel(t *testing.T) {
	w := &wire{
		token: "Bot token",
		client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet {
				t.Fatalf("channel() method = %q; want %q", req.Method, http.MethodGet)
			}

			if req.URL.Path != "/api/v10/channels/voice-1" {
				t.Fatalf("channel() path = %q; want /api/v10/channels/voice-1", req.URL.Path)
			}

			if got, want := req.Header.Get("Authorization"), "Bot token"; got != want {
				t.Fatalf("channel() authorization = %q; want %q", got, want)
			}

			return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(bytes.NewBufferString(`{"id":"voice-1","guild_id":"guild-1"}`)), Header: make(http.Header)}, nil
		})},
	}

	got, err := w.channel("voice-1")
	require.NoError(t, err)
	assert.Equal(t, &channel{ID: "voice-1", GuildID: "guild-1"}, got)
}

func TestWireGatewayPayloadsSendVoiceStateAndIdentify(t *testing.T) {
	messages := make(chan json.RawMessage, 3)
	errs := make(chan error, 1)
	upgrader := websocket.Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errs <- err
			return
		}

		defer func() { _ = conn.Close() }()

		for range 3 {
			_, data, err := conn.ReadMessage()
			if err != nil {
				errs <- err
				return
			}

			messages <- slices.Clone(data)
		}
	}))
	defer server.Close()

	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}

	require.NoError(t, err)

	defer func() { _ = conn.Close() }()

	w := &wire{identify: identify{Intents: 129}, token: "Bot test-token", wsConn: conn, log: slog.New(slog.DiscardHandler)}
	require.NoError(t, w.sendVoiceStateUpdate("guild-1", "voice-1", true, false))
	require.NoError(t, w.sendVoiceStateUpdate("guild-1", "", false, true))
	w.sendIdentify()

	nextMessage := func() json.RawMessage {
		t.Helper()

		select {
		case err := <-errs:
			t.Fatalf("gateway server error: %v", err)
		case message := <-messages:
			return message
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for gateway payload")
		}

		return nil
	}

	type voiceStatePayload struct {
		Op gatewayOpcode `json:"op"`
		D  struct {
			GuildID   string  `json:"guild_id"`
			ChannelID *string `json:"channel_id"`
			SelfMute  bool    `json:"self_mute"`
			SelfDeaf  bool    `json:"self_deaf"`
		} `json:"d"`
	}

	var join voiceStatePayload
	require.NoError(t, json.Unmarshal(nextMessage(), &join))
	assert.Equal(t, gatewayOpVoiceStateUpdate, join.Op)
	assert.Equal(t, "guild-1", join.D.GuildID)
	require.NotNil(t, join.D.ChannelID)
	assert.Equal(t, "voice-1", *join.D.ChannelID)
	assert.True(t, join.D.SelfMute)
	assert.False(t, join.D.SelfDeaf)

	var leave voiceStatePayload
	require.NoError(t, json.Unmarshal(nextMessage(), &leave))
	assert.Equal(t, gatewayOpVoiceStateUpdate, leave.Op)
	assert.Equal(t, "guild-1", leave.D.GuildID)
	assert.Nil(t, leave.D.ChannelID)
	assert.False(t, leave.D.SelfMute)
	assert.True(t, leave.D.SelfDeaf)

	var identifyPayload struct {
		Op gatewayOpcode `json:"op"`
		D  struct {
			Token      string            `json:"token"`
			Intents    int               `json:"intents"`
			Properties map[string]string `json:"properties"`
		} `json:"d"`
	}
	require.NoError(t, json.Unmarshal(nextMessage(), &identifyPayload))
	assert.Equal(t, gatewayOpIdentify, identifyPayload.Op)
	assert.Equal(t, "test-token", identifyPayload.D.Token)
	assert.Equal(t, 129, identifyPayload.D.Intents)
	assert.Equal(t, map[string]string{"os": "darwin", "browser": "rocketclaw", "device": "rocketclaw"}, identifyPayload.D.Properties)
}

func TestWireRESTJSONReportsResponseFailures(t *testing.T) {
	responses := []*http.Response{
		{StatusCode: http.StatusInternalServerError, Status: "500 Internal Server Error", Body: io.NopCloser(bytes.NewBufferString("nope")), Header: make(http.Header)},
		{StatusCode: http.StatusOK, Status: "200 OK", Body: io.NopCloser(bytes.NewBufferString("not-json")), Header: make(http.Header)},
		{StatusCode: http.StatusNoContent, Status: "204 No Content", Body: io.NopCloser(bytes.NewReader(nil)), Header: make(http.Header)},
	}
	w := &wire{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		response := responses[0]
		responses = responses[1:]

		return response, nil
	})}}

	err := w.restJSON(http.MethodGet, "/bad", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "discord API GET /bad failed: 500 Internal Server Error: nope")

	err = w.restJSON(http.MethodGet, "/bad-json", nil, &channel{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decode Discord API response")

	require.NoError(t, w.restJSON(http.MethodDelete, "/ok", nil, nil))
}

func TestWireRESTJSONReportsLocalFailures(t *testing.T) {
	err := (&wire{}).restJSON("BAD METHOD", "/bad", nil, nil)
	require.ErrorContains(t, err, "create Discord API request")

	err = (&wire{}).restJSON(http.MethodPost, "/bad", map[string]chan int{"bad": make(chan int)}, nil)
	require.ErrorContains(t, err, "encode Discord API request")

	errNetwork := errors.New("network down")
	w := &wire{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errNetwork
	})}}

	err = w.restJSON(http.MethodGet, "/bad", nil, nil)
	require.ErrorIs(t, err, errNetwork)
	require.ErrorContains(t, err, "call Discord API")
}

func TestWireChannelReportsRESTError(t *testing.T) {
	errREST := errors.New("discord unavailable")
	w := &wire{client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errREST
	})}}

	got, err := w.channel("voice-1")
	require.ErrorIs(t, err, errREST)
	assert.Nil(t, got)
}

func TestWireWaitVoiceJoinReturnsStoredSessionAndServer(t *testing.T) {
	endpoint := "voice.example"
	w := &wire{
		voiceSessionID:     map[string]string{"guild": "session"},
		voiceServerUpdates: map[string]*voiceServerUpdate{"guild": {GuildID: "guild", Token: "token", Endpoint: &endpoint}},
	}

	sessionID, update, err := w.waitVoiceJoin(t.Context(), "guild")
	require.NoError(t, err)
	assert.Equal(t, "session", sessionID)
	assert.Equal(t, "token", update.Token)
}

func TestWireWaitVoiceJoinReportsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	w := &wire{voiceSessionID: map[string]string{}, voiceServerUpdates: map[string]*voiceServerUpdate{}}

	sessionID, update, err := w.waitVoiceJoin(ctx, "guild")
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, sessionID)
	assert.Nil(t, update)
}

func TestWireJoinVoiceReportsPrerequisiteErrors(t *testing.T) {
	w := &wire{}
	vc, err := w.joinVoice(t.Context(), "guild", "channel", true, false)
	require.ErrorContains(t, err, "discord gateway is not connected")
	assert.Nil(t, vc)

	messages := make(chan json.RawMessage, 1)
	errs := make(chan error, 1)
	upgrader := websocket.Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errs <- err
			return
		}

		defer func() { _ = conn.Close() }()

		_, data, err := conn.ReadMessage()
		if err != nil {
			errs <- err
			return
		}

		messages <- slices.Clone(data)
	}))
	defer server.Close()

	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}

	require.NoError(t, err)

	defer func() { _ = conn.Close() }()

	endpoint := "voice.example"
	w = &wire{
		wsConn:             conn,
		voiceSessionID:     map[string]string{"guild": "session"},
		voiceServerUpdates: map[string]*voiceServerUpdate{"guild": {GuildID: "guild", Token: "token", Endpoint: &endpoint}},
	}

	vc, err = w.joinVoice(t.Context(), "guild", "channel", true, true)
	require.ErrorContains(t, err, "discord current user missing")
	assert.Nil(t, vc)

	select {
	case err := <-errs:
		t.Fatalf("gateway server error: %v", err)
	case message := <-messages:
		var payload struct {
			Op gatewayOpcode `json:"op"`
			D  struct {
				SelfMute bool `json:"self_mute"`
				SelfDeaf bool `json:"self_deaf"`
			} `json:"d"`
		}
		require.NoError(t, json.Unmarshal(message, &payload))
		assert.Equal(t, gatewayOpVoiceStateUpdate, payload.Op)
		assert.True(t, payload.D.SelfMute)
		assert.True(t, payload.D.SelfDeaf)
	case <-time.After(time.Second):
		t.Fatal("Voice State Update payload was not received")
	}
}

func TestWireHandleDispatchStoresVoiceServerUpdate(t *testing.T) {
	w := &wire{voiceServerUpdates: map[string]*voiceServerUpdate{}}
	w.handleDispatch("VOICE_SERVER_UPDATE", json.RawMessage(`{"guild_id":"guild","token":"token","endpoint":"voice.example"}`))

	update := w.voiceServerUpdates["guild"]
	require.NotNil(t, update)
	assert.Equal(t, "token", update.Token)
	require.NotNil(t, update.Endpoint)
	assert.Equal(t, "voice.example", *update.Endpoint)
}

func TestWireHandleDispatchIgnoresMalformedPayloads(t *testing.T) {
	w := &wire{
		ready:              make(chan struct{}),
		voiceStates:        map[string][]*voiceStateUpdate{},
		voiceSessionID:     map[string]string{},
		voiceServerUpdates: map[string]*voiceServerUpdate{},
	}

	w.handleDispatch("READY", json.RawMessage(`{"user":null}`))
	w.handleDispatch("READY", json.RawMessage(`{`))
	w.handleDispatch("VOICE_STATE_UPDATE", json.RawMessage(`{`))
	w.handleDispatch("VOICE_SERVER_UPDATE", json.RawMessage(`{`))

	select {
	case <-w.ready:
		t.Fatal("malformed READY closed ready channel")
	default:
	}

	assert.Empty(t, w.voiceStates)
	assert.Empty(t, w.voiceSessionID)
	assert.Empty(t, w.voiceServerUpdates)
}

func TestVoiceConnectionValidateReportsUnavailableStates(t *testing.T) {
	var missing *voiceConnection
	if err := missing.validate(); err == nil {
		t.Fatal("validate(nil) succeeded")
	}

	v := newVoiceConnection(nil, "guild", "channel", true, false)

	v.err = errors.New("dead")
	if err := v.validate(); err == nil {
		t.Fatal("validate(connection error) succeeded")
	}

	v = newVoiceConnection(nil, "guild", "channel", true, false)

	v.opusSend = nil
	if err := v.validate(); err == nil {
		t.Fatal("validate(nil opusSend) succeeded")
	}
}

func TestVoiceConnectionConnectReportsMissingEndpoint(t *testing.T) {
	v := newVoiceConnection(&wire{}, "guild", "channel", false, false)

	err := v.connect(t.Context(), "bot", "session", &voiceServerUpdate{GuildID: "guild", Token: "token"})
	require.ErrorContains(t, err, "discord voice server endpoint missing")
}

func TestVoiceConnectionHandlesDaveBinaryMessages(t *testing.T) {
	d := new(daveSession)
	v := newVoiceConnection(nil, "guild", "channel", true, false)
	v.dave = d

	v.handleVoiceBinary([]byte{0, 9, daveMlsExternalSenderOp, 0xaa, 0xbb})

	v.cond.L.Lock()
	seqAck := v.seqAck
	v.cond.L.Unlock()

	assert.Equal(t, uint16(9), seqAck)
	assert.Equal(t, []byte{0xaa, 0xbb}, d.externalSender)

	v.handleVoiceBinary([]byte{0, 10, daveMlsWelcomeOp, 0, 1})
}

func TestVoiceConnectionDaveHandlersIgnoreUnavailablePayloads(t *testing.T) {
	v := newVoiceConnection(nil, "guild", "channel", true, false)

	v.handleDaveProposals([]byte{0})
	v.handleDaveTransition(daveMlsWelcomeOp, []byte{0})
	v.handleDaveTransition(daveMlsAnnounceCommitOp, []byte{0, 1, 2})

	v.dave = new(daveSession)
	v.handleDaveProposals(nil)
	v.handleDaveTransition(daveMlsWelcomeOp, []byte{0, 1, 2})

	select {
	case <-v.dead:
		t.Fatal("invalid DAVE payload killed voice connection")
	default:
	}
}

func TestVoiceConnectionSelectUDPProtocolSendsDiscoveredAddress(t *testing.T) {
	udpServer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)

	defer func() { require.NoError(t, udpServer.Close()) }()

	type udpResult struct {
		ssrc uint32
		err  error
	}

	udpResults := make(chan udpResult, 1)

	go func() {
		packet := make([]byte, 74)

		_, addr, err := udpServer.ReadFromUDP(packet)
		if err != nil {
			udpResults <- udpResult{err: err}
			return
		}

		response := make([]byte, 74)
		binary.BigEndian.PutUint16(response[0:2], 2)
		copy(response[8:72], "203.0.113.9")
		binary.BigEndian.PutUint16(response[72:74], 50000)

		_, err = udpServer.WriteToUDP(response, addr)
		udpResults <- udpResult{ssrc: binary.BigEndian.Uint32(packet[4:8]), err: err}
	}()

	type selectProtocolPayload struct {
		Op voiceOpcode `json:"op"`
		D  struct {
			Protocol string `json:"protocol"`
			Data     struct {
				Address string `json:"address"`
				Port    uint16 `json:"port"`
				Mode    string `json:"mode"`
			} `json:"data"`
			Codecs []struct {
				Name        string `json:"name"`
				Type        string `json:"type"`
				PayloadType int    `json:"payload_type"`
			} `json:"codecs"`
			Experiments []string `json:"experiments"`
		} `json:"d"`
	}

	payloads := make(chan selectProtocolPayload, 1)
	upgrader := websocket.Upgrader{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}

		defer func() { _ = conn.Close() }()

		var payload selectProtocolPayload
		if err := conn.ReadJSON(&payload); err != nil {
			return
		}

		payloads <- payload
	}))
	defer server.Close()

	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if resp != nil && resp.Body != nil {
		defer func() { _ = resp.Body.Close() }()
	}

	require.NoError(t, err)

	defer func() { _ = conn.Close() }()

	v := newVoiceConnection(nil, "guild", "channel", true, false)
	v.wsConn = conn

	t.Cleanup(func() { v.die(nil) })

	addr := udpServer.LocalAddr().(*net.UDPAddr)
	err = v.selectUDPProtocol(conn, 0x01020304, addr.IP.String(), addr.Port, []string{string(rtpEncryptionModeAES256GCMRTPSize)}, []string{"exp"})
	require.NoError(t, err)

	select {
	case result := <-udpResults:
		require.NoError(t, result.err)
		assert.Equal(t, uint32(0x01020304), result.ssrc)
	case <-time.After(time.Second):
		t.Fatal("UDP discovery probe was not received")
	}

	select {
	case payload := <-payloads:
		assert.Equal(t, voiceOpSelectProtocol, payload.Op)
		assert.Equal(t, "udp", payload.D.Protocol)
		assert.Equal(t, "203.0.113.9", payload.D.Data.Address)
		assert.Equal(t, uint16(50000), payload.D.Data.Port)
		assert.Equal(t, string(rtpEncryptionModeAES256GCMRTPSize), payload.D.Data.Mode)
		require.Len(t, payload.D.Codecs, 1)
		assert.Equal(t, "opus", payload.D.Codecs[0].Name)
		assert.Equal(t, "audio", payload.D.Codecs[0].Type)
		assert.Equal(t, rtpPayloadTypeOpus, payload.D.Codecs[0].PayloadType)
		assert.Equal(t, []string{"exp"}, payload.D.Experiments)
	case <-time.After(time.Second):
		t.Fatal("Select Protocol payload was not received")
	}

	v.cond.L.Lock()
	udpConn := v.udpConn
	ssrc := v.ssrc
	timestamp := v.timestamp
	v.cond.L.Unlock()

	require.NotNil(t, udpConn)
	assert.Equal(t, uint32(0x01020304), ssrc)
	assert.NotZero(t, timestamp)
}

func TestVoiceConnectionUDPLoopsStopWithoutConnection(t *testing.T) {
	v := newVoiceConnection(nil, "guild", "channel", true, false)
	v.udpReceiveLoop()

	done := make(chan struct{})

	go func() {
		v.udpSendLoop()
		close(done)
	}()

	v.cond.L.Lock()
	v.mode = rtpEncryptionModeXChaCha20Poly1305RTPSize
	v.cond.L.Unlock()

	v.opusSend <- []byte{0x01}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("udpSendLoop() did not stop without a UDP connection")
	}

	select {
	case <-v.dead:
		t.Fatal("udpSendLoop() killed the voice connection without a UDP connection")
	default:
	}

	v.die(nil)
	v.udpKeepAliveLoop()
}

func TestVoiceConnectionSessionDescriptionMarksReady(t *testing.T) {
	v := newVoiceConnection(nil, "guild", "channel", true, false)

	t.Cleanup(func() { v.die(nil) })

	secret := bytes.Repeat([]byte{0x55}, len(v.secret))
	data, err := json.Marshal(map[string]any{
		"mode":       string(rtpEncryptionModeXChaCha20Poly1305RTPSize),
		"secret_key": secret,
	})
	require.NoError(t, err)

	v.handleVoiceEvent(nil, voiceOpSessionDescription, data)

	select {
	case <-v.ready:
	case <-time.After(time.Second):
		t.Fatal("Session Description did not mark voice connection ready")
	}

	v.cond.L.Lock()
	status := v.status
	mode := v.mode
	gotSecret := v.secret
	v.cond.L.Unlock()

	assert.Equal(t, voiceConnectionStatusReady, status)
	assert.Equal(t, rtpEncryptionModeXChaCha20Poly1305RTPSize, mode)
	assert.Equal(t, secret, gotSecret[:])
}

func TestWirePlaySendsFramesAndReportsIteratorErrors(t *testing.T) {
	v := newVoiceConnection(nil, "guild", "channel", true, false)
	v.opusSend = make(chan []byte, 3)
	w := &wire{voiceConn: v}

	frames := opusFrames(func(yield func([]byte) error) error {
		if err := yield([]byte{0x01}); err != nil {
			return err
		}

		return yield([]byte{0x02})
	})

	sent, err := w.play(t.Context(), frames)
	if err != nil {
		t.Fatalf("play() returned error: %v", err)
	}

	if sent != 2 {
		t.Fatalf("play() sent = %d; want 2", sent)
	}

	if got := <-v.opusSend; !bytes.Equal(got, []byte{0x01}) {
		t.Fatalf("play() first frame = %x; want 01", got)
	}

	if got := <-v.opusSend; !bytes.Equal(got, []byte{0x02}) {
		t.Fatalf("play() second frame = %x; want 02", got)
	}

	v.cond.L.Lock()
	speaking := v.speaking
	v.cond.L.Unlock()

	if speaking {
		t.Fatal("play() left connection speaking")
	}

	errFrames := errors.New("frames failed")

	sent, err = w.play(t.Context(), func(yield func([]byte) error) error {
		if err := yield([]byte{0x03}); err != nil {
			return err
		}

		return errFrames
	})
	if err == nil {
		t.Fatal("play() with iterator error succeeded")
	}

	if sent != 1 {
		t.Fatalf("play() sent after iterator error = %d; want 1", sent)
	}
}

func TestWirePlayReportsContextAndConnectionErrors(t *testing.T) {
	w := new(wire)
	sent, err := w.play(t.Context(), func(yield func([]byte) error) error {
		return yield([]byte{0x01})
	})
	require.ErrorContains(t, err, "discord voice connection unavailable")
	assert.Zero(t, sent)

	v := newVoiceConnection(nil, "guild", "channel", true, false)
	w = &wire{voiceConn: v}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	sent, err = w.play(ctx, func(yield func([]byte) error) error {
		return yield([]byte{0x01})
	})
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, err.Error(), "decode Ogg Opus stream")
	assert.Zero(t, sent)

	v = newVoiceConnection(nil, "guild", "channel", true, false)
	w.voiceConn = v
	errDead := errors.New("dead")
	sent, err = w.play(t.Context(), func(yield func([]byte) error) error {
		v.die(errDead)
		return yield([]byte{0x01})
	})
	require.ErrorContains(t, err, "discord voice connection unavailable")
	assert.Zero(t, sent)
}

func TestWireSendWakeupFrames(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newVoiceConnection(nil, "guild", "channel", true, false)
		v.opusSend = make(chan []byte, 5)
		w := new(wire)

		w.sendWakeupFrames(t.Context(), v)
		close(v.opusSend)

		count := 0
		for frame := range v.opusSend {
			count++

			if !bytes.Equal(frame, []byte{daveOpusSilence0, daveOpusSilence1, daveOpusSilence2}) {
				t.Fatalf("sendWakeupFrames() frame = %x; want Opus silence", frame)
			}
		}

		if count != 5 {
			t.Fatalf("sendWakeupFrames() sent %d frames; want 5", count)
		}
	})
}

func TestWireSendWakeupFramesStopsOnCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	v := newVoiceConnection(nil, "guild", "channel", true, false)
	w := new(wire)
	done := make(chan struct{})

	go func() {
		w.sendWakeupFrames(ctx, v)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sendWakeupFrames() did not stop after context cancellation")
	}
}

func TestWireTracksGuildUsers(t *testing.T) {
	wire := &wire{voiceStates: make(map[string][]*voiceStateUpdate), voiceSessionID: make(map[string]string), ready: make(chan struct{})}
	wire.handleDispatch("READY", json.RawMessage(`{"user":{"id":"u1"}}`))
	wire.handleDispatch("VOICE_STATE_UPDATE", json.RawMessage(`{"guild_id":"g1","user_id":"u1","channel_id":"c1"}`))
	wire.handleDispatch("VOICE_STATE_UPDATE", json.RawMessage(`{"guild_id":"g1","user_id":"u2","channel_id":"c2"}`))
	wire.handleDispatch("VOICE_STATE_UPDATE", json.RawMessage(`{"guild_id":"g1","user_id":"u1","channel_id":"c3"}`))

	wire.mu.Lock()
	user := wire.user
	states := slices.Clone(wire.voiceStates["g1"])
	wire.mu.Unlock()

	require.NotNil(t, user)
	assert.Equal(t, "u1", user.ID)
	require.Len(t, states, 2)
	assert.Equal(t, "c3", states[0].ChannelID)
	assert.Equal(t, "c2", states[1].ChannelID)
}

func receiveVoiceEvent(t *testing.T, events <-chan voiceEvent) voiceEvent {
	t.Helper()

	select {
	case event := <-events:
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for voice event")
	}

	return voiceEvent{}
}

func TestDaveProposalReferences(t *testing.T) {
	session, err := newDaveSession("111", "222")
	require.NoError(t, err)

	reference := mlsVector(bytes.Repeat([]byte{0xaa}, mlsHashSize))
	commitWelcome, err := session.processProposals(slices.Concat([]byte{1}, mlsVector(reference)))
	require.NoError(t, err)
	assert.Empty(t, commitWelcome)
}

func TestDaveProposalControlMessages(t *testing.T) {
	session := new(daveSession)
	if _, err := session.processProposals(nil); err == nil {
		t.Fatal("processProposals(empty payload) succeeded")
	}

	revoke := slices.Concat([]byte{byte(daveProposalsRevoke)}, mlsVector(mlsVector([]byte("proposal-ref"))))

	commitWelcome, err := session.processProposals(revoke)
	if err != nil {
		t.Fatalf("processProposals(revoke) returned error: %v", err)
	}

	if len(commitWelcome) != 0 {
		t.Fatalf("processProposals(revoke) returned %x; want empty", commitWelcome)
	}

	if _, err := session.processProposals(append(slices.Clone(revoke), 0)); err == nil {
		t.Fatal("processProposals(trailing bytes) succeeded")
	}

	unknown := slices.Concat([]byte{99}, mlsVector(nil))

	commitWelcome, err = session.processProposals(unknown)
	if err != nil {
		t.Fatalf("processProposals(unknown operation) returned error: %v", err)
	}

	if len(commitWelcome) != 0 {
		t.Fatalf("processProposals(unknown operation) returned %x; want empty", commitWelcome)
	}

	session.externalSender = []byte{1, 2, 3, 4}
	session.setExternalSender([]byte{9, 8})

	if !bytes.Equal(session.externalSender, []byte{9, 8}) {
		t.Fatalf("externalSender = %x; want 0908", session.externalSender)
	}
}

func TestReadWelcomeJoinerSecretBranches(t *testing.T) {
	joinerSecret := bytes.Repeat([]byte{0x33}, mlsHashSize)
	pathSecret := []byte{0xaa, 0xbb}

	got, err := readWelcomeJoinerSecret(slices.Concat(mlsVector(joinerSecret), []byte{1}, mlsVector(pathSecret), mlsVector(nil)))
	require.NoError(t, err)
	assert.Equal(t, joinerSecret, got)

	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "bad path secret presence", data: slices.Concat(mlsVector(joinerSecret), []byte{2}, mlsVector(nil)), want: "path secret presence"},
		{name: "psks unsupported", data: slices.Concat(mlsVector(joinerSecret), []byte{0}, mlsVector([]byte{1})), want: "PSKs are not supported"},
		{name: "trailing bytes", data: slices.Concat(mlsVector(joinerSecret), []byte{0}, mlsVector(nil), []byte{0xff}), want: "trailing bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := readWelcomeJoinerSecret(tt.data)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestDaveMLSKeyPackageProposalCommitAndWelcome(t *testing.T) {
	owner, err := newDaveSession("111", "222")
	require.NoError(t, err)
	_, err = owner.keyPackageMessage()
	require.NoError(t, err)

	joiner, err := newDaveSession("333", "222")
	require.NoError(t, err)
	joinerKeyPackage, err := joiner.keyPackageMessage()
	require.NoError(t, err)

	proposalMessage := slices.Concat(
		binary.BigEndian.AppendUint16(binary.BigEndian.AppendUint16(nil, mlsVersion10), mlsWireFormatPublic),
		mlsVector([]byte("group")),
		binary.BigEndian.AppendUint64(nil, 0),
		[]byte{mlsSenderExternal},
		binary.BigEndian.AppendUint32(nil, 0),
		mlsVector(nil),
		[]byte{mlsContentTypeProposal},
		binary.BigEndian.AppendUint16(nil, mlsProposalAdd),
		joinerKeyPackage,
		mlsVector(nil),
	)
	proposal, err := (&mlsReader{data: proposalMessage}).readProposalMessage()
	require.NoError(t, err)
	assert.Equal(t, joiner.keyPackage, proposal.keyPackage)

	payload := append([]byte{0}, mlsVector(proposalMessage)...)
	commitWelcome, err := owner.processProposals(payload)
	require.NoError(t, err)
	require.NotEmpty(t, commitWelcome)
	require.NotEmpty(t, owner.mediaSecret)
}

func TestDaveMLSWelcomeAppliesEpochSecrets(t *testing.T) {
	joiner, err := newDaveSession("333", "222")
	require.NoError(t, err)
	_, err = joiner.keyPackageMessage()
	require.NoError(t, err)

	joinerSecret := bytes.Repeat([]byte{0x33}, mlsHashSize)
	preEpochSecret, err := hkdf.Extract(sha256.New, make([]byte, mlsHashSize), joinerSecret)
	require.NoError(t, err)
	welcomeSecret, err := mlsDeriveSecret(preEpochSecret, "welcome")
	require.NoError(t, err)
	welcomeKey, err := mlsExpandWithLabel(welcomeSecret, "key", nil, 16)
	require.NoError(t, err)
	welcomeNonce, err := mlsExpandWithLabel(welcomeSecret, "nonce", nil, 12)
	require.NoError(t, err)

	groupContext := joiner.groupContext([]byte("group"), 7, bytes.Repeat([]byte{0x44}, mlsHashSize), bytes.Repeat([]byte{0x55}, mlsHashSize))
	groupInfo := slices.Concat(groupContext, mlsVector(nil), mlsVector(bytes.Repeat([]byte{0x66}, mlsHashSize)), binary.BigEndian.AppendUint32(nil, 0))
	block, err := aes.NewCipher(welcomeKey)
	require.NoError(t, err)
	aead, err := cipher.NewGCM(block)
	require.NoError(t, err)

	encryptedGroupInfo := aead.Seal(nil, welcomeNonce, groupInfo, nil)

	publicKey, err := hpke.NewDHKEMPublicKey(joiner.initKey.PublicKey())
	require.NoError(t, err)
	enc, sender, err := hpke.NewSender(publicKey, hpke.HKDFSHA256(), hpke.AES128GCM(), mlsEncryptContext("Welcome", encryptedGroupInfo))
	require.NoError(t, err)
	ciphertext, err := sender.Seal(nil, slices.Concat(mlsVector(joinerSecret), []byte{0, 0}))
	require.NoError(t, err)

	encryptedSecrets := slices.Concat(mlsVector(mlsHashWithLabel(mlsKeyPackageRefLabel, joiner.keyPackage)), mlsVector(enc), mlsVector(ciphertext))
	welcome := slices.Concat(binary.BigEndian.AppendUint16(nil, mlsCipherSuiteDAVEV1), mlsVector(encryptedSecrets), mlsVector(encryptedGroupInfo))

	require.NoError(t, joiner.processWelcome(welcome))
	assert.Len(t, joiner.mediaSecret, mlsHashSize)
	assert.Equal(t, joiner.mediaSecret, joiner.epochSecret)
	assert.Len(t, joiner.initSecret, mlsHashSize)
}

func TestDaveMLSWelcomeRejectsMalformedPayloads(t *testing.T) {
	for _, tt := range []struct {
		name string
		data []byte
		want string
	}{
		{name: "truncated cipher suite", data: []byte{0}, want: "MLS uint16 is truncated"},
		{name: "wrong cipher suite", data: binary.BigEndian.AppendUint16(nil, 99), want: "DAVE MLS welcome cipher suite 99"},
		{name: "truncated secrets", data: binary.BigEndian.AppendUint16(nil, mlsCipherSuiteDAVEV1), want: "MLS varint is truncated"},
		{name: "truncated group info", data: slices.Concat(binary.BigEndian.AppendUint16(nil, mlsCipherSuiteDAVEV1), mlsVector(nil)), want: "MLS varint is truncated"},
		{name: "trailing bytes", data: slices.Concat(binary.BigEndian.AppendUint16(nil, mlsCipherSuiteDAVEV1), mlsVector(nil), mlsVector(nil), []byte{0xff}), want: "DAVE MLS welcome has 1 trailing bytes"},
		{name: "missing key package", data: slices.Concat(binary.BigEndian.AppendUint16(nil, mlsCipherSuiteDAVEV1), mlsVector(nil), mlsVector(nil)), want: "DAVE MLS welcome did not include this key package"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := new(daveSession).processWelcome(tt.data)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestReadMLSGroupContext(t *testing.T) {
	contextBytes := slices.Concat(
		binary.BigEndian.AppendUint16(binary.BigEndian.AppendUint16(nil, mlsVersion10), mlsCipherSuiteDAVEV1),
		mlsVector([]byte("group")),
		binary.BigEndian.AppendUint64(nil, 7),
		mlsVector([]byte("tree")),
		mlsVector([]byte("confirmed")),
		mlsVector(nil),
	)

	got, err := readMLSGroupContext(append(slices.Clone(contextBytes), 0xff))
	require.NoError(t, err)
	assert.Equal(t, contextBytes, got)

	groupIDEnd := 4 + len(mlsVector([]byte("group")))
	epochEnd := groupIDEnd + 8
	treeEnd := epochEnd + len(mlsVector([]byte("tree")))
	confirmedEnd := treeEnd + len(mlsVector([]byte("confirmed")))

	for _, tt := range []struct {
		name string
		data []byte
	}{
		{name: "version", data: contextBytes[:1]},
		{name: "cipher suite", data: contextBytes[:3]},
		{name: "group id", data: contextBytes[:4]},
		{name: "epoch", data: contextBytes[:groupIDEnd+7]},
		{name: "tree hash", data: contextBytes[:epochEnd]},
		{name: "confirmed hash", data: contextBytes[:treeEnd]},
		{name: "extensions", data: contextBytes[:confirmedEnd]},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := readMLSGroupContext(tt.data)
			require.Error(t, err)
		})
	}
}

func TestDaveMediaFrameParsing(t *testing.T) {
	actual := []byte{0x10, 0x11, 0x20, 0x21, 0x22, 0x30}
	rangeBytes := appendDaveLEB128(nil, 2)
	rangeBytes = appendDaveLEB128(rangeBytes, 2)
	rangeBytes = appendDaveLEB128(rangeBytes, 5)
	rangeBytes = appendDaveLEB128(rangeBytes, 1)
	supplemental := bytes.Repeat([]byte{0xaa}, daveMediaTagSize)
	supplemental = appendDaveLEB128(supplemental, 7)
	supplemental = append(supplemental, rangeBytes...)
	supplemental = append(supplemental, byte(len(supplemental)+3), 0xfa, 0xfa)
	frameBytes := append(slices.Clone(actual), supplemental...)

	frame, err := parseDaveMediaFrame(frameBytes)
	if err != nil {
		t.Fatalf("parseDaveMediaFrame() returned error: %v", err)
	}

	if frame.nonce != 7 {
		t.Fatalf("parseDaveMediaFrame() nonce = %d; want 7", frame.nonce)
	}

	if !bytes.Equal(frame.authenticated, []byte{0x20, 0x21, 0x30}) {
		t.Fatalf("parseDaveMediaFrame() authenticated = %x; want 202130", frame.authenticated)
	}

	if !bytes.Equal(frame.ciphertext, []byte{0x10, 0x11, 0x22}) {
		t.Fatalf("parseDaveMediaFrame() ciphertext = %x; want 101122", frame.ciphertext)
	}

	if got := reconstructDaveMediaFrame(frame.ranges, frame.authenticated, frame.ciphertext, len(actual)); !bytes.Equal(got, actual) {
		t.Fatalf("reconstructDaveMediaFrame() = %x; want %x", got, actual)
	}

	if _, err := parseDaveMediaFrame([]byte{1, 2}); err == nil {
		t.Fatal("parseDaveMediaFrame(short frame) succeeded")
	}

	badMarker := slices.Clone(frameBytes)

	badMarker[len(badMarker)-1] = 0
	if _, err := parseDaveMediaFrame(badMarker); err == nil {
		t.Fatal("parseDaveMediaFrame(bad marker) succeeded")
	}

	badSize := slices.Clone(frameBytes)

	badSize[len(badSize)-3] = daveMediaTagSize + 2
	if _, err := parseDaveMediaFrame(badSize); err == nil {
		t.Fatal("parseDaveMediaFrame(bad supplemental size) succeeded")
	}

	truncatedLEB := append(bytes.Repeat([]byte{0xbb}, daveMediaTagSize), 0x80)

	truncatedLEB = append(truncatedLEB, byte(len(truncatedLEB)+3), 0xfa, 0xfa)
	if _, err := parseDaveMediaFrame(append(slices.Clone(actual), truncatedLEB...)); err == nil {
		t.Fatal("parseDaveMediaFrame(truncated LEB128) succeeded")
	}

	badRangeEncoding := bytes.Repeat([]byte{0xbb}, daveMediaTagSize)
	badRangeEncoding = appendDaveLEB128(badRangeEncoding, 1)
	badRangeEncoding = appendDaveLEB128(badRangeEncoding, 1)

	badRangeEncoding = append(badRangeEncoding, 0x80, byte(len(badRangeEncoding)+4), 0xfa, 0xfa)
	if _, err := parseDaveMediaFrame(append(slices.Clone(actual), badRangeEncoding...)); err == nil {
		t.Fatal("parseDaveMediaFrame(bad range encoding) succeeded")
	}

	badRangeBytes := appendDaveLEB128(nil, uint32(len(actual)+1))
	badRangeBytes = appendDaveLEB128(badRangeBytes, 1)
	badRangeSupplemental := bytes.Repeat([]byte{0xcc}, daveMediaTagSize)
	badRangeSupplemental = appendDaveLEB128(badRangeSupplemental, 1)
	badRangeSupplemental = append(badRangeSupplemental, badRangeBytes...)

	badRangeSupplemental = append(badRangeSupplemental, byte(len(badRangeSupplemental)+3), 0xfa, 0xfa)
	if _, err := parseDaveMediaFrame(append(slices.Clone(actual), badRangeSupplemental...)); err == nil {
		t.Fatal("parseDaveMediaFrame(range outside frame) succeeded")
	}

	if _, err := parseDaveMediaRanges([]byte{0x80}); err == nil {
		t.Fatal("parseDaveMediaRanges(truncated offset) succeeded")
	}

	if _, err := parseDaveMediaRanges([]byte{0x01, 0x80}); err == nil {
		t.Fatal("parseDaveMediaRanges(truncated size) succeeded")
	}
}

func TestMLSReaderPrimitivesAndVarints(t *testing.T) {
	r := &mlsReader{data: []byte{0x12, 0x34, 0x56, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 2}}

	value16, err := r.readUint16()
	if err != nil {
		t.Fatalf("readUint16() returned error: %v", err)
	}

	if value16 != 0x1234 {
		t.Fatalf("readUint16() = %#x; want 0x1234", value16)
	}

	value8, err := r.readUint8()
	if err != nil {
		t.Fatalf("readUint8() returned error: %v", err)
	}

	if value8 != 0x56 {
		t.Fatalf("readUint8() = %#x; want 0x56", value8)
	}

	if err := r.skipUint32(); err != nil {
		t.Fatalf("skipUint32() returned error: %v", err)
	}

	if err := r.skipUint64(); err != nil {
		t.Fatalf("skipUint64() returned error: %v", err)
	}

	if got := r.remaining(); got != 0 {
		t.Fatalf("remaining() = %d; want 0", got)
	}

	if _, err := new(mlsReader).readUint16(); err == nil {
		t.Fatal("readUint16(empty) succeeded")
	}

	if _, err := new(mlsReader).readUint8(); err == nil {
		t.Fatal("readUint8(empty) succeeded")
	}

	if err := (&mlsReader{data: []byte{1, 2, 3}}).skipUint32(); err == nil {
		t.Fatal("skipUint32(short) succeeded")
	}

	if err := (&mlsReader{data: []byte{1, 2, 3, 4, 5, 6, 7}}).skipUint64(); err == nil {
		t.Fatal("skipUint64(short) succeeded")
	}

	for _, tt := range []struct {
		name string
		data []byte
		want int
	}{
		{name: "one byte", data: []byte{0x3f}, want: 63},
		{name: "two bytes", data: []byte{0x40, 0x40}, want: 64},
		{name: "four bytes", data: []byte{0x80, 0, 0x40, 0}, want: 16384},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &mlsReader{data: tt.data}

			got, err := r.readVarint()
			if err != nil {
				t.Fatalf("readVarint(%x) returned error: %v", tt.data, err)
			}

			if got != tt.want {
				t.Fatalf("readVarint(%x) = %d; want %d", tt.data, got, tt.want)
			}

			if encoded := mlsVarint(tt.want); !bytes.Equal(encoded, tt.data) {
				t.Fatalf("mlsVarint(%d) = %x; want %x", tt.want, encoded, tt.data)
			}
		})
	}

	for _, data := range [][]byte{nil, []byte{0x40}, []byte{0x80, 0, 0}, []byte{0xc0}} {
		r := &mlsReader{data: data}
		if _, err := r.readVarint(); err == nil {
			t.Fatalf("readVarint(%x) succeeded", data)
		}
	}
}

func TestMLSReaderProposalMessageRemoveAndErrors(t *testing.T) {
	message := slices.Concat(
		binary.BigEndian.AppendUint16(binary.BigEndian.AppendUint16(nil, mlsVersion10), mlsWireFormatPublic),
		mlsVector([]byte("group")),
		binary.BigEndian.AppendUint64(nil, 0),
		[]byte{mlsSenderExternal},
		binary.BigEndian.AppendUint32(nil, 0),
		mlsVector(nil),
		[]byte{mlsContentTypeProposal},
		binary.BigEndian.AppendUint16(nil, mlsProposalRemove),
		binary.BigEndian.AppendUint32(nil, 7),
		mlsVector(nil),
	)

	proposal, err := (&mlsReader{data: message}).readProposalMessage()
	require.NoError(t, err)
	assert.Equal(t, mlsProposalRemove, proposal.proposalType)
	assert.Equal(t, []byte("group"), proposal.groupID)
	assert.Empty(t, proposal.keyPackage)
	assert.Equal(t, message[2:], proposal.contentAuth)

	badVersion := slices.Clone(message)
	badVersion[1] = 2
	badWireFormat := slices.Clone(message)
	badWireFormat[3] = 2
	badSender := slices.Clone(message)
	badSender[18] = mlsSenderMember
	badContentType := slices.Clone(message)
	badContentType[24] = mlsContentTypeCommit
	badProposalType := slices.Clone(message)
	binary.BigEndian.PutUint16(badProposalType[25:27], 0xffff)

	badAddProposal := slices.Clone(message)
	binary.BigEndian.PutUint16(badAddProposal[25:27], mlsProposalAdd)

	badSignature := slices.Clone(message)
	badSignature[31] = 1

	for _, tt := range []struct {
		name string
		data []byte
		want string
	}{
		{name: "truncated version", data: message[:1], want: "MLS uint16 is truncated"},
		{name: "truncated wire format", data: message[:3], want: "MLS uint16 is truncated"},
		{name: "bad version", data: badVersion, want: "MLS proposal message version 2"},
		{name: "bad wire format", data: badWireFormat, want: "MLS proposal message wire format 2"},
		{name: "truncated group id", data: message[:4], want: "MLS varint is truncated"},
		{name: "truncated epoch", data: message[:17], want: "MLS uint64 is truncated"},
		{name: "truncated sender", data: message[:18], want: "MLS uint8 is truncated"},
		{name: "bad sender", data: badSender, want: "MLS proposal sender type 1"},
		{name: "truncated sender index", data: message[:22], want: "MLS uint32 is truncated"},
		{name: "truncated authenticated data", data: message[:23], want: "MLS varint is truncated"},
		{name: "truncated content type", data: message[:24], want: "MLS uint8 is truncated"},
		{name: "bad content type", data: badContentType, want: "MLS proposal content type 3"},
		{name: "truncated proposal type", data: message[:26], want: "MLS uint16 is truncated"},
		{name: "bad proposal type", data: badProposalType, want: "MLS proposal type 65535 is not supported"},
		{name: "truncated add proposal", data: badAddProposal[:27], want: "MLS uint16 is truncated"},
		{name: "truncated remove proposal", data: message[:29], want: "MLS uint32 is truncated"},
		{name: "truncated signature", data: badSignature, want: "MLS vector length 1 exceeds remaining 0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (&mlsReader{data: tt.data}).readProposalMessage()
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestMLSReaderSkipLeafNodeSources(t *testing.T) {
	leafNode := func(source mlsLeafNodeSource, credentialType int) []byte {
		data := slices.Concat(
			mlsVector([]byte{0x01}),
			mlsVector([]byte{0x02}),
			binary.BigEndian.AppendUint16(nil, uint16(credentialType)),
			mlsVector([]byte("credential")),
			mlsVector(nil),
			mlsVector(nil),
			mlsVector(nil),
			mlsVector(nil),
			mlsVector(nil),
			[]byte{byte(source)},
		)

		switch source {
		case mlsLeafNodeSourceKeyPackage:
			data = binary.BigEndian.AppendUint64(data, 1)
			data = binary.BigEndian.AppendUint64(data, 2)
		case mlsLeafNodeSourceUpdate:
		case mlsLeafNodeSourceCommit:
			data = append(data, mlsVector([]byte("parent"))...)
		}

		return slices.Concat(data, mlsVector(nil), mlsVector(nil))
	}

	keyPackageLeaf := leafNode(mlsLeafNodeSourceKeyPackage, mlsCredentialTypeBasic)
	updateLeaf := leafNode(mlsLeafNodeSourceUpdate, mlsCredentialTypeBasic)
	commitLeaf := leafNode(mlsLeafNodeSourceCommit, mlsCredentialTypeBasic)

	for _, tt := range []struct {
		name string
		data []byte
		want string
	}{
		{name: "key package", data: keyPackageLeaf},
		{name: "update", data: updateLeaf},
		{name: "commit", data: commitLeaf},
		{name: "truncated encryption key", data: nil, want: "MLS varint is truncated"},
		{name: "truncated signature key", data: updateLeaf[:2], want: "MLS varint is truncated"},
		{name: "truncated credential type", data: updateLeaf[:5], want: "MLS uint16 is truncated"},
		{name: "truncated credential", data: updateLeaf[:6], want: "MLS varint is truncated"},
		{name: "bad credential", data: leafNode(mlsLeafNodeSourceUpdate, 2), want: "MLS leaf credential type 2"},
		{name: "truncated capabilities", data: updateLeaf[:17], want: "MLS varint is truncated"},
		{name: "truncated source", data: updateLeaf[:22], want: "MLS uint8 is truncated"},
		{name: "truncated key package lifetime start", data: keyPackageLeaf[:30], want: "MLS uint64 is truncated"},
		{name: "truncated key package lifetime end", data: keyPackageLeaf[:38], want: "MLS uint64 is truncated"},
		{name: "truncated commit parent hash", data: commitLeaf[:23], want: "MLS varint is truncated"},
		{name: "bad source", data: leafNode(mlsLeafNodeSource(9), mlsCredentialTypeBasic), want: "MLS leaf node source 9"},
		{name: "truncated leaf extensions", data: updateLeaf[:23], want: "MLS varint is truncated"},
		{name: "truncated signature", data: updateLeaf[:24], want: "MLS varint is truncated"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := &mlsReader{data: tt.data}

			err := r.skipLeafNode()
			if tt.want != "" {
				require.ErrorContains(t, err, tt.want)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, 0, r.remaining())
		})
	}
}

func TestMLSReaderReadKeyPackageRejectsMalformedInput(t *testing.T) {
	session, err := newDaveSession("111", "222")
	require.NoError(t, err)
	keyPackage, err := session.keyPackageMessage()
	require.NoError(t, err)

	reader := &mlsReader{data: keyPackage}
	_, err = reader.readUint16()
	require.NoError(t, err)
	_, err = reader.readUint16()
	require.NoError(t, err)
	_, err = reader.readVector()
	require.NoError(t, err)

	leafStart := reader.off
	require.NoError(t, reader.skipLeafNode())
	leafEnd := reader.off
	_, err = reader.readVector()
	require.NoError(t, err)

	extensionsEnd := reader.off

	for _, tt := range []struct {
		name string
		data []byte
		want string
	}{
		{name: "truncated version", data: keyPackage[:1], want: "MLS uint16 is truncated"},
		{name: "truncated cipher suite", data: keyPackage[:3], want: "MLS uint16 is truncated"},
		{name: "truncated init key", data: keyPackage[:4], want: "MLS varint is truncated"},
		{name: "truncated leaf node", data: keyPackage[:leafStart], want: "MLS varint is truncated"},
		{name: "truncated extensions", data: keyPackage[:leafEnd], want: "MLS varint is truncated"},
		{name: "truncated signature", data: keyPackage[:extensionsEnd], want: "MLS varint is truncated"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, _, err := (&mlsReader{data: tt.data}).readKeyPackage()
			require.ErrorContains(t, err, tt.want)
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

package csp

import (
	"errors"
	"io"
	"time"
)

var ErrNoData = errors.New("no data available")
var ErrWrongChecksum = errors.New("wrong checksum")
var ErrWrite = errors.New("write failed")
var ErrWriteLength = errors.New("write failed to send all bytes")
var ErrTimeout = errors.New("timeout")

const BeaconInterval = 6 * time.Second

const maxPayload = 1 + 1 + 110 // CONFIG SET requests: ID[1], Offset[1], Data[up to 110 bytes]

const (
	stateIdle byte = iota
	stateHeader
	stateDirection
	stateLength
	stateCommand
	statePayload
	stateChecksum
)

type Adapter struct {
	wire io.ReadWriter

	lowestID            byte
	beaconReferenceTime int64

	state   byte
	message Message
}

func NewAdapter(wire io.ReadWriter) *Adapter {
	return &Adapter{
		wire: wire,
	}
}

// Send a message.
func (a *Adapter) Send(message *Message) error {
	bytes := message.Bytes()
	logTs("SEND ")
	for _, b := range bytes {
		log(" %02X", b)
	}
	log("\n")
	n, err := a.wire.Write(bytes)
	if err != nil {
		return ErrWrite
	}
	if n != len(bytes) {
		return ErrWriteLength
	}
	return nil
}

// Receive a message; returns nil if no message is available (yet).
func (a *Adapter) Receive() (*Message, error) {
	buf := make([]byte, 16)
	for {
		n, err := a.wire.Read(buf)
		if err != nil || n == 0 {
			return nil, ErrNoData
		}
		for i := 0; i < n; i++ {
			b := buf[i]
			switch a.state {
			case stateIdle:
				if b == '$' {
					logTs("IDLE %02X\n", b)
					a.message.Header[0] = b
					a.state = stateHeader
				}
			case stateHeader:
				if b == 'C' {
					logTs("HEADER %02X\n", b)
					a.message.Header[1] = b
					a.state = stateDirection
				} else {
					a.state = stateIdle
				}
			case stateDirection:
				logTs("DIRECTION %02X\n", b)
				if b != byte(DirRequest) && b != byte(DirResponse) {
					a.state = stateIdle
					continue
				}
				a.message.Direction = Direction(b)
				a.state = stateLength
			case stateLength:
				logTs("LENGTH %02X\n", b)
				if b > maxPayload {
					a.state = stateIdle
					continue
				}
				a.message.Length = b
				a.message.Payload = []byte{}
				a.message.Checksum = b
				a.state = stateCommand
			case stateCommand:
				logTs("COMMAND %02X\n", b)
				a.message.Command = Command(b)
				a.message.Checksum ^= b
				a.state = statePayload
			case statePayload:
				a.message.Payload = append(a.message.Payload, b)
				a.message.Checksum ^= b
				if len(a.message.Payload) == int(a.message.Length) {
					a.state = stateChecksum
				}
			case stateChecksum:
				logTs("PAYLOAD ")
				for _, bb := range a.message.Bytes() {
					log(" %02X", bb)
				}
				log("\n")
				logTs("CHECKSUM expected %02X ?= %02X actual\n", a.message.Checksum, b)
				result := a.message
				a.message = Message{}
				a.state = stateIdle
				if result.Checksum == b {
					a.handleBeaconMaybe(&result)
					return &result, nil
				} else {
					return nil, ErrWrongChecksum
				}
			}
		}
	}
}

// Wait for a message with the given command and direction.
func (a *Adapter) Wait(command Command, direction Direction, timeout time.Duration) (*Message, error) {
	start := time.Now()
	for time.Since(start) < timeout {
		message, _ := a.Receive()
		// wait for correct message
		if message != nil && message.Command == command && message.Direction == direction {
			return message, nil
		}
	}
	return nil, ErrTimeout
}

// Reset the state machine and clear the message buffer.
func (a *Adapter) Reset() {
	a.state = stateIdle
	a.message = Message{}
	buf := make([]byte, 16)
	for {
		n, err := a.wire.Read(buf)
		if err != nil || n == 0 {
			return
		}
	}
}

// BeaconTime returns the next time when a beacon with the given ID should be broadcasted.
func (a *Adapter) BeaconTime(id byte) time.Time {
	if a.beaconReferenceTime == 0 {
		return time.Time{}
	}
	offset := beaconOffset(id)
	t := a.beaconReferenceTime + offset.Milliseconds()
	now := time.Now().UnixMilli()
	for t < now {
		t += BeaconInterval.Milliseconds()
	}
	return time.UnixMilli(t)
}

func (a *Adapter) handleBeaconMaybe(message *Message) {
	if message.Command != CmdBeacon {
		return
	}
	id := message.Payload[0]
	if a.lowestID == 0 || a.lowestID > id {
		a.lowestID = id
	}
	if id != a.lowestID {
		return
	}
	// The beacon with the lowest ID is the reference beacon.
	offset := beaconOffset(a.lowestID)
	a.beaconReferenceTime = time.Now().UnixMilli() - offset.Milliseconds()
}

func beaconOffset(id byte) time.Duration {
	team := (id << 4) - 0x0A
	player := (id & 0x0F) - 1
	return time.Duration(team)*time.Second + time.Duration(player)*100*time.Millisecond
}

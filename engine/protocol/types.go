package protocol

import "fmt"

type MessageType uint8

const (
	MessageTypeOpen  MessageType = 1
	MessageTypeData  MessageType = 2
	MessageTypeClose MessageType = 3
	MessageTypeReset MessageType = 4
	MessageTypePing  MessageType = 5
	MessageTypeProbe MessageType = 6
)

func IsValidMessageType(messageType MessageType) bool {
	switch messageType {
	case MessageTypeOpen,
		MessageTypeData,
		MessageTypeClose,
		MessageTypeReset,
		MessageTypePing,
		MessageTypeProbe:
		return true
	default:
		return false
	}
}

func (m MessageType) String() string {
	switch m {
	case MessageTypeOpen:
		return "OPEN"
	case MessageTypeData:
		return "DATA"
	case MessageTypeClose:
		return "CLOSE"
	case MessageTypeReset:
		return "RESET"
	case MessageTypePing:
		return "PING"
	case MessageTypeProbe:
		return "PROBE"
	default:
		return ""
	}
}

func parseMessageType(s string) (MessageType, error) {
	switch s {
	case "OPEN":
		return MessageTypeOpen, nil
	case "DATA":
		return MessageTypeData, nil
	case "CLOSE":
		return MessageTypeClose, nil
	case "RESET":
		return MessageTypeReset, nil
	case "PING":
		return MessageTypePing, nil
	case "PROBE":
		return MessageTypeProbe, nil
	default:
		return 0, fmt.Errorf("unknown message type %q", s)
	}
}

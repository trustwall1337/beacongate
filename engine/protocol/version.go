package protocol

const (
	ProtocolVersionMajor uint16 = 1
	ProtocolVersionMinor uint16 = 0
)

func IsSupportedVersion(major, minor uint16) bool {
	return major == ProtocolVersionMajor && minor == ProtocolVersionMinor
}

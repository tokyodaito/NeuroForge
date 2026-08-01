package proctree

// Signal is a platform-independent process signal for [KillGroup]. It avoids a
// direct syscall.Signal dependency in the public API.
type Signal int

const (
	SigTerm Signal = 15
	SigKill Signal = 9
)

// String renders the signal name.
func (s Signal) String() string {
	switch s {
	case SigTerm:
		return "SIGTERM"
	case SigKill:
		return "SIGKILL"
	default:
		return "signal"
	}
}

package protocol

// Message types: Client → Server (requests)
const (
	MsgConnect         uint16 = 1  // Connect to SSH host
	MsgLocal           uint16 = 2  // Create local session
	MsgDetach          uint16 = 3  // Detach from session
	MsgKill            uint16 = 4  // Kill session
	MsgAttach          uint16 = 5  // Attach to existing session
	MsgExec            uint16 = 6  // Execute command in session
	MsgLocalExec       uint16 = 7  // One-off local exec (no session)
	MsgList            uint16 = 8  // List sessions
	MsgUse             uint16 = 9  // Set default session
	MsgForward         uint16 = 10 // Start port forward
	MsgForwards        uint16 = 11 // List forwards
	MsgForwardClose    uint16 = 12 // Close forward
	MsgSCP             uint16 = 13 // File transfer
	MsgSFTPLs          uint16 = 14 // SFTP list directory
	MsgSFTPMkdir       uint16 = 15 // SFTP mkdir
	MsgSFTPRm          uint16 = 16 // SFTP remove
	MsgReconnect       uint16 = 17 // Force reconnect
	MsgPing            uint16 = 18 // Health check
	MsgStop            uint16 = 19 // Stop daemon
	MsgExecStream      uint16 = 20 // Execute command with streaming output
	MsgLocalExecStream uint16 = 21 // One-off local exec with streaming output
)

// Message types: Server → Client (responses)
const (
	MsgResult      uint16 = 100 // Success result
	MsgError       uint16 = 101 // Error response
	MsgPong        uint16 = 102 // Ping response
	MsgStreamChunk uint16 = 103 // Streaming output chunk
	MsgStreamEnd   uint16 = 104 // End of streaming output (includes exit code)
)

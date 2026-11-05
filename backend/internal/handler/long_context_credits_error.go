package handler

import (
	"encoding/json"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type clientProtocol int

const (
	clientProtocolAnthropic clientProtocol = iota
	clientProtocolChatCompletions
	clientProtocolResponses
)

// writeLongContextCreditsError answers a long context stop (upstream credits error or
// local admission, #132/#139) in the client's protocol and reports whether it did. Before
// headers it is a JSON error with the stop's status; once a keepalive or output committed
// HTTP 200, it is a terminal SSE error with the same code.
func writeLongContextCreditsError(c *gin.Context, e *service.UpstreamFailoverError, streamStarted bool, protocol clientProtocol) bool {
	if !e.IsLongContextStop() || c == nil || c.Writer == nil {
		return false
	}
	errType := e.ClientErrorType()
	code, message, status := string(e.Reason), e.ClientMessage, e.ClientStatusCode
	errObj := gin.H{"type": errType, "code": code, "message": message}

	if !streamStarted && !c.Writer.Written() {
		switch protocol {
		case clientProtocolAnthropic:
			c.JSON(status, gin.H{"type": "error", "error": errObj})
		case clientProtocolResponses:
			c.JSON(status, gin.H{"error": gin.H{"code": code, "message": message}})
		default:
			c.JSON(status, gin.H{"error": errObj})
		}
		return true
	}

	if service.IsResponseCommitted(c) {
		// The compat stream writer already sent the terminal frame with this code; never send a second one.
		return true
	}
	service.MarkOpsStreamError(c, errType, message, status)
	var frame []byte
	switch protocol {
	case clientProtocolResponses:
		// Same rule as handleResponsesFailoverExhausted: never append a second terminal event.
		if c.Writer.Size() <= 0 || gatewayStreamHasOnlyHeartbeats(c) {
			writeResponsesFailedSSE(c, errType, code, message)
		}
		return true
	case clientProtocolAnthropic:
		payload, _ := json.Marshal(gin.H{"type": "error", "error": errObj})
		frame = append(append([]byte("event: error\ndata: "), payload...), "\n\n"...)
	default:
		payload, _ := json.Marshal(gin.H{"error": errObj})
		frame = append(append([]byte("data: "), payload...), "\n\n"...)
	}
	if _, err := c.Writer.Write(frame); err != nil {
		_ = c.Error(err)
		return true
	}
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return true
}

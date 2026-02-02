package websocket

import (
	"encoding/json"
	"regexp"
	"strings"
)

// SessionControlType represents the type of session control message
type SessionControlType int

const (
	// ControlNone - Not a session control message
	ControlNone SessionControlType = iota
	// ControlInvite - Start a new voice session (like SIP INVITE)
	ControlInvite
	// ControlOK - Session accepted/confirmed (like SIP 200 OK)
	ControlOK
	// ControlBye - End the voice session (like SIP BYE)
	ControlBye
	// ControlHold - Put session on hold
	ControlHold
	// ControlResume - Resume from hold
	ControlResume
	// ControlCancel - Cancel pending session
	ControlCancel
	// ControlTurnStart - Conversation turn starting
	ControlTurnStart
	// ControlTurnEnd - Conversation turn ended
	ControlTurnEnd
)

func (t SessionControlType) String() string {
	switch t {
	case ControlNone:
		return "none"
	case ControlInvite:
		return "invite"
	case ControlOK:
		return "ok"
	case ControlBye:
		return "bye"
	case ControlHold:
		return "hold"
	case ControlResume:
		return "resume"
	case ControlCancel:
		return "cancel"
	case ControlTurnStart:
		return "turn_start"
	case ControlTurnEnd:
		return "turn_end"
	default:
		return "unknown"
	}
}

// SessionControlMessage represents a parsed session control message
type SessionControlMessage struct {
	Type     SessionControlType
	Protocol string            // Which protocol detected it
	Metadata map[string]string // Extracted metadata
	RawEvent string            // Original event type string

	// Transcript content (if this message contains speech/text)
	Transcript        string // The transcript text
	TranscriptSpeaker string // "user" or "assistant"
	TranscriptFinal   bool   // true if this is a final transcript
	TranscriptSource  string // "stt", "tts", "text"
}

// SessionControlParser detects session control messages in WebSocket frames
type SessionControlParser struct {
	protocols []ProtocolParser
}

// ProtocolParser defines how to parse a specific protocol's control messages
type ProtocolParser interface {
	Name() string
	Parse(data []byte) *SessionControlMessage
}

// NewSessionControlParser creates a parser with default protocol support
func NewSessionControlParser(customPatterns *CustomPatternConfig) *SessionControlParser {
	parser := &SessionControlParser{
		protocols: []ProtocolParser{
			&OpenAIRealtimeParser{},
			&DeepgramParser{},
			&ElevenLabsParser{},
			&LiveKitParser{},
		},
	}

	// Add custom pattern parser if configured
	if customPatterns != nil && len(customPatterns.Patterns) > 0 {
		parser.protocols = append(parser.protocols, NewCustomPatternParser(customPatterns))
	}

	return parser
}

// Parse attempts to parse a session control message from frame data
func (p *SessionControlParser) Parse(data []byte) *SessionControlMessage {
	for _, proto := range p.protocols {
		if msg := proto.Parse(data); msg != nil {
			return msg
		}
	}
	return nil
}

// OpenAI Realtime API Protocol Parser
// https://platform.openai.com/docs/guides/realtime

type OpenAIRealtimeParser struct{}

func (p *OpenAIRealtimeParser) Name() string { return "openai_realtime" }

func (p *OpenAIRealtimeParser) Parse(data []byte) *SessionControlMessage {
	var msg struct {
		Type    string `json:"type"`
		Session struct {
			ID           string   `json:"id"`
			Model        string   `json:"model"`
			Voice        string   `json:"voice"`
			Instructions string   `json:"instructions"`
			Modalities   []string `json:"modalities"`
		} `json:"session"`
		Response struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"response"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
		// Transcript fields
		Transcript string `json:"transcript"` // For input_audio_transcription
		Delta      string `json:"delta"`      // For response.audio_transcript.delta, response.text.delta
		Text       string `json:"text"`       // For response.text.done
	}

	if err := json.Unmarshal(data, &msg); err != nil {
		return nil
	}

	metadata := make(map[string]string)

	switch msg.Type {
	case "session.create", "session.update":
		// INVITE - Client requesting to start/update session
		if msg.Session.Model != "" {
			metadata["model"] = msg.Session.Model
		}
		if msg.Session.Voice != "" {
			metadata["voice"] = msg.Session.Voice
		}
		if msg.Session.ID != "" {
			metadata["session_id"] = msg.Session.ID
		}
		if len(msg.Session.Modalities) > 0 {
			metadata["modalities"] = strings.Join(msg.Session.Modalities, ",")
		}
		return &SessionControlMessage{
			Type:     ControlInvite,
			Protocol: p.Name(),
			Metadata: metadata,
			RawEvent: msg.Type,
		}

	case "session.created", "session.updated":
		// OK - Server confirmed session
		if msg.Session.ID != "" {
			metadata["session_id"] = msg.Session.ID
		}
		return &SessionControlMessage{
			Type:     ControlOK,
			Protocol: p.Name(),
			Metadata: metadata,
			RawEvent: msg.Type,
		}

	case "response.create":
		// Turn start - Client requesting a response
		return &SessionControlMessage{
			Type:     ControlTurnStart,
			Protocol: p.Name(),
			Metadata: metadata,
			RawEvent: msg.Type,
		}

	case "response.done":
		// Turn end - Response completed
		if msg.Response.ID != "" {
			metadata["response_id"] = msg.Response.ID
		}
		if msg.Response.Status != "" {
			metadata["status"] = msg.Response.Status
		}
		return &SessionControlMessage{
			Type:     ControlTurnEnd,
			Protocol: p.Name(),
			Metadata: metadata,
			RawEvent: msg.Type,
		}

	case "input_audio_buffer.clear", "input_audio_buffer.commit":
		// Could be turn boundary
		return &SessionControlMessage{
			Type:     ControlTurnEnd,
			Protocol: p.Name(),
			Metadata: metadata,
			RawEvent: msg.Type,
		}

	case "error":
		// Session error - treat as potential BYE
		if msg.Error.Message != "" {
			metadata["error"] = msg.Error.Message
		}
		return &SessionControlMessage{
			Type:     ControlBye,
			Protocol: p.Name(),
			Metadata: metadata,
			RawEvent: msg.Type,
		}

	// Transcript events - user speech transcribed
	case "conversation.item.input_audio_transcription.completed":
		if msg.Transcript != "" {
			return &SessionControlMessage{
				Type:              ControlNone, // Not a control message, just transcript
				Protocol:          p.Name(),
				Metadata:          metadata,
				RawEvent:          msg.Type,
				Transcript:        msg.Transcript,
				TranscriptSpeaker: "user",
				TranscriptFinal:   true,
				TranscriptSource:  "stt",
			}
		}

	// Transcript events - assistant audio transcript (incremental)
	case "response.audio_transcript.delta":
		if msg.Delta != "" {
			return &SessionControlMessage{
				Type:              ControlNone,
				Protocol:          p.Name(),
				Metadata:          metadata,
				RawEvent:          msg.Type,
				Transcript:        msg.Delta,
				TranscriptSpeaker: "assistant",
				TranscriptFinal:   false,
				TranscriptSource:  "stt",
			}
		}

	// Transcript events - assistant audio transcript (final)
	case "response.audio_transcript.done":
		if msg.Transcript != "" {
			return &SessionControlMessage{
				Type:              ControlNone,
				Protocol:          p.Name(),
				Metadata:          metadata,
				RawEvent:          msg.Type,
				Transcript:        msg.Transcript,
				TranscriptSpeaker: "assistant",
				TranscriptFinal:   true,
				TranscriptSource:  "stt",
			}
		}

	// Text response events
	case "response.text.delta":
		if msg.Delta != "" {
			return &SessionControlMessage{
				Type:              ControlNone,
				Protocol:          p.Name(),
				Metadata:          metadata,
				RawEvent:          msg.Type,
				Transcript:        msg.Delta,
				TranscriptSpeaker: "assistant",
				TranscriptFinal:   false,
				TranscriptSource:  "text",
			}
		}

	case "response.text.done":
		if msg.Text != "" {
			return &SessionControlMessage{
				Type:              ControlNone,
				Protocol:          p.Name(),
				Metadata:          metadata,
				RawEvent:          msg.Type,
				Transcript:        msg.Text,
				TranscriptSpeaker: "assistant",
				TranscriptFinal:   true,
				TranscriptSource:  "text",
			}
		}
	}

	return nil
}

// Deepgram STT Protocol Parser
// https://developers.deepgram.com/docs/streaming

type DeepgramParser struct{}

func (p *DeepgramParser) Name() string { return "deepgram" }

func (p *DeepgramParser) Parse(data []byte) *SessionControlMessage {
	var msg struct {
		Type     string `json:"type"`
		IsFinal  bool   `json:"is_final"`
		Metadata struct {
			RequestID string `json:"request_id"`
			ModelInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"model_info"`
		} `json:"metadata"`
		Channel struct {
			Alternatives []struct {
				Transcript string  `json:"transcript"`
				Confidence float64 `json:"confidence"`
			} `json:"alternatives"`
		} `json:"channel"`
	}

	if err := json.Unmarshal(data, &msg); err != nil {
		return nil
	}

	metadata := make(map[string]string)

	switch msg.Type {
	case "Metadata":
		// Session started - server sending metadata
		if msg.Metadata.RequestID != "" {
			metadata["request_id"] = msg.Metadata.RequestID
		}
		if msg.Metadata.ModelInfo.Name != "" {
			metadata["model"] = msg.Metadata.ModelInfo.Name
		}
		return &SessionControlMessage{
			Type:     ControlOK,
			Protocol: p.Name(),
			Metadata: metadata,
			RawEvent: msg.Type,
		}

	case "Results":
		// Transcription result with transcript content
		if len(msg.Channel.Alternatives) > 0 && msg.Channel.Alternatives[0].Transcript != "" {
			return &SessionControlMessage{
				Type:              ControlNone, // Not a control message, just transcript
				Protocol:          p.Name(),
				Metadata:          metadata,
				RawEvent:          msg.Type,
				Transcript:        msg.Channel.Alternatives[0].Transcript,
				TranscriptSpeaker: "user",
				TranscriptFinal:   msg.IsFinal,
				TranscriptSource:  "stt",
			}
		}
		return nil

	case "UtteranceEnd":
		// End of utterance - turn end
		return &SessionControlMessage{
			Type:     ControlTurnEnd,
			Protocol: p.Name(),
			Metadata: metadata,
			RawEvent: msg.Type,
		}

	case "SpeechStarted":
		// Speech detected - turn start
		return &SessionControlMessage{
			Type:     ControlTurnStart,
			Protocol: p.Name(),
			Metadata: metadata,
			RawEvent: msg.Type,
		}
	}

	return nil
}

// ElevenLabs TTS Protocol Parser
// https://elevenlabs.io/docs/api-reference/websockets

type ElevenLabsParser struct{}

func (p *ElevenLabsParser) Name() string { return "elevenlabs" }

func (p *ElevenLabsParser) Parse(data []byte) *SessionControlMessage {
	var msg struct {
		Text                     string `json:"text"`
		VoiceSettings            any    `json:"voice_settings"`
		GenerationConfig         any    `json:"generation_config"`
		XIAPIKey                 string `json:"xi_api_key"`
		AuthorizationBearerToken string `json:"authorization"`
		Flush                    bool   `json:"flush"`
		// Server responses
		Audio               string `json:"audio"`
		IsFinal             bool   `json:"isFinal"`
		NormalizedAlignment any    `json:"normalizedAlignment"`
	}

	if err := json.Unmarshal(data, &msg); err != nil {
		return nil
	}

	metadata := make(map[string]string)

	// Detect session start (first text message with settings)
	if msg.VoiceSettings != nil || msg.GenerationConfig != nil {
		return &SessionControlMessage{
			Type:     ControlInvite,
			Protocol: p.Name(),
			Metadata: metadata,
			RawEvent: "voice_settings",
		}
	}

	// Capture text being sent for TTS (what the assistant is saying)
	if msg.Text != "" && !msg.Flush {
		return &SessionControlMessage{
			Type:              ControlNone,
			Protocol:          p.Name(),
			Metadata:          metadata,
			RawEvent:          "text",
			Transcript:        msg.Text,
			TranscriptSpeaker: "assistant", // TTS is assistant speaking
			TranscriptFinal:   false,       // Not final until flush
			TranscriptSource:  "tts",
		}
	}

	// Detect turn end (flush or isFinal)
	if msg.Flush || msg.IsFinal {
		return &SessionControlMessage{
			Type:     ControlTurnEnd,
			Protocol: p.Name(),
			Metadata: metadata,
			RawEvent: "flush",
		}
	}

	return nil
}

// LiveKit Protocol Parser (for LiveKit Agents)
// https://docs.livekit.io/agents/

type LiveKitParser struct{}

func (p *LiveKitParser) Name() string { return "livekit" }

func (p *LiveKitParser) Parse(data []byte) *SessionControlMessage {
	var msg struct {
		Type string `json:"type"`
		// Various LiveKit event fields
		Participant struct {
			Identity string `json:"identity"`
			SID      string `json:"sid"`
		} `json:"participant"`
		Room struct {
			Name string `json:"name"`
			SID  string `json:"sid"`
		} `json:"room"`
	}

	if err := json.Unmarshal(data, &msg); err != nil {
		return nil
	}

	metadata := make(map[string]string)

	switch msg.Type {
	case "participant_joined", "room_joined":
		if msg.Participant.Identity != "" {
			metadata["participant"] = msg.Participant.Identity
		}
		if msg.Room.Name != "" {
			metadata["room"] = msg.Room.Name
		}
		return &SessionControlMessage{
			Type:     ControlInvite,
			Protocol: p.Name(),
			Metadata: metadata,
			RawEvent: msg.Type,
		}

	case "participant_left", "room_left", "disconnected":
		return &SessionControlMessage{
			Type:     ControlBye,
			Protocol: p.Name(),
			Metadata: metadata,
			RawEvent: msg.Type,
		}
	}

	return nil
}

// Custom Pattern Parser for user-defined protocols

type CustomPatternConfig struct {
	Patterns []CustomPattern `yaml:"patterns"`
}

type CustomPattern struct {
	Name     string             `yaml:"name"`
	Type     SessionControlType `yaml:"-"`
	TypeStr  string             `yaml:"type"`    // "invite", "bye", "ok", etc.
	Pattern  string             `yaml:"pattern"` // Regex pattern
	compiled *regexp.Regexp
}

type CustomPatternParser struct {
	patterns []CustomPattern
}

func NewCustomPatternParser(config *CustomPatternConfig) *CustomPatternParser {
	parser := &CustomPatternParser{}

	for _, p := range config.Patterns {
		compiled, err := regexp.Compile(p.Pattern)
		if err != nil {
			continue // Skip invalid patterns
		}
		p.compiled = compiled
		p.Type = parseControlType(p.TypeStr)
		parser.patterns = append(parser.patterns, p)
	}

	return parser
}

func (p *CustomPatternParser) Name() string { return "custom" }

func (p *CustomPatternParser) Parse(data []byte) *SessionControlMessage {
	text := string(data)

	for _, pattern := range p.patterns {
		if pattern.compiled != nil && pattern.compiled.MatchString(text) {
			return &SessionControlMessage{
				Type:     pattern.Type,
				Protocol: p.Name(),
				Metadata: map[string]string{"pattern_name": pattern.Name},
				RawEvent: pattern.Name,
			}
		}
	}

	return nil
}

func parseControlType(s string) SessionControlType {
	switch strings.ToLower(s) {
	case "invite":
		return ControlInvite
	case "ok":
		return ControlOK
	case "bye":
		return ControlBye
	case "hold":
		return ControlHold
	case "resume":
		return ControlResume
	case "cancel":
		return ControlCancel
	case "turn_start":
		return ControlTurnStart
	case "turn_end":
		return ControlTurnEnd
	default:
		return ControlNone
	}
}

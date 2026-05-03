package telemetry

import (
	"encoding/json"
	"time"
)

// OCSF schema version
const ocsfVersion = "1.8.0"

// OCSF class UIDs — consolidated on Detection Finding (class 2004)
const (
	OCSFClassDetectionFinding = 2004
)

// OCSF category UIDs
const (
	OCSFCategoryFindings = 2
)

// OCSF activity IDs (required in 1.8)
const (
	OCSFActivityCreate = 1
)

// OCSF severity IDs
const (
	OCSFSeverityInfo     = 1
	OCSFSeverityWarning  = 3
	OCSFSeverityCritical = 5
)

// OCSFMetadata contains event metadata
type OCSFMetadata struct {
	Product OCSFProduct `json:"product"`
	Version string      `json:"version"`
}

// OCSFProduct identifies the source product
type OCSFProduct struct {
	Name       string `json:"name"`
	VendorName string `json:"vendor_name"`
	Version    string `json:"version"`
	UID        string `json:"uid,omitempty"`
}

// OCSFActor represents the actor in an event
type OCSFActor struct {
	Session OCSFSession `json:"session"`
}

// OCSFSession represents a session reference
type OCSFSession struct {
	UID string `json:"uid"`
}

// OCSFFinding contains finding details
type OCSFFinding struct {
	Title string   `json:"title"`
	Desc  string   `json:"desc"`
	Types []string `json:"types"`
}

// OCSFAnalytic describes the detection analytic
type OCSFAnalytic struct {
	Name string `json:"name"`
	Type string `json:"type"`
	UID  string `json:"uid,omitempty"`
}

// OCSFAIModel represents the ai_model object from the ai_operation profile
type OCSFAIModel struct {
	AIProvider string `json:"ai_provider"`
	Name       string `json:"name"`
	UID        string `json:"uid,omitempty"`
	Version    string `json:"version,omitempty"`
}

// OCSFMessageContext represents the message_context object from the ai_operation profile
type OCSFMessageContext struct {
	AIRoleID         int    `json:"ai_role_id,omitempty"`
	PromptTokens     int64  `json:"prompt_tokens,omitempty"`
	CompletionTokens int64  `json:"completion_tokens,omitempty"`
	TotalTokens      int64  `json:"total_tokens,omitempty"`
	UID              string `json:"uid,omitempty"`
	Name             string `json:"name,omitempty"`
}

// OCSFUnmapped holds ELIDA-specific fields not in OCSF schema
type OCSFUnmapped struct {
	Backend       string  `json:"elida.backend,omitempty"`
	Action        string  `json:"elida.action,omitempty"`
	MatchedText   string  `json:"elida.matched_text,omitempty"`
	SourceRole    string  `json:"elida.source_role,omitempty"`
	Model         string  `json:"elida.model,omitempty"`
	CompoundScore float64 `json:"elida.compound_score,omitempty"`
	RateScore     float64 `json:"elida.rate_score,omitempty"`
	EntropyScore  float64 `json:"elida.entropy_score,omitempty"`
	SDRRootHash   string  `json:"elida.sdr_root_hash,omitempty"`
	SDREventHash  string  `json:"elida.sdr_event_hash,omitempty"`
	SDREventIndex *int    `json:"elida.sdr_event_index,omitempty"`
	SDRProof      any     `json:"elida.sdr_proof,omitempty"`
}

// OCSFDetectionFinding represents OCSF class 2004 — Detection Finding
// with ai_operation profile overlay fields
type OCSFDetectionFinding struct {
	ClassUID    int          `json:"class_uid"`
	CategoryUID int          `json:"category_uid"`
	TypeUID     int          `json:"type_uid"`
	ActivityID  int          `json:"activity_id"`
	SeverityID  int          `json:"severity_id"`
	Time        int64        `json:"time"`
	Message     string       `json:"message"`
	Metadata    OCSFMetadata `json:"metadata"`
	FindingInfo OCSFFinding  `json:"finding_info"`
	Analytic    OCSFAnalytic `json:"analytic"`
	Actor       OCSFActor    `json:"actor"`

	// ai_operation profile overlay
	AIModel        *OCSFAIModel        `json:"ai_model,omitempty"`
	MessageContext *OCSFMessageContext `json:"message_context,omitempty"`

	Unmapped OCSFUnmapped `json:"unmapped,omitempty"`
}

// MapSeverityToOCSF maps ELIDA severity strings to OCSF severity IDs
func MapSeverityToOCSF(severity string) int {
	switch severity {
	case "critical":
		return OCSFSeverityCritical
	case "warning":
		return OCSFSeverityWarning
	default:
		return OCSFSeverityInfo
	}
}

func newMetadata() OCSFMetadata {
	return OCSFMetadata{
		Product: OCSFProduct{
			Name:       "ELIDA",
			VendorName: "ELIDA",
			Version:    "0.4.3",
			UID:        "elida",
		},
		Version: ocsfVersion,
	}
}

func nowMillis() int64 {
	return time.Now().UnixMilli()
}

// BuildPolicyDetection builds a Detection Finding (class 2004) for session-end policy violations.
// Replaces the former BuildSecurityFinding (class 2001, deprecated in OCSF 1.8).
func BuildPolicyDetection(sessionID string, v Violation, record SessionRecord) OCSFDetectionFinding {
	severityID := MapSeverityToOCSF(v.Severity)

	eventCategory := v.EventCategory
	if eventCategory == "" {
		eventCategory = "policy_violation"
	}

	finding := OCSFDetectionFinding{
		ClassUID:    OCSFClassDetectionFinding,
		CategoryUID: OCSFCategoryFindings,
		TypeUID:     200401, // Detection Finding: Create
		ActivityID:  OCSFActivityCreate,
		SeverityID:  severityID,
		Time:        nowMillis(),
		Message:     "Policy violation: " + v.RuleName,
		Metadata:    newMetadata(),
		FindingInfo: OCSFFinding{
			Title: v.RuleName,
			Desc:  v.Description,
			Types: []string{eventCategory},
		},
		Analytic: OCSFAnalytic{
			Name: v.RuleName,
			Type: "Rule",
			UID:  v.FrameworkRef,
		},
		Actor: OCSFActor{
			Session: OCSFSession{UID: sessionID},
		},
		Unmapped: OCSFUnmapped{
			Backend:     record.Backend,
			Action:      v.Action,
			MatchedText: v.MatchedText,
			SourceRole:  v.SourceRole,
			Model:       record.Model,
			SDRRootHash: record.SDRRootHash,
		},
	}

	// Populate ai_model from record
	if record.Backend != "" {
		finding.AIModel = &OCSFAIModel{
			AIProvider: record.Backend,
			Name:       record.Model,
		}
	}

	// Populate message_context with token counts at session end
	if record.TokensIn > 0 || record.TokensOut > 0 {
		finding.MessageContext = &OCSFMessageContext{
			PromptTokens:     record.TokensIn,
			CompletionTokens: record.TokensOut,
			TotalTokens:      record.TokensIn + record.TokensOut,
			UID:              sessionID,
		}
	}

	return finding
}

// BuildBlockDetection builds a Detection Finding (class 2004) for real-time block events.
// Replaces the former BuildPolicyViolation (class 6003, removed in OCSF 1.8).
func BuildBlockDetection(sessionID, ruleName, matchedText, backend, model string) OCSFDetectionFinding {
	finding := OCSFDetectionFinding{
		ClassUID:    OCSFClassDetectionFinding,
		CategoryUID: OCSFCategoryFindings,
		TypeUID:     200401, // Detection Finding: Create
		ActivityID:  OCSFActivityCreate,
		SeverityID:  OCSFSeverityCritical,
		Time:        nowMillis(),
		Message:     "Request blocked: " + ruleName,
		Metadata:    newMetadata(),
		FindingInfo: OCSFFinding{
			Title: ruleName,
			Desc:  "Real-time block",
			Types: []string{"policy_violation"},
		},
		Analytic: OCSFAnalytic{
			Name: ruleName,
			Type: "Rule",
		},
		Actor: OCSFActor{
			Session: OCSFSession{UID: sessionID},
		},
		Unmapped: OCSFUnmapped{
			Backend:     backend,
			Action:      "block",
			MatchedText: matchedText,
			Model:       model,
		},
	}

	if backend != "" {
		finding.AIModel = &OCSFAIModel{
			AIProvider: backend,
			Name:       model,
		}
	}

	return finding
}

// BuildAnomalyDetection builds a Detection Finding (class 2004) for M3-lite anomaly scores.
// The score parameter is a Mahalanobis distance (typically 0-10+).
// Severity mapping: >=5.0 critical, >=4.1 warning, <4.1 info.
func BuildAnomalyDetection(sessionID string, score float64, bucket, class string) OCSFDetectionFinding {
	severityID := OCSFSeverityInfo
	if score >= 5.0 {
		severityID = OCSFSeverityCritical
	} else if score >= 4.1 {
		severityID = OCSFSeverityWarning
	}

	return OCSFDetectionFinding{
		ClassUID:    OCSFClassDetectionFinding,
		CategoryUID: OCSFCategoryFindings,
		TypeUID:     200401, // Detection Finding: Create
		ActivityID:  OCSFActivityCreate,
		SeverityID:  severityID,
		Time:        nowMillis(),
		Message:     "Behavioral anomaly: " + class,
		Metadata:    newMetadata(),
		FindingInfo: OCSFFinding{
			Title: class,
			Desc:  bucket,
			Types: []string{"behavioral_anomaly"},
		},
		Analytic: OCSFAnalytic{
			Name: "M3-lite",
			Type: "ML/AI",
		},
		Actor: OCSFActor{
			Session: OCSFSession{UID: sessionID},
		},
	}
}

// BuildCompoundAnomalyDetection builds a Detection Finding (class 2004) for compound
// anomaly detections (adaptive CUSUM + Shannon entropy). Emitted in real-time when the
// compound detector fires, not just at session end.
func BuildCompoundAnomalyDetection(sessionID string, compoundScore, rateScore, entropyScore float64, ruleName string) OCSFDetectionFinding {
	severityID := OCSFSeverityInfo
	if compoundScore >= 0.5 {
		severityID = OCSFSeverityCritical
	} else if compoundScore >= 0.15 {
		severityID = OCSFSeverityWarning
	}

	return OCSFDetectionFinding{
		ClassUID:    OCSFClassDetectionFinding,
		CategoryUID: OCSFCategoryFindings,
		TypeUID:     200401, // Detection Finding: Create
		ActivityID:  OCSFActivityCreate,
		SeverityID:  severityID,
		Time:        nowMillis(),
		Message:     "Compound anomaly: sustained high-rate + high-entropy burst",
		Metadata:    newMetadata(),
		FindingInfo: OCSFFinding{
			Title: ruleName,
			Desc:  "Adaptive CUSUM rate anomaly combined with elevated Shannon entropy",
			Types: []string{"compound_anomaly"},
		},
		Analytic: OCSFAnalytic{
			Name: "M3-CUSUM",
			Type: "Statistical",
		},
		Actor: OCSFActor{
			Session: OCSFSession{UID: sessionID},
		},
		Unmapped: OCSFUnmapped{
			Action:        "flag",
			CompoundScore: compoundScore,
			RateScore:     rateScore,
			EntropyScore:  entropyScore,
		},
	}
}

// MarshalOCSFEvent serializes any OCSF event to JSON
func MarshalOCSFEvent(event any) ([]byte, error) {
	return json.Marshal(event)
}

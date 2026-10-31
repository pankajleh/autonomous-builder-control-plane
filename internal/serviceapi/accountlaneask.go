package serviceapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// An account lane answers one question (Repo C design note AI_IN_EVERY_APP §10, AE3; docs/plans/account-lane-ask.md):
// an administrator's own app, while they try it themselves, asks the model through their own signed-in Claude account.
// The request is the relay's, {system?, messages, max_tokens?}, with texts and pictures and no tools, checked here as
// the relay checks it.

// AccountLaneAskV1 is one question, checked.
type AccountLaneAskV1 struct {
	System string
	Turns  []AccountLaneTurnV1
	// MaxTokens is the request's, 0 when it gave none. Claude Code has no limit for one answer's tokens (its
	// CLAUDE_CODE_MAX_OUTPUT_TOKENS makes it ask again, then fail), so it is checked and not passed on: the question's
	// time and the bounded output limit the answer.
	MaxTokens int
}

// AccountLaneTurnV1 is one turn of the conversation, "user" or "assistant"; content given as a string is one text part.
type AccountLaneTurnV1 struct {
	Role  string
	Parts []AccountLanePartV1
}

// AccountLanePartV1 is a text, or a user's picture: its media type and its base64 data, as given.
type AccountLanePartV1 struct {
	Text      string
	MediaType string
	Data      string
}

// Picture reports whether the part is a picture.
func (p AccountLanePartV1) Picture() bool { return p.MediaType != "" }

// AccountLaneAnswerV1 is the answer: its text and the tokens it took, null when Claude Code did not say.
type AccountLaneAnswerV1 struct {
	Text         string `json:"text"`
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
}

var (
	ErrAccountLaneAskInvalid     = errors.New("account lane question invalid")
	ErrAccountLaneAskUnsupported = errors.New("account lane does not answer questions")
	ErrAccountLaneSignedOut      = errors.New("account lane signed out")
	ErrAccountLaneBusy           = errors.New("account lane busy")
	ErrAccountLaneTimeout        = errors.New("account lane timed out")
)

// The relay's limits (Repo C AppAiService and AppAiController, AE1): characters are counted as Java counts them, in
// UTF-16 code units.
const (
	// MaxAccountLaneAskBytes bounds a question's body: the relay's 1 MB and 16 KB more.
	MaxAccountLaneAskBytes = 1<<20 + 16<<10
	// MaxAccountLaneAskTimeout bounds the time a question may be given (abcp serve --account-lane-ask-timeout).
	MaxAccountLaneAskTimeout = time.Minute

	maxAskSystem       = 8_000
	maxAskTurns        = 40
	maxAskParts        = 20
	maxAskText         = 20_000
	maxAskTokens       = 1_024
	maxAskPictures     = 3
	maxAskPictureBytes = 300 * 1024
)

// accountLaneAskWriteTimeout lets a question's answer be written after its whole time, past the server's WriteTimeout.
const accountLaneAskWriteTimeout = MaxAccountLaneAskTimeout + 15*time.Second

var askPictureTypes = map[string]bool{"image/jpeg": true, "image/png": true, "image/webp": true}

// ParseAccountLaneAsk checks a question's body: strict JSON of exactly {system?, messages, max_tokens?}; the system at
// most 8,000 characters; 1 to 40 turns, alternating, the first and the last the user's; each turn's content a string of
// 1 to 20,000 characters or 1 to 20 parts, a text of 1 to 20,000 characters or, in a user's turn, a picture (JPEG, PNG
// or WebP, padded base64 without line breaks, at most 300 KB decoded and starting as its type does), at most 3 in all;
// max_tokens 1 to 1,024. Anything else, tools included, is ErrAccountLaneAskInvalid.
func ParseAccountLaneAsk(data []byte) (AccountLaneAskV1, error) {
	var wire struct {
		System    json.RawMessage `json:"system"`
		Messages  json.RawMessage `json:"messages"`
		MaxTokens json.RawMessage `json:"max_tokens"`
	}
	if len(data) == 0 || len(data) > MaxAccountLaneAskBytes || decodeStrictJSON(data, &wire) != nil {
		return AccountLaneAskV1{}, ErrAccountLaneAskInvalid
	}
	var ask AccountLaneAskV1
	if wire.System != nil {
		system, ok := askText(wire.System)
		// A system prompt is passed as an argument, which cannot hold a NUL.
		if !ok || utf16Length(system) > maxAskSystem || strings.ContainsRune(system, 0) {
			return AccountLaneAskV1{}, ErrAccountLaneAskInvalid
		}
		ask.System = system
	}
	if wire.MaxTokens != nil {
		if bytes.Equal(wire.MaxTokens, []byte("null")) || json.Unmarshal(wire.MaxTokens, &ask.MaxTokens) != nil ||
			ask.MaxTokens < 1 || ask.MaxTokens > maxAskTokens {
			return AccountLaneAskV1{}, ErrAccountLaneAskInvalid
		}
	}
	var turns []json.RawMessage
	if wire.Messages == nil || json.Unmarshal(wire.Messages, &turns) != nil || len(turns) == 0 || len(turns) > maxAskTurns {
		return AccountLaneAskV1{}, ErrAccountLaneAskInvalid
	}
	pictures := 0
	for index, raw := range turns {
		turn, err := parseAskTurn(raw, &pictures)
		if err != nil {
			return AccountLaneAskV1{}, err
		}
		if (index == 0 || index == len(turns)-1) && turn.Role != "user" {
			return AccountLaneAskV1{}, ErrAccountLaneAskInvalid
		}
		if index > 0 && turn.Role == ask.Turns[index-1].Role {
			return AccountLaneAskV1{}, ErrAccountLaneAskInvalid
		}
		ask.Turns = append(ask.Turns, turn)
	}
	return ask, nil
}

func parseAskTurn(raw json.RawMessage, pictures *int) (AccountLaneTurnV1, error) {
	var wire struct {
		Role    json.RawMessage `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if decodeStrictJSON(raw, &wire) != nil || wire.Role == nil || wire.Content == nil {
		return AccountLaneTurnV1{}, ErrAccountLaneAskInvalid
	}
	role, ok := askText(wire.Role)
	if !ok || (role != "user" && role != "assistant") {
		return AccountLaneTurnV1{}, ErrAccountLaneAskInvalid
	}
	turn := AccountLaneTurnV1{Role: role}
	if text, ok := askText(wire.Content); ok {
		if text == "" || utf16Length(text) > maxAskText {
			return AccountLaneTurnV1{}, ErrAccountLaneAskInvalid
		}
		turn.Parts = []AccountLanePartV1{{Text: text}}
		return turn, nil
	}
	var parts []json.RawMessage
	if !bytes.HasPrefix(wire.Content, []byte("[")) || json.Unmarshal(wire.Content, &parts) != nil || len(parts) == 0 || len(parts) > maxAskParts {
		return AccountLaneTurnV1{}, ErrAccountLaneAskInvalid
	}
	for _, raw := range parts {
		part, err := parseAskPart(raw, role)
		if err != nil {
			return AccountLaneTurnV1{}, err
		}
		if part.Picture() {
			if *pictures++; *pictures > maxAskPictures {
				return AccountLaneTurnV1{}, ErrAccountLaneAskInvalid
			}
		}
		turn.Parts = append(turn.Parts, part)
	}
	return turn, nil
}

func parseAskPart(raw json.RawMessage, role string) (AccountLanePartV1, error) {
	var wire struct {
		Type      json.RawMessage `json:"type"`
		Text      json.RawMessage `json:"text"`
		MediaType json.RawMessage `json:"media_type"`
		Data      json.RawMessage `json:"data"`
	}
	if decodeStrictJSON(raw, &wire) != nil {
		return AccountLanePartV1{}, ErrAccountLaneAskInvalid
	}
	kind, _ := askText(wire.Type)
	switch {
	case kind == "text" && wire.Text != nil && wire.MediaType == nil && wire.Data == nil:
		text, ok := askText(wire.Text)
		if !ok || text == "" || utf16Length(text) > maxAskText {
			return AccountLanePartV1{}, ErrAccountLaneAskInvalid
		}
		return AccountLanePartV1{Text: text}, nil
	case kind == "image" && role == "user" && wire.Text == nil && wire.MediaType != nil && wire.Data != nil:
		mediaType, typeOK := askText(wire.MediaType)
		data, dataOK := askText(wire.Data)
		if !typeOK || !dataOK || !askPicture(mediaType, data) {
			return AccountLanePartV1{}, ErrAccountLaneAskInvalid
		}
		return AccountLanePartV1{MediaType: mediaType, Data: data}, nil
	}
	return AccountLanePartV1{}, ErrAccountLaneAskInvalid
}

// askPicture checks a picture as the relay does: one of the three types; plain padded base64 (no data: prefix, no line
// breaks); at most 300 KB decoded; its first bytes its type's (JPEG's FF D8 FF, PNG's signature, WebP's RIFF....WEBP).
func askPicture(mediaType, data string) bool {
	if !askPictureTypes[mediaType] || data == "" || len(data)%4 != 0 {
		return false
	}
	for index := 0; index < len(data); index++ {
		c := data[index]
		digit := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '+' || c == '/'
		if !digit && !(c == '=' && (index == len(data)-1 || index == len(data)-2 && data[len(data)-1] == '=')) {
			return false
		}
	}
	if base64.StdEncoding.DecodedLen(len(data))-strings.Count(data[len(data)-2:], "=") > maxAskPictureBytes {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return false
	}
	switch mediaType {
	case "image/jpeg":
		return len(decoded) >= 3 && decoded[0] == 0xFF && decoded[1] == 0xD8 && decoded[2] == 0xFF
	case "image/png":
		return bytes.HasPrefix(decoded, []byte("\x89PNG\r\n\x1a\n"))
	default:
		return len(decoded) >= 12 && string(decoded[0:4]) == "RIFF" && string(decoded[8:12]) == "WEBP"
	}
}

// askText answers a JSON string's value; anything else (a number, null, a list) is not one.
func askText(raw json.RawMessage) (string, bool) {
	var text string
	if !bytes.HasPrefix(raw, []byte(`"`)) || json.Unmarshal(raw, &text) != nil {
		return "", false
	}
	return text, true
}

// utf16Length counts a text's characters as Java's String.length does, in UTF-16 code units.
func utf16Length(text string) int {
	length := 0
	for _, r := range text {
		length++
		if r >= 0x10000 {
			length++
		}
	}
	return length
}

// isAccountLaneAsk reports whether a request is a question to an account lane, whose body has its own bound.
func isAccountLaneAsk(r *http.Request) bool {
	return r.Method == http.MethodPost && r.URL.RawPath == "" && strings.HasPrefix(r.URL.Path, "/v1/account-lanes/") &&
		strings.HasSuffix(r.URL.Path, "/ask")
}

// POST /v1/account-lanes/{lane}/ask: the body is checked before the lane is asked, so a wrong body answers 400 for
// any lane. Nothing of the question or the answer is logged; the request log keeps only the path.
func (s *Server) accountLaneAsk(w http.ResponseWriter, r *http.Request, lane, id string) {
	// The answer may come after the server's write timeout; the lane bounds the question's time below this.
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(accountLaneAskWriteTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		s.writeAccountLaneError(w, id, ErrAccountLaneUnavailable)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, MaxAccountLaneAskBytes+1))
	if err != nil {
		s.writeAccountLaneError(w, id, ErrAccountLaneAskInvalid)
		return
	}
	ask, err := ParseAccountLaneAsk(data)
	if err != nil {
		s.writeAccountLaneError(w, id, ErrAccountLaneAskInvalid)
		return
	}
	answer, err := s.reserved.AccountLanes.Ask(r.Context(), lane, ask)
	if err != nil {
		s.writeAccountLaneError(w, id, err)
		return
	}
	s.writeJSON(w, http.StatusOK, answer)
}

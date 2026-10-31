package serviceapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testPicture is base64 of size bytes starting as mediaType's do.
func testPicture(mediaType string, size int) string {
	data := make([]byte, size)
	switch mediaType {
	case "image/jpeg":
		copy(data, []byte{0xFF, 0xD8, 0xFF})
	case "image/png":
		copy(data, "\x89PNG\r\n\x1a\n")
	case "image/webp":
		copy(data, "RIFF\x00\x00\x00\x00WEBP")
	}
	return base64.StdEncoding.EncodeToString(data)
}

func testText(value string) map[string]any { return map[string]any{"type": "text", "text": value} }

func testImage(mediaType, data string) map[string]any {
	return map[string]any{"type": "image", "media_type": mediaType, "data": data}
}

func testTurn(role string, content any) map[string]any {
	return map[string]any{"role": role, "content": content}
}

func testJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(data)
}

const testAskPath = "/v1/account-lanes/claude-account/ask"

func TestAnAccountLaneAnswersOneQuestionWithItsGrantAndLogsOnlyThePath(t *testing.T) {
	s, backend := accountServer(t, true)
	var log bytes.Buffer
	s.reserved.RequestLog = &log
	in, out := int64(1210), int64(42)
	backend.answer = AccountLaneAnswerV1{Text: "Rice, dal and two sabzis.", InputTokens: &in, OutputTokens: &out}
	jpeg, png := testPicture("image/jpeg", 600), testPicture("image/png", 900)
	question := testJSON(map[string]any{"system": "You are a calorie lens.", "max_tokens": 512, "messages": []any{
		testTurn("user", []any{testImage("image/jpeg", jpeg), testText("What is on this plate?")}),
		testTurn("assistant", "A thali."),
		testTurn("user", []any{testText("And this one?"), testImage("image/png", png)}),
	}})
	response := call(s, http.MethodPost, testAskPath, question)
	if response.Code != http.StatusOK || response.Body.String() != `{"text":"Rice, dal and two sabzis.","input_tokens":1210,"output_tokens":42}`+"\n" {
		t.Fatalf("ask = %d %s", response.Code, response.Body.String())
	}
	if len(backend.asks) != 1 || backend.asks[0] != "claude-account" {
		t.Fatalf("asked %v", backend.asks)
	}
	ask := backend.asked[0]
	if ask.System != "You are a calorie lens." || ask.MaxTokens != 512 || len(ask.Turns) != 3 ||
		ask.Turns[0].Role != "user" || len(ask.Turns[0].Parts) != 2 || !ask.Turns[0].Parts[0].Picture() || ask.Turns[0].Parts[0].Data != jpeg ||
		ask.Turns[0].Parts[1].Text != "What is on this plate?" ||
		ask.Turns[1].Role != "assistant" || len(ask.Turns[1].Parts) != 1 || ask.Turns[1].Parts[0].Text != "A thali." ||
		ask.Turns[2].Parts[0].Text != "And this one?" || ask.Turns[2].Parts[1].MediaType != "image/png" || ask.Turns[2].Parts[1].Data != png {
		t.Fatalf("the question reaches the lane as asked: %+v", ask)
	}
	line := log.String()
	if !strings.Contains(line, "method=POST path="+testAskPath+" status=200") {
		t.Fatalf("the request is logged: %q", line)
	}
	for _, secret := range []string{"calorie", "plate", "thali", "sabzis", jpeg[:16]} {
		if strings.Contains(line, secret) {
			t.Fatalf("nothing of the question or the answer is logged: %q", line)
		}
	}

	backend.answer = AccountLaneAnswerV1{Text: "Yes."}
	if response := call(s, http.MethodPost, testAskPath, `{"messages":[{"role":"user","content":"Is it lunch?"}]}`); response.Code != http.StatusOK ||
		response.Body.String() != `{"text":"Yes.","input_tokens":null,"output_tokens":null}`+"\n" {
		t.Fatalf("counts Claude Code did not give are null: %d %s", response.Code, response.Body.String())
	}
	if ask := backend.asked[1]; ask.System != "" || ask.MaxTokens != 0 || len(ask.Turns) != 1 || ask.Turns[0].Parts[0].Text != "Is it lunch?" {
		t.Fatalf("a string content is one text: %+v", ask)
	}
}

func TestAQuestionsRefusalsHaveTheirOwnCodes(t *testing.T) {
	s, backend := accountServer(t, true)
	question := `{"messages":[{"role":"user","content":"hi"}]}`
	for err, want := range map[error]struct {
		status int
		code   string
	}{
		ErrAccountLaneUnknown:        {http.StatusNotFound, "account_lane_unknown"},
		ErrAccountLaneAskUnsupported: {http.StatusBadRequest, "account_lane_ask_unsupported"},
		ErrAccountLaneAskInvalid:     {http.StatusBadRequest, "account_lane_ask_invalid"},
		ErrAccountLaneSignedOut:      {http.StatusConflict, "account_lane_signed_out"},
		ErrAccountLaneBusy:           {http.StatusTooManyRequests, "account_lane_busy"},
		ErrAccountLaneTimeout:        {http.StatusGatewayTimeout, "account_lane_timeout"},
		ErrAccountLaneUnavailable:    {http.StatusServiceUnavailable, "account_lane_unavailable"},
		errors.New("anything else"):  {http.StatusServiceUnavailable, "account_lane_unavailable"},
	} {
		backend.err = err
		response := call(s, http.MethodPost, testAskPath, question)
		if response.Code != want.status || !strings.Contains(response.Body.String(), `"code":"`+want.code+`"`) {
			t.Errorf("%v = %d %s", err, response.Code, response.Body.String())
		}
		if err == ErrAccountLaneBusy && response.Header().Get("Retry-After") != "1" {
			t.Errorf("a busy lane says when to ask again: %v", response.Header())
		}
	}
}

func TestAQuestionNeedsItsGrantItsFileAndASignedInCaller(t *testing.T) {
	question := `{"messages":[{"role":"user","content":"hi"}]}`
	s, backend := accountServer(t, false)
	if response := call(s, http.MethodPost, testAskPath, question); response.Code != http.StatusForbidden || len(backend.asks) != 0 {
		t.Fatalf("ungranted = %d, asked %v", response.Code, backend.asks)
	}
	s.reserved.AccountLanes = nil
	if response := call(s, http.MethodPost, testAskPath, question); response.Code != http.StatusNotImplemented {
		t.Fatalf("without the file = %d", response.Code)
	}
	anonymous := newTestServer(t, &testCatalog{}, time.Now().UTC())
	anonymous.reserved.AccountLanes = backend
	response := httptest.NewRecorder()
	anonymous.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, testAskPath, strings.NewReader(question)))
	if response.Code != http.StatusUnauthorized || len(backend.asks) != 0 {
		t.Fatalf("anonymous = %d, asked %v", response.Code, backend.asks)
	}
}

func TestAQuestionIsTheRelaysWithNoTools(t *testing.T) {
	s, backend := accountServer(t, true)
	hi := testTurn("user", "hi")
	jpeg := testPicture("image/jpeg", 64)
	many := func(count int, item any) []any {
		out := make([]any, count)
		for index := range out {
			out[index] = item
		}
		return out
	}
	alternating := func(count int) []any {
		out := make([]any, count)
		for index := range out {
			out[index] = testTurn([]string{"user", "assistant"}[index%2], "words")
		}
		return out
	}
	emoji := "\U0001F35B" // two UTF-16 code units, as the relay counts it
	for name, bad := range map[string]string{
		"an empty body":                  "",
		"no messages":                    `{}`,
		"no turns":                       `{"messages":[]}`,
		"messages that are not a list":   `{"messages":"hi"}`,
		"null messages":                  `{"messages":null}`,
		"41 turns":                       testJSON(map[string]any{"messages": alternating(41)}),
		"the assistant first":            testJSON(map[string]any{"messages": []any{testTurn("assistant", "hi"), hi}}),
		"the assistant last":             testJSON(map[string]any{"messages": alternating(2)}),
		"two user turns in a row":        testJSON(map[string]any{"messages": []any{hi, hi}}),
		"a system role":                  testJSON(map[string]any{"messages": []any{testTurn("system", "hi")}}),
		"a turn with a name":             `{"messages":[{"role":"user","content":"hi","name":"x"}]}`,
		"a turn without content":         `{"messages":[{"role":"user"}]}`,
		"a turn with null content":       `{"messages":[{"role":"user","content":null}]}`,
		"a Role":                         `{"messages":[{"Role":"user","content":"hi"}]}`,
		"empty content":                  testJSON(map[string]any{"messages": []any{testTurn("user", "")}}),
		"20,001 characters":              testJSON(map[string]any{"messages": []any{testTurn("user", strings.Repeat("a", 20_001))}}),
		"10,001 emoji":                   testJSON(map[string]any{"messages": []any{testTurn("user", strings.Repeat(emoji, 10_001))}}),
		"no parts":                       testJSON(map[string]any{"messages": []any{testTurn("user", []any{})}}),
		"21 parts":                       testJSON(map[string]any{"messages": []any{testTurn("user", many(21, testText("a")))}}),
		"an empty text":                  testJSON(map[string]any{"messages": []any{testTurn("user", []any{testText("")})}}),
		"a text with a media type":       `{"messages":[{"role":"user","content":[{"type":"text","text":"hi","media_type":"image/png"}]}]}`,
		"a text part of 20,001":          testJSON(map[string]any{"messages": []any{testTurn("user", []any{testText(strings.Repeat("a", 20_001))})}}),
		"a document":                     `{"messages":[{"role":"user","content":[{"type":"document","text":"hi"}]}]}`,
		"a part without a type":          `{"messages":[{"role":"user","content":[{"text":"hi"}]}]}`,
		"tools":                          `{"messages":[{"role":"user","content":"hi"}],"tools":[]}`,
		"tool_choice":                    `{"messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"auto"}}`,
		"a model":                        `{"messages":[{"role":"user","content":"hi"}],"model":"opus"}`,
		"a tool call":                    `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"look","input":{}}]},{"role":"user","content":"ok"}]}`,
		"a tool result":                  `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"x"}]}]}`,
		"an assistant's picture":         testJSON(map[string]any{"messages": []any{hi, testTurn("assistant", []any{testImage("image/jpeg", jpeg)}), hi}}),
		"a GIF":                          testJSON(map[string]any{"messages": []any{testTurn("user", []any{testImage("image/gif", jpeg)})}}),
		"a picture with a line break":    testJSON(map[string]any{"messages": []any{testTurn("user", []any{testImage("image/jpeg", jpeg[:40]+"\n"+jpeg[41:])})}}),
		"a picture unpadded":             testJSON(map[string]any{"messages": []any{testTurn("user", []any{testImage("image/jpeg", strings.TrimRight(testPicture("image/jpeg", 65), "="))})}}),
		"a data: link":                   testJSON(map[string]any{"messages": []any{testTurn("user", []any{testImage("image/jpeg", "data:image/jpeg;base64,"+jpeg)})}}),
		"a PNG that is a JPEG":           testJSON(map[string]any{"messages": []any{testTurn("user", []any{testImage("image/png", jpeg)})}}),
		"a WebP that is a JPEG":          testJSON(map[string]any{"messages": []any{testTurn("user", []any{testImage("image/webp", jpeg)})}}),
		"an empty picture":               testJSON(map[string]any{"messages": []any{testTurn("user", []any{testImage("image/jpeg", "")})}}),
		"a picture with a text":          `{"messages":[{"role":"user","content":[{"type":"image","media_type":"image/jpeg","data":"` + jpeg + `","text":"x"}]}]}`,
		"a picture in a source":          `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"` + jpeg + `"}}]}]}`,
		"four pictures":                  testJSON(map[string]any{"messages": []any{testTurn("user", many(2, testImage("image/jpeg", jpeg))), testTurn("assistant", "ok"), testTurn("user", many(2, testImage("image/jpeg", jpeg)))}}),
		"a picture over 300 KB":          testJSON(map[string]any{"messages": []any{testTurn("user", []any{testImage("image/jpeg", testPicture("image/jpeg", 300*1024+1))})}}),
		"a system of 8,001":              testJSON(map[string]any{"system": strings.Repeat("s", 8_001), "messages": []any{hi}}),
		"a system of 4,001 emoji":        testJSON(map[string]any{"system": strings.Repeat(emoji, 4_001), "messages": []any{hi}}),
		"a null system":                  `{"system":null,"messages":[{"role":"user","content":"hi"}]}`,
		"a system that is a list":        `{"system":[{"type":"text","text":"hi"}],"messages":[{"role":"user","content":"hi"}]}`,
		"a system with a NUL":            `{"system":"a\u0000b","messages":[{"role":"user","content":"hi"}]}`,
		"no tokens":                      `{"max_tokens":0,"messages":[{"role":"user","content":"hi"}]}`,
		"1,025 tokens":                   `{"max_tokens":1025,"messages":[{"role":"user","content":"hi"}]}`,
		"half a token":                   `{"max_tokens":1.5,"messages":[{"role":"user","content":"hi"}]}`,
		"tokens as a string":             `{"max_tokens":"512","messages":[{"role":"user","content":"hi"}]}`,
		"null tokens":                    `{"max_tokens":null,"messages":[{"role":"user","content":"hi"}]}`,
		"messages twice":                 `{"messages":[{"role":"user","content":"hi"}],"messages":[{"role":"user","content":"hi"}]}`,
		"a second value":                 `{"messages":[{"role":"user","content":"hi"}]} {}`,
		"a list":                         `[{"role":"user","content":"hi"}]`,
		"Messages":                       `{"Messages":[{"role":"user","content":"hi"}]}`,
		"not UTF-8":                      "{\"messages\":[{\"role\":\"user\",\"content\":\"\xff\"}]}",
		"a body over 1 MB and 16 KB":     `{"messages":[{"role":"user","content":"hi"}],"system":"` + strings.Repeat(" ", MaxAccountLaneAskBytes) + `"}`,
		"a picture where text must be":   testJSON(map[string]any{"messages": []any{testTurn("user", testImage("image/jpeg", jpeg))}}),
		"a number where content must be": `{"messages":[{"role":"user","content":7}]}`,
	} {
		response := call(s, http.MethodPost, testAskPath, bad)
		wantStatus, wantCode := http.StatusBadRequest, `"code":"account_lane_ask_invalid"`
		if name == "a body over 1 MB and 16 KB" {
			// Its length is known before it is read: the server's own limit answers.
			wantStatus, wantCode = http.StatusRequestEntityTooLarge, `"code":"invalid_request"`
		}
		if response.Code != wantStatus || !strings.Contains(response.Body.String(), wantCode) {
			t.Errorf("%s = %d %s", name, response.Code, response.Body.String())
		}
	}
	for _, bad := range []struct{ method, path, body, code string }{
		{http.MethodPost, "/v1/account-lanes/Claude_Account/ask", `{"messages":[{"role":"user","content":"hi"}]}`, "account_lane_ask_invalid"},
		{http.MethodGet, testAskPath, "", "invalid_request"},
		{http.MethodPost, "/v1/account-lanes/ask", `{"messages":[{"role":"user","content":"hi"}]}`, "invalid_request"},
		{http.MethodPost, "/v1/account-lanes/claude-account/ask/again", `{"messages":[{"role":"user","content":"hi"}]}`, "invalid_request"},
		{http.MethodPost, testAskPath + "?model=opus", `{"messages":[{"role":"user","content":"hi"}]}`, "invalid_request"},
	} {
		if response := call(s, bad.method, bad.path, bad.body); response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"`+bad.code+`"`) {
			t.Errorf("%s %s = %d %s", bad.method, bad.path, response.Code, response.Body.String())
		}
	}
	if len(backend.asks) != 0 {
		t.Fatalf("nothing refused reached the lane: %v", backend.asks)
	}

	// The relay's limits are met exactly: 39 turns (40 cannot alternate and end with the user), 20 parts, 20,000 and
	// 8,000 characters (an emoji counting two), three pictures of 300 KB, 1,024 tokens, and a body past the server's own
	// megabyte.
	big := testPicture("image/webp", 264_000)
	for name, good := range map[string]string{
		"39 turns, the most that alternate": testJSON(map[string]any{"messages": alternating(39)}),
		"20 parts":                          testJSON(map[string]any{"messages": []any{testTurn("user", many(20, testText("a")))}}),
		"20,000 characters":                 testJSON(map[string]any{"messages": []any{testTurn("user", strings.Repeat("a", 20_000))}}),
		"10,000 emoji":                      testJSON(map[string]any{"messages": []any{testTurn("user", []any{testText(strings.Repeat(emoji, 10_000))})}}),
		"a system of 4,000 emoji":           testJSON(map[string]any{"system": strings.Repeat(emoji, 4_000), "messages": []any{hi}}),
		"an empty system":                   testJSON(map[string]any{"system": "", "messages": []any{hi}}),
		"1,024 tokens":                      `{"max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`,
		"a picture of 300 KB":               testJSON(map[string]any{"messages": []any{testTurn("user", []any{testImage("image/png", testPicture("image/png", 300*1024))})}}),
		"three pictures":                    testJSON(map[string]any{"messages": []any{testTurn("user", many(2, testImage("image/jpeg", jpeg))), testTurn("assistant", "ok"), testTurn("user", []any{testImage("image/webp", testPicture("image/webp", 64))})}}),
		"a body past a megabyte":            testJSON(map[string]any{"messages": []any{testTurn("user", []any{testText("these?"), testImage("image/webp", big), testImage("image/webp", big), testImage("image/webp", big)})}}),
	} {
		if name == "a body past a megabyte" && (len(good) <= MaxRequestBodyBytes || len(good) > MaxAccountLaneAskBytes) {
			t.Fatalf("the body is %d bytes", len(good))
		}
		if response := call(s, http.MethodPost, testAskPath, good); response.Code != http.StatusOK {
			t.Errorf("%s = %d %s", name, response.Code, response.Body.String())
		}
	}
}

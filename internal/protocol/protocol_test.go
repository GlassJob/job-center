package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidatorStrictVsLenient(t *testing.T) {
	v, err := DefaultValidator()
	if err != nil {
		t.Fatal(err)
	}
	if v.Types() != 20 {
		t.Fatalf("want 20 message schemas, got %d", v.Types())
	}
	good := `{"protocol_version":"1.0","message_id":"m1","type":"service.heartbeat","timestamp":"2026-11-05T08:00:00.000Z","data":{"instance_id":"svc-host-1-abcdef","status":"ONLINE","running":0}}`
	if err := v.Validate(TypeServiceHeartbeat, []byte(good), true); err != nil {
		t.Fatalf("good: %v", err)
	}
	extra := strings.Replace(good, `"running":0`, `"running":0,"future_field":1`, 1)
	if err := v.Validate(TypeServiceHeartbeat, []byte(extra), true); err == nil {
		t.Fatal("strict should reject unknown field")
	}
	if err := v.Validate(TypeServiceHeartbeat, []byte(extra), false); err != nil {
		t.Fatalf("lenient should accept unknown field: %v", err)
	}
	both := strings.Replace(extra, `"running":0`, `"running":-1`, 1)
	if err := v.Validate(TypeServiceHeartbeat, []byte(both), false); err == nil {
		t.Fatal("lenient must still reject type/range errors")
	}
	missing := strings.Replace(good, `"status":"ONLINE",`, ``, 1)
	if err := v.Validate(TypeServiceHeartbeat, []byte(missing), false); err == nil {
		t.Fatal("missing required must be rejected")
	}
}

func TestNewMessageValidates(t *testing.T) {
	v, _ := DefaultValidator()
	b, id, err := NewMessage(TypeSystemNotice, "", "", SystemNotice{Code: ErrSessionReplaced, Message: "x"})
	if err != nil || id == "" {
		t.Fatal(err)
	}
	if err := v.Validate(TypeSystemNotice, b, true); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	var env Envelope
	_ = json.Unmarshal(b, &env)
	if env.RequestID != nil {
		t.Fatal("request_id should be omitted")
	}
	b, _, _ = NewMessage(TypeServiceRegisterAck, "m-1", "", RegisterAck{OK: true, InstanceStatus: "ONLINE",
		AcceptedTasks: []AcceptedTask{{TaskCode: "demo.hello", TaskID: 1}},
		Warnings:      []TaskIssue{{TaskCode: "demo.hello", Code: ErrRegisterInvalidCron, Message: "bad"}}})
	if err := v.Validate(TypeServiceRegisterAck, b, true); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
}

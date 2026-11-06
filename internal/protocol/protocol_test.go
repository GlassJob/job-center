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
	if v.Types() != 21 {
		t.Fatalf("want 21 message schemas, got %d", v.Types())
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

func TestTaskCheckpointAndStateSchemas(t *testing.T) {
	v, err := DefaultValidator()
	if err != nil {
		t.Fatal(err)
	}
	execBytes, _, err := NewMessage(TypeTaskExecute, "", "", TaskExecute{
		ExecutionID:    "01JC0000000000000000000001",
		AttemptID:      "01JC0000000000000000000002",
		AttemptNumber:  1,
		LeaseToken:     "0123456789abcdef0123456789abcdef",
		LeaseExpiresAt: "2026-11-06T09:01:00.000Z",
		TaskCode:       "demo.incremental_sync",
		Params:         json.RawMessage(`{}`),
		State:          json.RawMessage(`{}`),
		StateVersion:   0,
		TimeoutSeconds: 60,
		DeadlineAt:     "2026-11-06T09:02:00.000Z",
		TriggerType:    "CRON",
		HandlerVersion: "1.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Validate(TypeTaskExecute, execBytes, true); err != nil {
		t.Fatalf("TaskExecute strict validate: %v (%s)", err, execBytes)
	}
	if !strings.Contains(string(execBytes), `"state_version":0`) || !strings.Contains(string(execBytes), `"state":{}`) {
		t.Fatalf("expected state={} and state_version=0 in %s", execBytes)
	}

	cpGood := `{"protocol_version":"1.0","message_id":"cp1","type":"task.checkpoint","timestamp":"2026-11-06T09:00:10.000Z","data":{"attempt_id":"01JC0000000000000000000002","lease_token":"0123456789abcdef0123456789abcdef","state":{"cursor":"abc"}}}`
	if err := v.Validate(TypeTaskCheckpoint, []byte(cpGood), true); err != nil {
		t.Fatalf("checkpoint valid: %v", err)
	}
	cpBad := `{"protocol_version":"1.0","message_id":"cp2","type":"task.checkpoint","timestamp":"2026-11-06T09:00:10.000Z","data":{"attempt_id":"01JC0000000000000000000002","lease_token":"0123456789abcdef0123456789abcdef","state":123}}`
	if err := v.Validate(TypeTaskCheckpoint, []byte(cpBad), false); err == nil {
		t.Fatal("checkpoint non-object state must fail")
	}

	compGood := `{"protocol_version":"1.0","message_id":"c1","type":"task.completed","timestamp":"2026-11-06T09:00:20.000Z","data":{"attempt_id":"01JC0000000000000000000002","lease_token":"0123456789abcdef0123456789abcdef","finished_at":"2026-11-06T09:00:20.000Z","result":{"ok":true},"next_state":{"cursor":"xyz"}}}`
	if err := v.Validate(TypeTaskCompleted, []byte(compGood), true); err != nil {
		t.Fatalf("completed with next_state valid: %v", err)
	}

	ackBytes, _, _ := NewMessage(TypeTaskResultAck, "cp1", "", TaskResultAck{
		Status: "REJECTED", AttemptID: "01JC0000000000000000000002",
		Code: ErrLeaseInvalid, ErrorCode: ErrLeaseInvalid, Message: "stale lease",
	})
	if err := v.Validate(TypeTaskResultAck, ackBytes, true); err != nil {
		t.Fatalf("result_ack strict validate: %v (%s)", err, ackBytes)
	}
}

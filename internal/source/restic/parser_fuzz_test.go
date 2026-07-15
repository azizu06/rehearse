package restic

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

const fuzzPrivateMarker = "private-fuzz-marker"

func FuzzDecodeResticMessageType(f *testing.F) {
	f.Add([]byte(`{"message_type":"future_message","private":"` + fuzzPrivateMarker + `"}`))
	f.Add([]byte(`{"message_type":"summary"}`))
	f.Add([]byte(`{"private":"` + fuzzPrivateMarker + `"}`))
	f.Add([]byte(`{"message_type":null,"private":"` + fuzzPrivateMarker + `"}`))
	f.Add([]byte(`{"message_type":false,"private":"` + fuzzPrivateMarker + `"}`))
	f.Add([]byte(`{"messAge_tYpe":"future_message"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		messageType, err := decodeResticMessageType(data)
		if err != nil {
			if !errors.Is(err, errInvalidResticMessageType) {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Contains(err.Error(), fuzzPrivateMarker) {
				t.Fatalf("private marker leaked through error: %v", err)
			}
			return
		}

		var object map[string]json.RawMessage
		if err := json.Unmarshal(data, &object); err != nil {
			t.Fatalf("decoder accepted non-object JSON: %q", data)
		}
		raw, ok := object["message_type"]
		if !ok {
			t.Fatalf("decoder accepted missing message_type: %q", data)
		}
		var want string
		if err := json.Unmarshal(raw, &want); err != nil || messageType != want {
			t.Fatalf("message_type = %q, want valid string %q", messageType, want)
		}
	})
}

func FuzzDecodeRestoreSummary(f *testing.F) {
	f.Add([]byte(`{"message_type":"summary"}`))
	f.Add([]byte(`{"message_type":"summary","files_restored":1,"total_files":1,"bytes_restored":2,"total_bytes":2}`))
	f.Add([]byte(`{"message_type":"summary","files_restored":1,"private":"` + fuzzPrivateMarker + `"}`))
	f.Add([]byte(`{"message_type":"summary","total_files":null,"private":"` + fuzzPrivateMarker + `"}`))
	f.Add([]byte(`{"message_type":"future_message"}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		summary, err := decodeRestoreSummary(data)
		if err != nil {
			if !errors.Is(err, errInvalidRestoreSummary) {
				t.Fatalf("unexpected error: %v", err)
			}
			if strings.Contains(err.Error(), fuzzPrivateMarker) {
				t.Fatalf("private marker leaked through error: %v", err)
			}
			return
		}
		if summary.FilesRestored > summary.TotalFiles || summary.BytesRestored > summary.TotalBytes {
			t.Fatalf("invalid summary accepted: %+v", summary)
		}
		messageType, err := decodeResticMessageType(data)
		if err != nil || messageType != "summary" {
			t.Fatalf("non-summary accepted as promotion token: %q", data)
		}
	})
}

func FuzzValidateStructuredExit(f *testing.F) {
	f.Add([]byte(`{"message_type":"exit_error","code":12}`))
	f.Add([]byte("{\"message_type\":\"future_error\",\"private\":\"" + fuzzPrivateMarker + "\"}\n{\"message_type\":\"exit_error\",\"code\":12}"))
	f.Add([]byte(`{"message_type":null,"private":"` + fuzzPrivateMarker + `"}`))
	f.Add([]byte(`{"message_type":"exit_error","code":10,"private":"` + fuzzPrivateMarker + `"}`))
	f.Add([]byte(`{"message_type":"exit_error","Code":12}`))
	f.Add([]byte(`{"message_type":"exit_error","code":null}`))
	f.Add([]byte(`{"message_type":"exit_error","code":"12"}`))
	f.Add([]byte(`{"message_type":"exit_error","code":12,"code":12}`))

	f.Fuzz(func(t *testing.T, data []byte) {
		err := validateStructuredExit(data, 12)
		if err == nil {
			if !independentlyValidStructuredExit(data, 12) {
				t.Fatalf("structured-exit parser accepted input rejected by independent JSON oracle: %q", data)
			}
			return
		}
		if !errors.Is(err, errInvalidStructuredExit) && !errors.Is(err, errInconsistentExitCode) && !errors.Is(err, errMissingStructuredExit) {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(err.Error(), fuzzPrivateMarker) {
			t.Fatalf("private marker leaked through error: %v", err)
		}
	})
}

// independentlyValidStructuredExit intentionally shares no production parser
// helpers or structs. It is the fuzz oracle for the accepted JSON-line shape.
func independentlyValidStructuredExit(data []byte, expectedCode int64) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	exitErrorSeen := false
	for {
		var object map[string]any
		err := decoder.Decode(&object)
		if errors.Is(err, io.EOF) {
			return exitErrorSeen
		}
		if err != nil || object == nil {
			return false
		}
		messageType, ok := object["message_type"].(string)
		if !ok {
			return false
		}
		if messageType != "exit_error" {
			continue
		}
		code, ok := object["code"].(json.Number)
		if !ok {
			return false
		}
		value, err := code.Int64()
		if err != nil || value != expectedCode {
			return false
		}
		exitErrorSeen = true
	}
}

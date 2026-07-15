package restic

import (
	"encoding/json"
	"errors"
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

	f.Fuzz(func(t *testing.T, data []byte) {
		err := validateStructuredExit(data, 12)
		if err == nil {
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
